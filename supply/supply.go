package supply

import (
	"context"
	"fmt"
	"sync"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/hannahhoward/go-pubsub"
	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p-core/host"
	peer "github.com/libp2p/go-libp2p-core/peer"
)

// ErrNoPeers when no peers are available to get or send supply to
var ErrNoPeers = fmt.Errorf("no peers available for supply")

// Manager exposes methods to manage the blocks we can serve as a provider
type Manager interface {
	SendAddRequest(cid.Cid, uint64) error
	SubscribeToEvents(Subscriber) Unsubscribe
	ProviderPeersForContent(cid.Cid) ([]peer.ID, error)
}

// Supply keeps track of the content we store and provide on the network
// its role is to always seek and supply new and more efficient content to store
type Supply struct {
	h   host.Host
	dt  datatransfer.Manager
	net *Network
	man *Manifest
	ctx context.Context
	// New content subscriber to know when we've sent the content to new providers
	subscribers *pubsub.PubSub

	mu sync.Mutex // Protects the following fields
	// Keep track of which of our peers may have a block
	// Not use for anything else than debugging currently but may be useful eventualy
	providerPeers map[cid.Cid]*peer.Set
}

// New instance of the SupplyManager
func New(
	ctx context.Context,
	h host.Host,
	dt datatransfer.Manager,
) *Supply {
	manifest := NewManifest(h, dt)
	// Connect to incoming supply messages form peers
	net := NewNetwork(h)
	// Set the manifest to handle our messages
	net.SetDelegate(manifest)
	// We wrap it all in our Supply object
	s := &Supply{
		h:             h,
		dt:            dt,
		net:           net,
		man:           manifest,
		ctx:           ctx,
		providerPeers: make(map[cid.Cid]*peer.Set),
		subscribers:   pubsub.New(EventDispatcher),
	}

	// listen for datatransfer events to identify the peers who pulled the content
	s.dt.SubscribeToEvents(s.notifyProvidersReceived)

	return s
}

func (s *Supply) notifyProvidersReceived(event datatransfer.Event, chState datatransfer.ChannelState) {
	if chState.Status() == datatransfer.Completed {
		s.mu.Lock()
		defer s.mu.Unlock()

		root := chState.BaseCID()

		// We should already have a peer set for that cid
		// if not this data transfer may be unrelated
		set, ok := s.providerPeers[root]
		if !ok {
			return
		}
		// The recipient is the provider who received our content
		rec := chState.Recipient()
		if rec != s.h.ID() {
			set.Add(rec)
		}
		// Notify subscribers
		// TODO: publish error event
		s.subscribers.Publish(Event{
			PayloadCID: root,
			Provider:   rec,
		})
	}
}

// ProviderPeersForContent gets the known providers for a given content id
func (s *Supply) ProviderPeersForContent(c cid.Cid) ([]peer.ID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pset, ok := s.providerPeers[c]
	if !ok {
		return nil, fmt.Errorf("content not tracked")
	}
	return pset.Peers(), nil
}

// SendAddRequest to the network until we have propagated the content to enough peers
func (s *Supply) SendAddRequest(payload cid.Cid, size uint64) error {
	s.mu.Lock()
	s.providerPeers[payload] = peer.NewSet()
	s.mu.Unlock()
	// Select the providers we want to send to
	providers, err := s.selectProviders()
	if err != nil {
		return err
	}
	s.processAddRequests(payload, size, providers)
	return nil
}

func (s *Supply) selectProviders() ([]peer.ID, error) {
	var peers []peer.ID
	// Get the current connected peers
	for _, pconn := range s.h.Network().Conns() {
		pid := pconn.RemotePeer()
		// Make sure we don't add ourselves
		if pid != s.h.ID() {
			// Make sure our peer supports the retrieval dispatch protocol
			supported, err := s.h.Peerstore().SupportsProtocols(
				pid,
				string(AddRequestProtocolID),
			)
			if err != nil || len(supported) == 0 {
				continue
			}
			peers = append(peers, pid)
		}
	}

	if len(peers) == 0 {
		return nil, ErrNoPeers
	}
	// TODO: Allow configurating the amount of peers we want to notify
	max := 6
	// If we have less peers we adjust accordingly
	if len(peers) > max {
		peers = peers[:max]
	}
	return peers, nil
}

func (s *Supply) processAddRequests(payload cid.Cid, size uint64, peers []peer.ID) {
	for _, p := range peers {
		stream, err := s.net.NewAddRequestStream(p)
		if err != nil {
			fmt.Println("Unable to create new request stream", err)
			continue
		}
		m := AddRequest{
			PayloadCID: payload,
			Size:       size,
		}
		err = stream.WriteAddRequest(m)
		if err != nil {
			fmt.Println("Unable to send addRequest:", err)
			continue
		}
	}
}

// SubscribeToEvents to listen for supply events
func (s *Supply) SubscribeToEvents(subscriber Subscriber) Unsubscribe {
	return Unsubscribe(s.subscribers.Subscribe(subscriber))
}

// Subscriber is a callback to listen for supply events
type Subscriber func(event Event)

// Unsubscribe cancels a subscription
type Unsubscribe func()

// Event determines when we propagated content to a new provider
// TODO: different event types etc
type Event struct {
	PayloadCID cid.Cid
	Provider   peer.ID
}

// EventDispatcher converts our pubsub signature to our callback signature
func EventDispatcher(evt pubsub.Event, subscriberFn pubsub.SubscriberFn) error {
	ie, ok := evt.(Event)
	if !ok {
		return fmt.Errorf("wrong type of event")
	}
	cb, ok := subscriberFn.(Subscriber)
	if !ok {
		return fmt.Errorf("wrong subscriber")
	}
	cb(ie)
	return nil
}
