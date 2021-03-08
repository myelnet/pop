package supply

import (
	"context"
	"encoding/json"
	"fmt"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-multistore"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
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
	// Datastore for storing the manifest
	ds datastore.Batching
	// MultiStore for creating independent stores for each new content
	ms *multistore.MultiStore
}

const (
	// KStoreID is the local store ID where that content is stored
	KStoreID = "store"
	// KSize is the full content size
	KSize = "size"
)

// ContentRecord is a map of labels associated with a content ID
// lind of like a mini database for that content activity
type ContentRecord struct {
	Labels map[string]string
}

// NewManifest creates a new Manifest instance
func NewManifest(
	h host.Host,
	dt datatransfer.Manager,
	ds datastore.Batching,
	ms *multistore.MultiStore,
) *Manifest {
	return &Manifest{
		h:  h,
		dt: dt,
		ds: namespace.Wrap(ds, datastore.NewKey("/supply")),
		ms: ms,
	}
}

// PutRecord creates a new record for a given content ID
func (m *Manifest) PutRecord(id cid.Cid, r *ContentRecord) error {
	rec, err := json.Marshal(r)
	if err != nil {
		return err
	}

	return m.ds.Put(datastore.NewKey(id.String()), rec)
}

// GetRecord returns a record for a given content ID
func (m *Manifest) GetRecord(id cid.Cid) (*ContentRecord, error) {
	r, err := m.ds.Get(datastore.NewKey(id.String()))
	if err != nil {
		return nil, err
	}

	var rec ContentRecord
	if err := json.Unmarshal(r, &rec); err != nil {
		return nil, err
	}

	return &rec, nil
}

// AddLabel adds a label to a ContentRecord
func (m *Manifest) AddLabel(id cid.Cid, key, value string) error {
	dsk := datastore.NewKey(id.String())

	r, err := m.ds.Get(dsk)
	if err != nil {
		return err
	}

	var rec ContentRecord
	if err := json.Unmarshal(r, &rec); err != nil {
		return err
	}

	rec.Labels[key] = value

	r, err = json.Marshal(&rec)
	if err != nil {
		return err
	}

	return m.ds.Put(dsk, r)
}

// RemoveRecord removes a record entirely from our manifest
func (m *Manifest) RemoveRecord(id cid.Cid) error {
	if err := m.ds.Delete(datastore.NewKey(id.String())); err != nil {
		return err
	}
	return nil
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

	// Create a new store to receive our new blocks
	// It will be automatically picked up in the TransportConfigurer
	storeID := m.ms.Next()
	err = m.PutRecord(req.PayloadCID, &ContentRecord{Labels: map[string]string{
		KStoreID: fmt.Sprintf("%d", storeID),
		KSize:    fmt.Sprintf("%d", req.Size),
	}})
	if err != nil {
		return
	}
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
