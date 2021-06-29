package exchange

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	cborutil "github.com/filecoin-project/go-cbor-util"
	"github.com/ipfs/go-cid"
	"github.com/ipld/go-ipld-prime"
	"github.com/jpillora/backoff"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/mux"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pb "github.com/libp2p/go-libp2p-pubsub/pb"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/myelnet/pop/retrieval/deal"
	"github.com/rs/zerolog/log"
	cbg "github.com/whyrusleeping/cbor-gen"
)

// We stay compatible with lotus nodes so we can retrieve from lotus providers too

// FilQueryProtocolID is the protocol for querying information about retrieval
// deal parameters from Filecoin storage miners
const FilQueryProtocolID = protocol.ID("/fil/retrieval/qry/1.0.0")

// PopQueryProtocolID is the protocol for exchanging information about retrieval
// deal parameters from retrieval providers
const PopQueryProtocolID = protocol.ID("/myel/pop/query/1.0")

const (
	// MaxStreamOpenAttempts is the number of times we try opening a stream with a given peer before giving up
	MaxStreamOpenAttempts = 5
	// MinAttemptDuration is the minimum amount of time we should wait before trying again
	MinAttemptDuration = 1 * time.Second
	// MaxAttemptDuration is maximum delay we should wait before trying again
	MaxAttemptDuration = 5 * time.Minute
)

// OpenStream is a generic method for opening streams with a backoff.
func OpenStream(ctx context.Context, h host.Host, p peer.ID, protos []protocol.ID) (network.Stream, error) {
	b := &backoff.Backoff{
		Min:    MinAttemptDuration,
		Max:    MaxAttemptDuration,
		Factor: MaxStreamOpenAttempts,
		Jitter: true,
	}

	for {
		s, err := h.NewStream(ctx, p, protos...)
		if err == nil {
			return s, err
		}
		log.Error().Err(err).Msg("trying again")

		nAttempts := b.Attempt()
		if nAttempts == MaxStreamOpenAttempts {
			return nil, fmt.Errorf("exhausted %d attempts but failed to open stream, err: %w", MaxStreamOpenAttempts, err)
		}
		d := b.Duration()
		time.Sleep(d)
	}

}

//QueryStream wraps convenience methods for writing and reading CBOR messages from a stream.
type QueryStream struct {
	p   peer.ID
	rw  mux.MuxedStream
	buf *bufio.Reader
}

// ReadQuery reads and decodes a CBOR encoded Query from a stream buffer.
func (qs *QueryStream) ReadQuery() (deal.Query, error) {
	var q deal.Query

	if err := q.UnmarshalCBOR(qs.buf); err != nil {
		return deal.Query{}, err

	}

	return q, nil
}

// WriteQuery encodes and writes a CBOR Query message to a stream.
func (qs *QueryStream) WriteQuery(q deal.Query) error {
	return cborutil.WriteCborRPC(qs.rw, &q)
}

// ReadQueryResponse reads and decodes a QueryResponse CBOR message from a stream buffer.
func (qs *QueryStream) ReadQueryResponse() (deal.QueryResponse, error) {
	var resp deal.QueryResponse

	if err := resp.UnmarshalCBOR(qs.buf); err != nil {
		return deal.QueryResponse{}, err
	}

	return resp, nil
}

// WriteQueryResponse encodes and writes a CBOR QueryResponse message to a stream.
func (qs *QueryStream) WriteQueryResponse(qr deal.QueryResponse) error {
	return cborutil.WriteCborRPC(qs.rw, &qr)
}

// ReadOffer reads and decodes a CBOR encoded offer message.
func (qs *QueryStream) ReadOffer() (deal.Offer, error) {
	var offer deal.Offer

	if err := offer.UnmarshalCBOR(qs.buf); err != nil {
		return deal.Offer{}, err
	}

	return offer, nil
}

// WriteOffer encodes and writes an Offer message to byte stream.
func (qs *QueryStream) WriteOffer(o deal.Offer) error {
	return cborutil.WriteCborRPC(qs.rw, &o)
}

// Close the underlying stream
func (qs *QueryStream) Close() error {
	return qs.rw.Close()
}

// OtherPeer returns the peer ID of the other peer at the end of the stream
func (qs *QueryStream) OtherPeer() peer.ID {
	return qs.p
}

// MessageTracker returns metadata about messages so we know if they're destined to this host
// or should be forwarded
type MessageTracker interface {
	// Published checks if we are actually the peer expecting this offer
	Published(string) bool
	// Sender returns the peer we think this message should be forwarded to
	Sender(string) (peer.ID, error)
}

// ReceiveResponse is fired every time we get a response
type ReceiveResponse func(peer.AddrInfo, deal.QueryResponse)

// ReceiveOffer is a callback for receiving a new offer
type ReceiveOffer func(deal.Offer)

// ResponseFunc takes a Query and returns an Offer or an error if request is declined
type ResponseFunc func(context.Context, peer.ID, Region, deal.Query) (deal.Offer, error)

