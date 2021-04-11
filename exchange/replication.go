package exchange

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/ipfs/go-cid"
	"github.com/ipld/go-ipld-prime"
	basicnode "github.com/ipld/go-ipld-prime/node/basic"
	"github.com/ipld/go-ipld-prime/traversal/selector"
	"github.com/ipld/go-ipld-prime/traversal/selector/builder"
	"github.com/jpillora/backoff"
	"github.com/libp2p/go-eventbus"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	pb "github.com/libp2p/go-libp2p-pubsub/pb"
)

// Replication manages the network replication scheme, it keeps track of read and write requests
// and decides whether to join a replication scheme or not
type Replication struct {
	h     host.Host
	dt    datatransfer.Manager
	pm    *PeerMgr
	hs    *HeyService
	store *MetadataStore
	mg    *Messaging
	rgs   []Region

	mu      sync.Mutex
	schemes map[peer.ID]struct{}

	pmu   sync.Mutex
	pulls map[cid.Cid]*peer.Set
}

// NewReplication starts the exchange replication management system
func NewReplication(h host.Host, metads *MetadataStore, dt datatransfer.Manager, mg *Messaging, rgs []Region) *Replication {
	pm := NewPeerMgr(h, rgs)
	hs := NewHeyService(h, pm)
	r := &Replication{
		h:       h,
		pm:      pm,
		hs:      hs,
		dt:      dt,
		mg:      mg,
		rgs:     rgs,
		schemes: make(map[peer.ID]struct{}),
		pulls:   make(map[cid.Cid]*peer.Set),
		store:   metads,
	}
	r.mg.SetRequestReceiver(r)
	r.dt.RegisterVoucherType(&Request{}, r)
	r.dt.RegisterTransportConfigurer(&Request{}, TransportConfigurer(r.store))

	// TODO: clean this up
	r.dt.SubscribeToEvents(func(event datatransfer.Event, channelState datatransfer.ChannelState) {
		if event.Code == datatransfer.Error && channelState.Recipient() == h.ID() {
			// If transfers fail and we're the recipient we need to remove it from our index
			r.store.RemoveRecord(channelState.BaseCID())
		}
	})

	return r
}

// Start initiates listeners to update our scheme if new peers join
func (r *Replication) Start(ctx context.Context) error {
	sub, err := r.h.EventBus().Subscribe(new(PeerRegionEvt), eventbus.BufSize(16))
	if err != nil {
		return err
	}
	if err := r.hs.Run(ctx); err != nil {
		return err
	}
	go func() {
		for evt := range sub.Out() {
			pevt := evt.(PeerRegionEvt)
			switch pevt.Type {
			case AddPeerEvt:
				r.JoinScheme(pevt.ID)
			case RemovePeerEvt:
				r.LeaveScheme(pevt.ID)
			}
		}
	}()
	return nil
}

// PRecord is a provider <> cid mapping for recording who is storing what content
type PRecord struct {
	Provider   peer.ID
	PayloadCID cid.Cid
}

// DispatchOptions exposes parameters to affect the duration of a Dispatch operation
type DispatchOptions struct {
	BackoffMin     time.Duration
	BackoffAttemps int
	RF             int
}

// DefaultDispatchOptions provides useful defaults
// We can change these if the content requires a long transfer time
var DefaultDispatchOptions = DispatchOptions{
	BackoffMin:     2 * time.Second,
	BackoffAttemps: 4,
	RF:             7,
}

// DispatchRequest to the network until we have propagated the content to enough peers
func (r *Replication) DispatchRequest(req Request, opt DispatchOptions) chan PRecord {
	resChan := make(chan PRecord, opt.RF)
	out := make(chan PRecord, opt.RF)
	// listen for datatransfer events to identify the peers who pulled the content
	unsub := r.dt.SubscribeToEvents(func(event datatransfer.Event, chState datatransfer.ChannelState) {
		if chState.Status() == datatransfer.Completed {
			root := chState.BaseCID()
			if root != req.PayloadCID {
				return
			}
			// The recipient is the provider who received our content
			rec := chState.Recipient()
			resChan <- PRecord{
				Provider:   rec,
				PayloadCID: root,
			}
		}
	})
	go func() {
		defer func() {
			unsub()
			close(out)
		}()
		// The peers we already sent requests to
		rcv := make(map[peer.ID]bool)
		// Set the parameters for backing off after each try
		b := backoff.Backoff{
			Min: opt.BackoffMin,
			Max: 60 * time.Minute,
			// Factor: 2 (default)
		}
		// The number of confirmations we received so far
		n := 0

	requests:
		for {
			// Give up after 6 attemps. Maybe should make this customizable for servers that can afford it
			if int(b.Attempt()) > opt.BackoffAttemps {
				return
			}
			// Select the providers we want to send to minus those we already confirmed
			// received the requests
			providers := r.pm.Peers(opt.RF-n, r.rgs, rcv)

			// Authorize the transfer
			for _, p := range providers {
				r.AuthorizePull(req.PayloadCID, p)
				rcv[p] = true
			}
			if len(providers) > 0 {
				// sendAllRequests
				r.sendAllRequests(req, providers)
			}

			timer := time.NewTimer(b.Duration())
			for {
				select {
				case <-timer.C:

					continue requests
				case r := <-resChan:
					// forward the confirmations to the Response channel
					out <- r
					// increment our results count
					n++
					if n == opt.RF {
						return
					}
				}
			}
		}
	}()
	return out
}

