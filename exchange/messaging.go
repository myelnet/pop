package exchange

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
	datatransfer "github.com/filecoin-project/go-data-transfer"
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

// PopRequestProtocolID is the protocol for requesting caches to store new content
const PopRequestProtocolID = protocol.ID("/myel/pop/request/1.0")

// Request describes the content to pull
type Request struct {
	PayloadCID cid.Cid
	Size       uint64
}

// Type defines AddRequest as a datatransfer voucher for pulling the data from the request
func (Request) Type() datatransfer.TypeIdentifier {
	return "DispatchRequestVoucher"
}

// MultiStream reads and writes all the exchange messages
type MultiStream interface {
	ReadRequest() (Request, error)
	WriteRequest(Request) error
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
	ReceiveOffer(peer.AddrInfo, deal.QueryResponse)
}

// RequestReceiver handles data coming from the Request mux
type RequestReceiver interface {
	// Receive queues up a request
	ReceiveRequest(peer.ID, Request)
}

// MessageTracker returns metadata about messages so we know if they're destined to this host
// or should be forwarded
type MessageTracker interface {
	// Published checks if we are actually the peer expecting this offer
	Published(string) bool
	// Sender returns the peer we think this message should be forwarded to
	Sender(string) (peer.ID, error)
}

type multiStream struct {
	p        peer.ID
	rw       mux.MuxedStream
	buffered *bufio.Reader
}

func (ms *multiStream) ReadRequest() (Request, error) {
	var m Request
	if err := m.UnmarshalCBOR(ms.buffered); err != nil {
		return Request{}, err
	}
	return m, nil
}

func (ms *multiStream) WriteRequest(m Request) error {
	return cborutil.WriteCborRPC(ms.rw, &m)
}

func (ms *multiStream) ReadQuery() (deal.Query, error) {
	var q deal.Query

	if err := q.UnmarshalCBOR(ms.buffered); err != nil {
		return deal.Query{}, err

	}

	return q, nil
}

func (ms *multiStream) WriteQuery(q deal.Query) error {
	return cborutil.WriteCborRPC(ms.rw, &q)
}

func (ms *multiStream) ReadQueryResponse() (deal.QueryResponse, error) {
	var resp deal.QueryResponse

	if err := resp.UnmarshalCBOR(ms.buffered); err != nil {
		return deal.QueryResponse{}, err
	}

	return resp, nil
}

func (ms *multiStream) WriteQueryResponse(qr deal.QueryResponse) error {
	return cborutil.WriteCborRPC(ms.rw, &qr)
}

func (ms *multiStream) Close() error {
	return ms.rw.Close()
}

func (ms *multiStream) OtherPeer() peer.ID {
	return ms.p
}

const defaultMaxStreamOpenAttempts = 5
const defaultMinAttemptDuration = 1 * time.Second
const defaultMaxAttemptDuration = 5 * time.Minute

// MgOption is an option for configuring the messaging service
type MgOption func(*Messaging)

// RetryParameters changes the default parameters around connection reopening
func RetryParameters(minDuration time.Duration, maxDuration time.Duration, attempts float64) MgOption {
	return func(mg *Messaging) {
		mg.maxStreamOpenAttempts = attempts
		mg.minAttemptDuration = minDuration
		mg.maxAttemptDuration = maxDuration
	}
}

// NetMetadata exposes accessor to access network metadata and make broker decisions
func NetMetadata(mt MessageTracker) MgOption {
	return func(mg *Messaging) {
		mg.meta = mt
	}
}

// NewMessaging constructs a new instance of the Messaging service from a libp2p host
func NewMessaging(h host.Host, options ...MgOption) *Messaging {
	mg := &Messaging{
		host:                  h,
		maxStreamOpenAttempts: defaultMaxStreamOpenAttempts,
		minAttemptDuration:    defaultMinAttemptDuration,
		maxAttemptDuration:    defaultMaxAttemptDuration,
		queryProtocols: []protocol.ID{
			FilQueryProtocolID,
			PopQueryProtocolID,
		},
		requestProtocols: []protocol.ID{
			PopRequestProtocolID,
		},
	}
	for _, option := range options {
		option(mg)
	}
	mg.host.SetStreamHandler(PopQueryProtocolID, mg.handleQueryResponse)
	mg.host.SetStreamHandler(PopRequestProtocolID, mg.handleRequest)

	return mg
}

// Messaging manages all the messages sent to coordinate peer exchanges
type Messaging struct {
	host                  host.Host
	maxStreamOpenAttempts float64
	minAttemptDuration    time.Duration
	maxAttemptDuration    time.Duration
	queryProtocols        []protocol.ID
	requestProtocols      []protocol.ID
	reqr                  RequestReceiver
	ofrr                  OfferReceiver
	meta                  MessageTracker
}

// NewRequestStream to send a Request message to the given peer
func (mg *Messaging) NewRequestStream(dest peer.ID) (MultiStream, error) {
	s, err := mg.openStream(context.Background(), dest, mg.requestProtocols)
	if err != nil {
		return nil, err
	}
	buffered := bufio.NewReaderSize(s, 16)
	return &multiStream{p: dest, rw: s, buffered: buffered}, nil
}