// GossipRouting is a content routing service to find content providers using pubsub gossip routing
type GossipRouting struct {
	h              host.Host
	ps             *pubsub.PubSub
	tops           []*pubsub.Topic
	queryProtocols []protocol.ID
	meta           MessageTracker
	regions        []Region
	rmu            sync.Mutex
	receiveOffer   ReceiveOffer
}

// NewGossipRouting creates a new GossipRouting service
func NewGossipRouting(h host.Host, ps *pubsub.PubSub, meta MessageTracker, rgs []Region) *GossipRouting {
	routing := &GossipRouting{
		h:       h,
		ps:      ps,
		meta:    meta,
		regions: rgs,
		tops:    make([]*pubsub.Topic, len(rgs)),
		queryProtocols: []protocol.ID{
			PopQueryProtocolID,
		},
	}
	return routing
}

// StartProviding opens up our gossip subscription and sets our stream handler
func (gr *GossipRouting) StartProviding(ctx context.Context, fn ResponseFunc) error {
	// The PopQueryProtocolID handler expects offer messages from peers who received a gossip query
	gr.h.SetStreamHandler(PopQueryProtocolID, gr.handleOffer)

	// The FilQueryProtocolID handler expects query messages
	gr.h.SetStreamHandler(FilQueryProtocolID, func(s network.Stream) {
		buffered := bufio.NewReaderSize(s, 16)
		defer s.Close()

		receivedFrom := s.Conn().RemotePeer()

		m := new(deal.Query)
		if err := m.UnmarshalCBOR(buffered); err != nil {
			return
		}
		// supports single region only
		offer, err := fn(ctx, receivedFrom, gr.regions[0], *m)
		if err != nil {
			return
		}

		qs := &QueryStream{p: receivedFrom, rw: s, buf: buffered}

		err = qs.WriteQueryResponse(offer.AsQueryResponse())
		if err != nil {
			log.Error().Err(err).Msg("writing query response")
		}
	})

	for i, r := range gr.regions {
		top, err := gr.ps.Join(fmt.Sprintf("%s/%s", PopQueryProtocolID, r.Name))
		if err != nil {
			return err
		}
		gr.tops[i] = top
		sub, err := top.Subscribe()
		if err != nil {
			return err
		}
		go gr.pump(ctx, sub, fn)
	}

	return nil
}

func (gr *GossipRouting) pump(ctx context.Context, sub *pubsub.Subscription, fn ResponseFunc) {
	r := RegionFromTopic(sub.Topic())
	for {
		msg, err := sub.Next(ctx)
		if err != nil {
			return
		}
		if msg.ReceivedFrom == gr.h.ID() {
			continue
		}
		m := new(deal.Query)
		if err := m.UnmarshalCBOR(bytes.NewReader(msg.Data)); err != nil {
			continue
		}
		offer, err := fn(ctx, msg.ReceivedFrom, r, *m)
		if err != nil {
			continue
		}

		qs, err := gr.NewQueryStream(msg.ReceivedFrom, gr.queryProtocols)
		if err != nil {
			log.Error().Err(err).Msg("failed to create response query stream")
			continue
		}
		offer.ID = pubsub.DefaultMsgIdFn(msg.Message)
		addrs, err := gr.Addrs()
		if err != nil || len(addrs) == 0 {
			log.Error().Err(err).Msg("failed to get host addresses")
			continue
		}
		offer.PeerAddr = addrs[0].Bytes()

		if err := qs.WriteOffer(offer); err != nil {
			log.Error().Err(err).Msg("retrieval query: WriteCborRPC")
			continue
		}

	}
}

// QueryProvider asks a provider directly for retrieval conditions
func (gr *GossipRouting) QueryProvider(p peer.AddrInfo, root cid.Cid, sel ipld.Node, fn ReceiveOffer) error {
	params, err := deal.NewQueryParams(sel)
	if err != nil {
		return err
	}
	m := deal.Query{
		PayloadCID:  root,
		QueryParams: params,
	}

	stream, err := gr.NewQueryStream(p.ID, []protocol.ID{FilQueryProtocolID})
	defer stream.Close()

	err = stream.WriteQuery(m)
	if err != nil {
		return err
	}

	res, err := stream.ReadQueryResponse()
	if err != nil {
		return err
	}
	addrs, err := peer.AddrInfoToP2pAddrs(&p)
	if err != nil {
		return err
	}
	fn(deal.Offer{
		PeerAddr:                   addrs[0].Bytes(),
		PayloadCID:                 root,
		Size:                       res.Size,
		PaymentAddress:             res.PaymentAddress,
		MinPricePerByte:            res.MinPricePerByte,
		MaxPaymentInterval:         res.MaxPaymentInterval,
		MaxPaymentIntervalIncrease: res.MaxPaymentIntervalIncrease,
		UnsealPrice:                res.UnsealPrice,
	})
	return nil
}

