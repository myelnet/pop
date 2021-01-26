package supply

import (
	"context"
	"fmt"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p-core/host"
	peer "github.com/libp2p/go-libp2p-core/peer"
)

// ErrNoPeers when no peers are available to get or send supply to
var ErrNoPeers = fmt.Errorf("no peers available for supply")

// Manager exposes methods to manage the blocks we can serve as a provider
type Manager interface {
	SendAddRequest(context.Context, cid.Cid, uint64) error
}

// Supply keeps track of the content we store and provide on the network
// its role is to always seek and supply new and more efficient content to store
type Supply struct {
	h   host.Host
	dt  datatransfer.Manager
	net *Network
	man *Manifest

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
	m := &Supply{
		h:             h,
		dt:            dt,
		net:           net,
		man:           manifest,
		providerPeers: make(map[cid.Cid]*peer.Set),
	}
	return m
}

// SendAddRequest to the network until we have propagated the content to enough peers
func (s *Supply) SendAddRequest(ctx context.Context, payload cid.Cid, size uint64) error {
	// Get the current connected peers
	var peers []peer.ID
	for _, pid := range s.h.Peerstore().Peers() {
		if pid != s.h.ID() {
			peers = append(peers, pid)
		}
	}

	if len(peers) == 0 {
		return nil // ErrNoPeers is quite noisy so will disable until we find a more elegant way
	}
	// Set the amount of peers we want to notify
	max := 6
	// If we have less peers we adjust accordingly
	if len(peers) < 6 {
		max = len(peers)
	}

	// wait for all the peers who pull the data from us
	c := make(chan peer.ID, max)
	// listen for datatransfer events to identify the peers who pulled the content
	unsubscribe := s.dt.SubscribeToEvents(func(event datatransfer.Event, channelState datatransfer.ChannelState) {
		if channelState.Status() == datatransfer.Completed {
			c <- channelState.OtherPeer()
		}
	})
	// Clean up when we're done
	defer unsubscribe()

	for i := 0; i < max; i++ {
		stream, err := s.net.NewAddRequestStream(peers[i])
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
	s.providerPeers[payload] = peer.NewSet()
	// For for a defined amount of successful transfers
	for i := 0; i < cap(c); i++ {
		// TODO: add a timeout but not sure how long we can tolerate waiting for peers to pull
		p := <-c
		s.providerPeers[payload].Add(p)
	}

	return nil

}
