package hop

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
)

// Let's try to stay compatible with lotus nodes so we can retrieve from lotus
// providers too

// FilQueryProtocolID is the protocol for querying information about retrieval
// deal parameters from Filecoin storage miners
const FilQueryProtocolID = protocol.ID("/fil/retrieval/qry/1.0.0")

// HopQueryProtocolID is the protocol for exchanging information about retrieval
// deal parameters from retrieval providers
const HopQueryProtocolID = protocol.ID("/myel/hop/query/1.0")

// These are the required interfaces that must be implemented to send and receive data
// for retrieval queries and deals.

// RetrievalQueryStream is the API needed to send and receive retrieval query
// data over data-transfer network.
type RetrievalQueryStream interface {
	ReadQuery() (Query, error)
	WriteQuery(Query) error
	ReadQueryResponse() (QueryResponse, error)
	WriteQueryResponse(QueryResponse) error
	Close() error
	OtherPeer() peer.ID
}

// RetrievalReceiver is the API for handling data coming in on
// both query and deal streams
type RetrievalReceiver interface {
	// HandleQueryStream sends and receives data-transfer data via the
	// RetrievalQueryStream provided
	HandleQueryStream(RetrievalQueryStream)
}

// RetrievalMarketNetwork is the API for creating query and deal streams and
// delegating responders to those streams.
type RetrievalMarketNetwork interface {
	//  NewQueryStream creates a new RetrievalQueryStream implementer using the provided peer.ID
	NewQueryStream(peer.ID) (RetrievalQueryStream, error)

	// SetDelegate sets a RetrievalReceiver implementer to handle stream data
	SetDelegate(RetrievalReceiver) error

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

func (qs *queryStream) ReadQuery() (Query, error) {
	var q Query

	if err := q.UnmarshalCBOR(qs.buffered); err != nil {
		fmt.Printf("Unable to unmarshall Query: %v", err)
		return Query{}, err

	}

	return q, nil
}

func (qs *queryStream) WriteQuery(q Query) error {
	return cborutil.WriteCborRPC(qs.rw, &q)
}

func (qs *queryStream) ReadQueryResponse() (QueryResponse, error) {
	var resp QueryResponse

	if err := resp.UnmarshalCBOR(qs.buffered); err != nil {
		fmt.Printf("Unable to unmarshall QueryResponse: %v", err)
		return QueryResponse{}, err
	}

	return resp, nil
}

func (qs *queryStream) WriteQueryResponse(qr QueryResponse) error {
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
type Option func(*libp2pRetrievalMarketNetwork)

// RetryParameters changes the default parameters around connection reopening
func RetryParameters(minDuration time.Duration, maxDuration time.Duration, attempts float64) Option {
	return func(impl *libp2pRetrievalMarketNetwork) {
		impl.maxStreamOpenAttempts = attempts
		impl.minAttemptDuration = minDuration
		impl.maxAttemptDuration = maxDuration
	}
}

// SupportedProtocols sets what protocols this network instances listens on
func SupportedProtocols(supportedProtocols []protocol.ID) Option {
	return func(impl *libp2pRetrievalMarketNetwork) {
		impl.supportedProtocols = supportedProtocols
	}
}

// NewFromLibp2pHost constructs a new instance of the RetrievalMarketNetwork from a
// libp2p host
func NewFromLibp2pHost(h host.Host, options ...Option) RetrievalMarketNetwork {
	impl := &libp2pRetrievalMarketNetwork{
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

// libp2pRetrievalMarketNetwork transforms the libp2p host interface, which sends and receives
// NetMessage objects, into the graphsync network interface.
// It implements the RetrievalMarketNetwork API.
type libp2pRetrievalMarketNetwork struct {
	host host.Host
	// inbound messages from the network are forwarded to the receiver
	receiver              RetrievalReceiver
	maxStreamOpenAttempts float64
	minAttemptDuration    time.Duration
	maxAttemptDuration    time.Duration
	supportedProtocols    []protocol.ID
}

//  NewQueryStream creates a new RetrievalQueryStream using the provided peer.ID
func (impl *libp2pRetrievalMarketNetwork) NewQueryStream(id peer.ID) (RetrievalQueryStream, error) {
	s, err := impl.openStream(context.Background(), id, impl.supportedProtocols)
	if err != nil {
		return nil, err
	}
	fmt.Println(s.Protocol())
	buffered := bufio.NewReaderSize(s, 16)
	return &queryStream{p: id, rw: s, buffered: buffered}, nil
}

func (impl *libp2pRetrievalMarketNetwork) openStream(ctx context.Context, id peer.ID, protocols []protocol.ID) (network.Stream, error) {
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
func (impl *libp2pRetrievalMarketNetwork) SetDelegate(r RetrievalReceiver) error {
	impl.receiver = r
	for _, proto := range impl.supportedProtocols {
		impl.host.SetStreamHandler(proto, impl.handleNewQueryStream)
	}
	return nil
}

// StopHandlingRequests unsets the RetrievalReceiver and would perform any other necessary
// shutdown logic.
func (impl *libp2pRetrievalMarketNetwork) StopHandlingRequests() error {
	impl.receiver = nil
	for _, proto := range impl.supportedProtocols {
		impl.host.RemoveStreamHandler(proto)
	}
	return nil
}

func (impl *libp2pRetrievalMarketNetwork) handleNewQueryStream(s network.Stream) {
	if impl.receiver == nil {
		fmt.Printf("no receiver set")
		s.Reset()
		return
	}
	remotePID := s.Conn().RemotePeer()
	buffered := bufio.NewReaderSize(s, 16)
	var qs RetrievalQueryStream
	qs = &queryStream{remotePID, s, buffered}
	impl.receiver.HandleQueryStream(qs)
}

func (impl *libp2pRetrievalMarketNetwork) ID() peer.ID {
	return impl.host.ID()
}

func (impl *libp2pRetrievalMarketNetwork) AddAddrs(p peer.ID, addrs []ma.Multiaddr) {
	impl.host.Peerstore().AddAddrs(p, addrs, 8*time.Hour)
}
