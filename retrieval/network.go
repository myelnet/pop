package retrieval

import (
	"bufio"
	"context"
	"fmt"
	"time"

	cborutil "github.com/filecoin-project/go-cbor-util"
	"github.com/jpillora/backoff"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/mux"
	"github.com/libp2p/go-libp2p-core/network"
	peer "github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/myelnet/go-hop-exchange/retrieval/deal"
)

// We stay compatible with lotus nodes so we can retrieve from lotus providers too

// FilQueryProtocolID is the protocol for querying information about retrieval
// deal parameters from Filecoin storage miners
const FilQueryProtocolID = protocol.ID("/fil/retrieval/qry/1.0.0")

// HopQueryProtocolID is the protocol for exchanging information about retrieval
// deal parameters from retrieval providers
const HopQueryProtocolID = protocol.ID("/myel/hop/query/1.0")

// These are the required interfaces that must be implemented to send and receive data
// for retrieval queries and deals.

// QueryStream is the API needed to send and receive retrieval query
// data over data-transfer network.
type QueryStream interface {
	ReadQuery() (deal.Query, error)
	WriteQuery(deal.Query) error
	ReadQueryResponse() (deal.QueryResponse, error)
	WriteQueryResponse(deal.QueryResponse) error
	Close() error
	OtherPeer() peer.ID
}

// QueryReceiver is the API for handling data coming in on
// both query and deal streams
type QueryReceiver interface {
	// HandleQueryStream sends and receives data-transfer data via the
	// RetrievalQueryStream provided
	HandleQueryStream(QueryStream)
}

// QueryNetwork is the API for creating query and deal streams and
// delegating responders to those streams.
type QueryNetwork interface {
	//  NewQueryStream creates a new RetrievalQueryStream implementer using the provided peer.ID
	NewQueryStream(peer.ID) (QueryStream, error)

	// SetDelegate sets a QueryReceiver implementer to handle stream data
	SetDelegate(QueryReceiver) error

	// StopHandlingRequests unsets the RetrievalReceiver and would perform any other necessary
	// shutdown logic.
	StopHandlingRequests() error

	// ID returns the peer id of the host for this network
	ID() peer.ID

	// AddAddrs adds the given multi-addrs to the peerstore for the passed peer ID
	AddAddrs(peer.ID, []ma.Multiaddr)
}

type queryStream struct {
	p        peer.ID
	rw       mux.MuxedStream
	buffered *bufio.Reader
}

func (qs *queryStream) ReadQuery() (deal.Query, error) {
	var q deal.Query

	if err := q.UnmarshalCBOR(qs.buffered); err != nil {
		return deal.Query{}, err

	}

	return q, nil
}

func (qs *queryStream) WriteQuery(q deal.Query) error {
	return cborutil.WriteCborRPC(qs.rw, &q)
}

func (qs *queryStream) ReadQueryResponse() (deal.QueryResponse, error) {
	var resp deal.QueryResponse

	if err := resp.UnmarshalCBOR(qs.buffered); err != nil {
		return deal.QueryResponse{}, err
	}

	return resp, nil
}

func (qs *queryStream) WriteQueryResponse(qr deal.QueryResponse) error {
	return cborutil.WriteCborRPC(qs.rw, &qr)
}

func (qs *queryStream) Close() error {
	return qs.rw.Close()
}

func (qs *queryStream) OtherPeer() peer.ID {
	return qs.p
}

const defaultMaxStreamOpenAttempts = 5
const defaultMinAttemptDuration = 1 * time.Second
const defaultMaxAttemptDuration = 5 * time.Minute

// Option is an option for configuring the libp2p storage market network
type Option func(*Libp2pQueryNetwork)

// RetryParameters changes the default parameters around connection reopening
func RetryParameters(minDuration time.Duration, maxDuration time.Duration, attempts float64) Option {
	return func(impl *Libp2pQueryNetwork) {
		impl.maxStreamOpenAttempts = attempts
		impl.minAttemptDuration = minDuration
		impl.maxAttemptDuration = maxDuration
	}
}