// Query asks the gossip network of providers if anyone can provide the blocks we're looking for
// it blocks execution until our conditions are satisfied
func (gr *GossipRouting) Query(ctx context.Context, root cid.Cid, sel ipld.Node) error {
	params, err := deal.NewQueryParams(sel)
	if err != nil {
		return err
	}
	m := deal.Query{
		PayloadCID:  root,
		QueryParams: params,
	}

	buf := new(bytes.Buffer)
	if err := m.MarshalCBOR(buf); err != nil {
		return err
	}

	bytes := buf.Bytes()
	// publish to all regions this exchange joined
	for _, topic := range gr.tops {
		if err := topic.Publish(ctx, bytes); err != nil {
			return err
		}
	}

	return nil
}

// SetReceiver sets a callback to receive offers from gossip routers
func (gr *GossipRouting) SetReceiver(fn ReceiveOffer) {
	gr.rmu.Lock()
	gr.receiveOffer = fn
	gr.rmu.Unlock()
}

// NewQueryStream creates a new query stream using the provided peer.ID to handle the Query protocols
func (gr *GossipRouting) NewQueryStream(dest peer.ID, protos []protocol.ID) (*QueryStream, error) {
	s, err := OpenStream(context.Background(), gr.h, dest, protos)
	if err != nil {
		return nil, err
	}
	buffered := bufio.NewReaderSize(s, 16)
	return &QueryStream{p: dest, rw: s, buf: buffered}, nil
}

// handleOffer reads an Offer message from an incoming stream, and checks if it matches
// any query we published. If we did publish it means we are expecting responses so we read the offer
// and send it to the receiver if not and we have a sender for the message reference we forward it back.
func (gr *GossipRouting) handleOffer(s network.Stream) {
	buffered := bufio.NewReaderSize(s, 16)
	defer s.Close()

	buf := new(bytes.Buffer)
	id, err := PeekOfferID(buffered, buf)
	if err != nil {
		log.Error().Err(err).Msg("failed to peek offer ID")
		return
	}
	// The receiver should know if we issued the query if it's not the case
	// it means we must forward it to whichever peer sent us the query
	if !gr.meta.Published(id) {
		to, err := gr.meta.Sender(id)
		if err != nil {
			log.Error().Err(err).Msg("failed to find message recipient")
			return
		}
		w, err := OpenStream(context.Background(), gr.h, to, gr.queryProtocols)
		if err != nil {
			log.Error().Err(err).Msg("failed to open stream")
			return
		}
		if _, err := io.Copy(w, buf); err != nil {
			log.Error().Err(err).Msg("failed to forward buffer")
		}
		return
	}
	gr.rmu.Lock()
	defer gr.rmu.Unlock()
	// Stop if we don't have a receiver set
	if gr.receiveOffer == nil {
		return
	}

	var offer deal.Offer
	if err := offer.UnmarshalCBOR(buf); err != nil && !errors.Is(err, io.EOF) {
		log.Error().Err(err).Msg("failed to read offer")
		return
	}

	gr.receiveOffer(offer)
}

// Addrs returns the host's p2p addresses
func (gr *GossipRouting) Addrs() ([]ma.Multiaddr, error) {
	return peer.AddrInfoToP2pAddrs(host.InfoFromHost(gr.h))
}

// AddAddrs adds a new peer into the host peerstore
func (gr *GossipRouting) AddAddrs(p peer.ID, addrs []ma.Multiaddr) {
	gr.h.Peerstore().AddAddrs(p, addrs, 8*time.Hour)
}

// PeekOfferID decodes the ID field only and returns the value while copying the bytes in a buffer
func PeekOfferID(r io.Reader, buf *bytes.Buffer) (string, error) {
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
		if name == "ID" {
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

// GossipTracer tracks messages we've seen so we can relay responses back to the publisher
type GossipTracer struct {
	pmu       sync.Mutex
	published map[string]bool
	smu       sync.Mutex
	senders   map[string]peer.ID
}

// NewGossipTracer creates a new instance of GossipTracer
func NewGossipTracer() *GossipTracer {
	return &GossipTracer{
		published: make(map[string]bool),
		senders:   make(map[string]peer.ID),
	}
}

// Trace gets triggered for every internal gossip sub operation
func (gt *GossipTracer) Trace(evt *pb.TraceEvent) {
	if evt.PublishMessage != nil {
		gt.pmu.Lock()
		gt.published[string(evt.PublishMessage.MessageID)] = true
		gt.pmu.Unlock()
	}
	if evt.DeliverMessage != nil {
		msg := evt.DeliverMessage
		gt.smu.Lock()
		gt.senders[string(msg.MessageID)] = peer.ID(msg.ReceivedFrom)
		gt.smu.Unlock()
	}
}

// Published checks if we were the publisher of a message
func (gt *GossipTracer) Published(mid string) bool {
	gt.pmu.Lock()
	defer gt.pmu.Unlock()
	return gt.published[mid]
}

// Sender returns the peer who sent us a message
func (gt *GossipTracer) Sender(mid string) (peer.ID, error) {
	gt.smu.Lock()
	defer gt.smu.Unlock()
	p, ok := gt.senders[mid]
	if !ok {
		return "", errors.New("no sender found")
	}
	return p, nil
}
