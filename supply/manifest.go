package supply

import (
	"context"
	"fmt"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/ipld/go-ipld-prime"
	basicnode "github.com/ipld/go-ipld-prime/node/basic"
	"github.com/ipld/go-ipld-prime/traversal/selector"
	"github.com/ipld/go-ipld-prime/traversal/selector/builder"
	"github.com/libp2p/go-libp2p-core/host"
	peer "github.com/libp2p/go-libp2p-core/peer"
)

// Manifest keeps track of our block supply lifecycle including
// how long we've been storing a block, how many times it has been requested
// and how many times it has been retrieved
type Manifest struct {
	// Host for accessing connected peers
	h host.Host
	// Some transfers may require payment in which case we will need a new instance of the data transfer manager
	dt datatransfer.Manager
}

// NewManifest creates a new Manifest instance
func NewManifest(h host.Host, dt datatransfer.Manager) *Manifest {
	return &Manifest{h, dt}
}

// HandleRequest and decide whether to add the block to our supply or not
func (m *Manifest) HandleRequest(stream RequestStreamer) {
	defer stream.Close()

	req, err := stream.ReadRequest()
	if err != nil {
		fmt.Println("Unable to read notification")
		return
	}
	// TODO: run custom logic to validate the presence of a storage deal for this block
	// we may need to request deal info in the message
	// + check if we have room to store it
	if err := m.SyncBlocks(context.Background(), req, stream.OtherPeer()); err != nil {
		fmt.Println("Unable to add new block to our supply", err)
		return
	}
}

// AllSelector selects all the nodes it can find
// TODO: prob should make this reusable
func AllSelector() ipld.Node {
	ssb := builder.NewSelectorSpecBuilder(basicnode.Prototype.Any)
	return ssb.ExploreRecursive(selector.RecursionLimitNone(),
		ssb.ExploreAll(ssb.ExploreRecursiveEdge())).Node()
}

// SyncBlocks to the manifest
func (m *Manifest) SyncBlocks(ctx context.Context, req Request, from peer.ID) error {

	done := make(chan datatransfer.Event, 1)
	unsubscribe := m.dt.SubscribeToEvents(func(event datatransfer.Event, channelState datatransfer.ChannelState) {
		if channelState.Status() == datatransfer.Completed || event.Code == datatransfer.Error {
			done <- event
		}
	})
	defer unsubscribe()

	_, err := m.dt.OpenPullDataChannel(ctx, from, &req, req.PayloadCID, AllSelector())
	if err != nil {
		return err
	}

	select {
	case event := <-done:
		if event.Code == datatransfer.Error {
			return fmt.Errorf("failed to retrieve: %v", event.Message)
		}
		return nil
	}
}