// SupportedProtocols sets what protocols this network instances listens on
func SupportedProtocols(supportedProtocols []protocol.ID) Option {
	return func(impl *Libp2pQueryNetwork) {
		impl.supportedProtocols = supportedProtocols
	}
}

// NewQueryNetwork constructs a new instance of the QueryNetwork from a
// libp2p host
func NewQueryNetwork(h host.Host, options ...Option) *Libp2pQueryNetwork {
	impl := &Libp2pQueryNetwork{
		host:                  h,
		maxStreamOpenAttempts: defaultMaxStreamOpenAttempts,
		minAttemptDuration:    defaultMinAttemptDuration,
		maxAttemptDuration:    defaultMaxAttemptDuration,
		supportedProtocols: []protocol.ID{
			FilQueryProtocolID,
			HopQueryProtocolID,
		},
	}
	for _, option := range options {
		option(impl)
	}
	return impl
}

// Libp2pQueryNetwork transforms the libp2p host interface, which sends and receives
// NetMessage objects, into the graphsync network interface.
// It implements the QueryNetwork API.
type Libp2pQueryNetwork struct {
	host host.Host
	// inbound messages from the network are forwarded to the receiver
	receiver              QueryReceiver
	maxStreamOpenAttempts float64
	minAttemptDuration    time.Duration
	maxAttemptDuration    time.Duration
	supportedProtocols    []protocol.ID
}

// NewQueryStream creates a new QueryStream using the provided peer.ID
func (impl *Libp2pQueryNetwork) NewQueryStream(id peer.ID) (QueryStream, error) {
	s, err := impl.openStream(context.Background(), id, impl.supportedProtocols)
	if err != nil {
		return nil, err
	}
	buffered := bufio.NewReaderSize(s, 16)
	return &queryStream{p: id, rw: s, buffered: buffered}, nil
}

func (impl *Libp2pQueryNetwork) openStream(ctx context.Context, id peer.ID, protocols []protocol.ID) (network.Stream, error) {
	b := &backoff.Backoff{
		Min:    impl.minAttemptDuration,
		Max:    impl.maxAttemptDuration,
		Factor: impl.maxStreamOpenAttempts,
		Jitter: true,
	}

	for {
		s, err := impl.host.NewStream(ctx, id, protocols...)
		if err == nil {
			return s, err
		}

		nAttempts := b.Attempt()
		if nAttempts == impl.maxStreamOpenAttempts {
			return nil, fmt.Errorf("exhausted %d attempts but failed to open stream, err: %w", int(impl.maxStreamOpenAttempts), err)
		}
		d := b.Duration()
		time.Sleep(d)
	}
}

// SetDelegate sets a RetrievalReceiver to handle stream data
func (impl *Libp2pQueryNetwork) SetDelegate(r QueryReceiver) error {
	impl.receiver = r
	for _, proto := range impl.supportedProtocols {
		impl.host.SetStreamHandler(proto, impl.handleNewQueryStream)
	}
	return nil
}

// StopHandlingRequests unsets the RetrievalReceiver and would perform any other necessary
// shutdown logic.
func (impl *Libp2pQueryNetwork) StopHandlingRequests() error {
	impl.receiver = nil
	for _, proto := range impl.supportedProtocols {
		impl.host.RemoveStreamHandler(proto)
	}
	return nil
}

func (impl *Libp2pQueryNetwork) handleNewQueryStream(s network.Stream) {
	if impl.receiver == nil {
		fmt.Printf("no receiver set")
		s.Reset()
		return
	}
	remotePID := s.Conn().RemotePeer()
	buffered := bufio.NewReaderSize(s, 16)
	var qs QueryStream
	qs = &queryStream{remotePID, s, buffered}
	impl.receiver.HandleQueryStream(qs)
}

// ID returns the host peer ID
func (impl *Libp2pQueryNetwork) ID() peer.ID {
	return impl.host.ID()
}

// AddAddrs adds a new peer into the host peerstore
func (impl *Libp2pQueryNetwork) AddAddrs(p peer.ID, addrs []ma.Multiaddr) {
	impl.host.Peerstore().AddAddrs(p, addrs, 8*time.Hour)
}
