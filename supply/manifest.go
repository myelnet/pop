package supply

import (
	"context"
	"fmt"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-graphsync"
	"github.com/libp2p/go-libp2p-core/host"
	peer "github.com/libp2p/go-libp2p-core/peer"
)

// Manifest keeps track of our block supply lifecycle including
// how long we've been storing a block, how many times it has been requested
// and how many times it has been retrieved
type Manifest struct {
	// Host for accessing connected peers
	h host.Host
	// Some transfers may require payment in which case we will need the data transfer manager
	dt datatransfer.Manager
	// Some transfers we can retrieve directly from the client so we can use graphsync directly
	gs graphsync.GraphExchange
}

// NewManifest creates a new Manifest instance
func NewManifest(h host.Host, dt datatransfer.Manager, gs graphsync.GraphExchange) *Manifest {
	return &Manifest{h, dt, gs}
}

// HandleAddRequest and decide whether to add the block to our supply or not
func (m *Manifest) HandleAddRequest(stream *addRequestStream) {
	defer stream.Close()

	notif, err := stream.ReadAddRequest()
	if err != nil {
		fmt.Println("Unable to read notification")
		return
	}
	// TODO: run custom logic to validate the presence of a storage deal for this block
	// we may need to request deal info in the message
	// + check if we have room to store it
	if err := m.AddBlock(context.Background(), notif.PayloadCID, stream.p); err != nil {
		fmt.Println("Unable to add new block to our supply")
		return
	}
}

// AddBlock to the manifest
func (m *Manifest) AddBlock(ctx context.Context, root cid.Cid, from peer.ID) error {
	return nil
}
