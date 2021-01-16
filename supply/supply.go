package supply

import (
	"context"
	"fmt"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p-core/host"
	peer "github.com/libp2p/go-libp2p-core/peer"
)

// Supply keeps track of the content we store and provide on the network
// its role is to always seek and supply new and more efficient content to store
type Supply struct {
	h   host.Host
	dt  datatransfer.Manager
	net *Network
	man *Manifest
}

// NewSupply creates a new instance of the SupplyManager
func NewSupply(
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
		h:   h,
		dt:  dt,
		net: net,
		man: manifest,
	}
	return m
}

// SendAddRequest to the network until we have a propagated the content to a satisfying amount of peers
func (s *Supply) SendAddRequest(ctx context.Context, payload cid.Cid, size uint64) error {
	// Get the current connected peers
	peers := s.h.Peerstore().Peers()
	// Set the amount of peers we want to notify
	max := 6
	// If we have less peers we adjust accordingly
	if len(peers) < 6 {
		max = len(peers)
	}
	// wait for all the peers who pull the data from us
	c := make(chan peer.ID, max)
	// save all the peers we messaged
	messaged := make(map[peer.ID]bool)
	// listen for datatransfer events to identify the peers who pulled the content
	unsubscribe := s.dt.SubscribeToEvents(func(event datatransfer.Event, channelState datatransfer.ChannelState) {
		if channelState.Status() == datatransfer.Completed && messaged[channelState.OtherPeer()] {
			c <- channelState.OtherPeer()
		}
	})
	// Clean up when we're done
	defer unsubscribe()

	for i := 0; i < max; i++ {
		stream, err := s.net.NewAddRequestStream(peers[i])
		if err != nil {
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
		messaged[peers[i]] = true
	}
	// For for a defined amount of successful transfers
	var received []peer.ID
	for i := 0; i < max; i++ {
		p := <-c
		received = append(received, p)
	}
	// TODO: save the peers somewhere?

	return nil

}
