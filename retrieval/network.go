package retrieval

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	cborutil "github.com/filecoin-project/go-cbor-util"
	"github.com/ipfs/go-cid"
	"github.com/jpillora/backoff"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/mux"
	"github.com/libp2p/go-libp2p-core/network"
	peer "github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/myelnet/pop/internal/utils"
	"github.com/myelnet/pop/retrieval/deal"
	cbg "github.com/whyrusleeping/cbor-gen"
)

// We stay compatible with lotus nodes so we can retrieve from lotus providers too

// FilQueryProtocolID is the protocol for querying information about retrieval
// deal parameters from Filecoin storage miners
const FilQueryProtocolID = protocol.ID("/fil/retrieval/qry/1.0.0")

// PopQueryProtocolID is the protocol for exchanging information about retrieval
// deal parameters from retrieval providers
const PopQueryProtocolID = protocol.ID("/myel/pop/query/1.0")

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

// OfferReceiver is the API for handling data coming in on
// both query and deal streams
type OfferReceiver interface {
	// Receive queues up an offer
	Receive(peer.AddrInfo, deal.QueryResponse)
	// IsRecipient checks if we are actually the peer expecting this offer
	IsRecipient(string) bool
	// Recipient returns the peer we think this message should be forwarded to
	Recipient(string) (peer.ID, error)
}

// QueryNetwork is the API for creating query and deal streams and
// delegating responders to those streams.
type QueryNetwork interface {
	//  NewQueryStream creates a new RetrievalQueryStream implementer using the provided peer.ID
	NewQueryStream(peer.ID) (QueryStream, error)

	// StopHandlingRequests unsets the RetrievalReceiver and would perform any other necessary
	// shutdown logic.
	StopHandlingRequests() error

	// ID returns the peer id of the host for this network
	ID() peer.ID

	// Addrs returns the host addresses
	Addrs() ([]ma.Multiaddr, error)

	// AddAddrs adds the given multi-addrs to the peerstore for the passed peer ID
	AddAddrs(peer.ID, []ma.Multiaddr)

	// Receiver returns the channel
	Receiver() OfferReceiver

	// Start receiving offers
	Start(OfferReceiver)
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
			PopQueryProtocolID,
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
	host                  host.Host
	maxStreamOpenAttempts float64
	minAttemptDuration    time.Duration
	maxAttemptDuration    time.Duration
	supportedProtocols    []protocol.ID
	receiver              OfferReceiver
}

func (impl *Libp2pQueryNetwork) Start(r OfferReceiver) {
	impl.receiver = r
	for _, proto := range impl.supportedProtocols {
		impl.host.SetStreamHandler(proto, impl.handleNewQueryStream)
	}
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
		fmt.Printf("trying again %v\n", err)

		nAttempts := b.Attempt()
		if nAttempts == impl.maxStreamOpenAttempts {
			return nil, fmt.Errorf("exhausted %d attempts but failed to open stream, err: %w", int(impl.maxStreamOpenAttempts), err)
		}
		d := b.Duration()
		time.Sleep(d)
	}
}

// StopHandlingRequests unsets the RetrievalReceiver and would perform any other necessary
// shutdown logic.
func (impl *Libp2pQueryNetwork) StopHandlingRequests() error {
	for _, proto := range impl.supportedProtocols {
		impl.host.RemoveStreamHandler(proto)
	}
	return nil
}

func (impl *Libp2pQueryNetwork) handleNewQueryStream(s network.Stream) {
	r := impl.receiver
	buffered := bufio.NewReaderSize(s, 16)

	var buf bytes.Buffer
	msg, err := PeekResponseMsg(buffered, &buf)
	if err != nil {
		fmt.Println("failed to peek message", err)
		return
	}
	if len(msg) == 0 {
		return
	}
	// Get the index where to split
	is, err := strconv.ParseInt(msg[:2], 10, 64)
	if err != nil {
		fmt.Println("failed to parse index", err)
		return
	}
	msgID := msg[2 : is+2]
	// The receiver should know if we issued the query if it's not the case
	// it means we must forward it to whichever peer sent us the query
	if !r.IsRecipient(msgID) {
		to, err := r.Recipient(msgID)
		if err != nil {
			fmt.Println("failed to find message recipient", err)
			return
		}
		w, err := impl.openStream(context.Background(), to, impl.supportedProtocols)
		if err != nil {
			fmt.Println("failed to open stream", err)
			return
		}
		if _, err := io.Copy(w, &buf); err != nil {
			fmt.Println("failed to forward buffer", err)
		}
		return
	}
	rec, err := utils.AddrBytesToAddrInfo([]byte(msg[is+2:]))
	if err != nil {
		fmt.Println("failed to parse addr bytes", err)
		return
	}

	var resp deal.QueryResponse
	if err := resp.UnmarshalCBOR(&buf); err != nil && !errors.Is(err, io.EOF) {
		fmt.Println("failed to read query response", err)
		return
	}

	r.Receive(*rec, resp)
}

// ID returns the host peer ID
func (impl *Libp2pQueryNetwork) ID() peer.ID {
	return impl.host.ID()
}

// AddAddrs adds a new peer into the host peerstore
func (impl *Libp2pQueryNetwork) AddAddrs(p peer.ID, addrs []ma.Multiaddr) {
	impl.host.Peerstore().AddAddrs(p, addrs, 8*time.Hour)
}

// Addrs returns the host's p2p addresses
func (impl *Libp2pQueryNetwork) Addrs() ([]ma.Multiaddr, error) {
	return peer.AddrInfoToP2pAddrs(host.InfoFromHost(impl.host))
}

// Receiver returns the interface between the network and the exchange
func (impl *Libp2pQueryNetwork) Receiver() OfferReceiver {
	return impl.receiver
}

// PeekResponseMsg decodes the Message field only and returns the value while copying the bytes in a buffer
func PeekResponseMsg(r io.Reader, buf *bytes.Buffer) (string, error) {
	tr := io.TeeReader(r, buf)
	br := cbg.GetPeeker(tr)
	scratch := make([]byte, 8)
	_, n, err := cbg.CborReadHeaderBuf(br, scratch)
	if err != nil {
		return "", err
	}
	var name string
	for i := uint64(0); i < n; i++ {
		{
			sval, err := cbg.ReadStringBuf(br, scratch)
			if err != nil {
				return "", err
			}
			name = string(sval)
		}
		if name == "Message" {
			sval, err := cbg.ReadStringBuf(br, scratch)
			if err != nil {
				return "", err
			}
			return string(sval), nil
		}
		cbg.ScanForLinks(br, func(cid.Cid) {})
	}
	return "", errors.New("no Message field")
}