// NewQueryStream creates a new MultiStream using the provided peer.ID to handle the Query protocols
func (mg *Messaging) NewQueryStream(dest peer.ID) (MultiStream, error) {
	s, err := mg.openStream(context.Background(), dest, mg.queryProtocols)
	if err != nil {
		return nil, err
	}
	buffered := bufio.NewReaderSize(s, 16)
	return &multiStream{p: dest, rw: s, buffered: buffered}, nil
}

func (mg *Messaging) openStream(ctx context.Context, id peer.ID, protocols []protocol.ID) (network.Stream, error) {
	b := &backoff.Backoff{
		Min:    mg.minAttemptDuration,
		Max:    mg.maxAttemptDuration,
		Factor: mg.maxStreamOpenAttempts,
		Jitter: true,
	}

	for {
		s, err := mg.host.NewStream(ctx, id, protocols...)
		if err == nil {
			return s, err
		}
		fmt.Println("trying again", err)

		nAttempts := b.Attempt()
		if nAttempts == mg.maxStreamOpenAttempts {
			return nil, fmt.Errorf("exhausted %d attempts but failed to open stream, err: %w", int(mg.maxStreamOpenAttempts), err)
		}
		d := b.Duration()
		time.Sleep(d)
	}
}

func (mg *Messaging) handleRequest(s network.Stream) {
	if mg.reqr == nil {
		s.Reset()
		return
	}
	remotePID := s.Conn().RemotePeer()
	buffered := bufio.NewReaderSize(s, 16)
	rs := &multiStream{remotePID, s, buffered}
	defer rs.Close()
	req, err := rs.ReadRequest()
	if err != nil {
		return
	}
	mg.reqr.ReceiveRequest(remotePID, req)
}

// handleQueryResponse reads a QueryResponse message from an incoming stream, reads the Message
// if a message is provided, it parses the ID embedded in it and checks if the reference matches
// one we published. If we did publish it means we are expecting the response to we read the response
// and send it to the receiver if not and we have a sender for the message reference we forward it back
// if there is no message attached with the response it might be sent from a Filecoin storage miner so
// we still try to send it to the receiver.
func (mg *Messaging) handleQueryResponse(s network.Stream) {
	buffered := bufio.NewReaderSize(s, 16)
	defer s.Close()

	buf := new(bytes.Buffer)
	msg, err := PeekResponseMsg(buffered, buf)
	if err != nil {
		fmt.Println("failed to peek message", err)
		return
	}
	// TODO: handle messages from Filecoin miners
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
	if !mg.meta.Published(msgID) {
		to, err := mg.meta.Sender(msgID)
		if err != nil {
			fmt.Println("failed to find message recipient", err)
			return
		}
		w, err := mg.openStream(context.Background(), to, mg.queryProtocols)
		if err != nil {
			fmt.Println("failed to open stream", err)
			return
		}
		if _, err := io.Copy(w, buf); err != nil {
			fmt.Println("failed to forward buffer", err)
		}
		return
	}
	// Stop if we don't have a receiver set
	if mg.ofrr == nil {
		return
	}

	rec, err := utils.AddrBytesToAddrInfo([]byte(msg[is+2:]))
	if err != nil {
		fmt.Println("failed to parse addr bytes", err)
		return
	}

	var resp deal.QueryResponse
	if err := resp.UnmarshalCBOR(buf); err != nil && !errors.Is(err, io.EOF) {
		fmt.Println("failed to read query response", err)
		return
	}

	mg.ofrr.ReceiveOffer(*rec, resp)
}

// ID returns the host peer ID
func (mg *Messaging) ID() peer.ID {
	return mg.host.ID()
}

// AddAddrs adds a new peer into the host peerstore
func (mg *Messaging) AddAddrs(p peer.ID, addrs []ma.Multiaddr) {
	mg.host.Peerstore().AddAddrs(p, addrs, 8*time.Hour)
}

// Addrs returns the host's p2p addresses
func (mg *Messaging) Addrs() ([]ma.Multiaddr, error) {
	return peer.AddrInfoToP2pAddrs(host.InfoFromHost(mg.host))
}

// OfferReceiver returns the interface to receive incoming offer messages
func (mg *Messaging) OfferReceiver() OfferReceiver {
	return mg.ofrr
}

// SetOfferReceiver assigns a given receiver interface to receive messages
func (mg *Messaging) SetOfferReceiver(r OfferReceiver) {
	mg.ofrr = r
}

// SetRequestReceiver assigns a request receiver interface to receive messages on
func (mg *Messaging) SetRequestReceiver(r RequestReceiver) {
	mg.reqr = r
}

// ClearOfferReceiver clears the receiver preventing from receiving any messages
func (mg *Messaging) ClearOfferReceiver() {
	mg.ofrr = nil
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
	var msg string
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
			msg = string(sval)
		} else {
			cbg.ScanForLinks(br, func(cid.Cid) {})
		}
	}
	return msg, nil
}