func (r *Replication) sendAllRequests(req Request, peers []peer.ID) {
	for _, p := range peers {
		stream, err := r.mg.NewRequestStream(p)
		if err != nil {
			continue
		}
		err = stream.WriteRequest(req)
		stream.Close()
		if err != nil {
			continue
		}
	}
}

// AllSelector is the default selector that reaches all the blocks
func AllSelector() ipld.Node {
	ssb := builder.NewSelectorSpecBuilder(basicnode.Prototype.Any)
	return ssb.ExploreRecursive(selector.RecursionLimitNone(),
		ssb.ExploreAll(ssb.ExploreRecursiveEdge())).Node()
}

// ReceiveRequest handles a write request from a peer
func (r *Replication) ReceiveRequest(p peer.ID, req Request) {
	// TODO: validate request
	// Create a new store to receive our new blocks
	// It will be automatically picked up in the TransportConfigurer
	storeID := r.store.ms.Next()
	err := r.store.PutRecord(req.PayloadCID, &ContentRecord{Labels: map[string]string{
		KStoreID: fmt.Sprintf("%d", storeID),
		KSize:    fmt.Sprintf("%d", req.Size),
	}})
	if err != nil {
		return
	}
	_, err = r.dt.OpenPullDataChannel(context.TODO(), p, &req, req.PayloadCID, AllSelector())
	if err != nil {
		return
	}

}

// JoinScheme adds a peer to our scheme set meaning we're in that peer's scheme
func (r *Replication) JoinScheme(p peer.ID) {
	r.mu.Lock()
	r.schemes[p] = struct{}{}
	r.mu.Unlock()
}

// LeaveScheme removes a peer from our scheme meaning we ...
func (r *Replication) LeaveScheme(p peer.ID) {
	r.mu.Lock()
	delete(r.schemes, p)
	r.mu.Unlock()
}

// AuthorizePull adds a peer to a set giving authorization to pull content without payment
// We assume that this authorizes the peer to pull as many links from the root CID as they can
// It runs on the client side to authorize caches
func (r *Replication) AuthorizePull(k cid.Cid, p peer.ID) {
	r.pmu.Lock()
	defer r.pmu.Unlock()
	if set, ok := r.pulls[k]; ok {
		set.Add(p)
		return
	}
	set := peer.NewSet()
	set.Add(p)
	r.pulls[k] = set
}

// ValidatePush returns a stubbed result for a push validation
func (r *Replication) ValidatePush(
	sender peer.ID,
	voucher datatransfer.Voucher,
	baseCid cid.Cid,
	selector ipld.Node) (datatransfer.VoucherResult, error) {
	return nil, fmt.Errorf("no pushed accepted")
}

// ValidatePull returns a stubbed result for a pull validation
func (r *Replication) ValidatePull(
	receiver peer.ID,
	voucher datatransfer.Voucher,
	baseCid cid.Cid,
	selector ipld.Node) (datatransfer.VoucherResult, error) {
	r.pmu.Lock()
	defer r.pmu.Unlock()
	set, ok := r.pulls[baseCid]
	if !ok {
		return nil, fmt.Errorf("unknown CID")
	}
	if !set.Contains(receiver) {
		return nil, fmt.Errorf("not authorized")
	}
	return nil, nil
}

// R exposes replication scheme methods
func (e *Exchange) R() *Replication {
	return e.rpl
}

// GossipTracer tracks messages we've seen so we can relay responses back to the publisher
type GossipTracer struct {
	published map[string]bool
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
		gt.published[string(evt.PublishMessage.MessageID)] = true
	}
	if evt.DeliverMessage != nil {
		msg := evt.DeliverMessage
		gt.senders[string(msg.MessageID)] = peer.ID(msg.ReceivedFrom)
	}
}

// Published checks if we were the publisher of a message
func (gt *GossipTracer) Published(mid string) bool {
	return gt.published[mid]
}

// Sender returns the peer who sent us a message
func (gt *GossipTracer) Sender(mid string) (peer.ID, error) {
	p, ok := gt.senders[mid]
	if !ok {
		return "", errors.New("no sender found")
	}
	return p, nil
}

// StoreConfigurableTransport defines the methods needed to
// configure a data transfer transport use a unique store for a given request
type StoreConfigurableTransport interface {
	UseStore(datatransfer.ChannelID, ipld.Loader, ipld.Storer) error
}

// TransportConfigurer configurers the graphsync transport to use a custom blockstore per content
func TransportConfigurer(s *MetadataStore) datatransfer.TransportConfigurer {
	return func(channelID datatransfer.ChannelID, voucher datatransfer.Voucher, transport datatransfer.Transport) {
		warn := func(err error) {
			fmt.Println("attempting to configure data store:", err)
		}
		request, ok := voucher.(*Request)
		if !ok {
			return
		}
		gsTransport, ok := transport.(StoreConfigurableTransport)
		if !ok {
			return
		}
		store, err := s.GetStore(request.PayloadCID)
		if err != nil {
			warn(err)
			return
		}
		err = gsTransport.UseStore(channelID, store.Loader, store.Storer)
		if err != nil {
			warn(err)
		}
	}
}
