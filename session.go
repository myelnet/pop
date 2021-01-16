package hop

import (
	"bytes"
	"context"
	"fmt"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	blocks "github.com/ipfs/go-block-format"
	cid "github.com/ipfs/go-cid"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	iprime "github.com/ipld/go-ipld-prime"
	basicnode "github.com/ipld/go-ipld-prime/node/basic"
	"github.com/ipld/go-ipld-prime/traversal/selector"
	"github.com/ipld/go-ipld-prime/traversal/selector/builder"
	peer "github.com/libp2p/go-libp2p-core/peer"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
)

// Session to exchange multiple blocks with a set of connected peers
type Session struct {
	blockstore   blockstore.Blockstore
	reqTopic     *pubsub.Topic
	net          RetrievalMarketNetwork
	dataTransfer datatransfer.Manager
	responses    map[peer.ID]QueryResponse // List of all the responses peers sent us back for a gossip query
	res          chan peer.ID              // First response we get
	started      bool                      // If we found a satisfying response and we started retrieving
}

// HandleQueryStream for direct provider queries
func (s *Session) HandleQueryStream(stream RetrievalQueryStream) {

	defer stream.Close()

	response, err := stream.ReadQueryResponse()
	if err != nil {
		fmt.Println("Unable to read query response", err)
		return
	}

	s.responses[stream.OtherPeer()] = response
	if !s.started {
		s.res <- stream.OtherPeer()
	}
}

// GetBlock from the first provider
func (s *Session) GetBlock(ctx context.Context, k cid.Cid) (blocks.Block, error) {
	return s.blockstore.Get(k)
}

// GetBlocks from one or multiple providers
func (s *Session) GetBlocks(ctx context.Context, ks []cid.Cid) (<-chan blocks.Block, error) {

	out := make(chan blocks.Block)
	m := Query{
		PayloadCID:  ks[0],
		QueryParams: QueryParams{},
	}

	buf := new(bytes.Buffer)
	if err := m.MarshalCBOR(buf); err != nil {
		return out, err
	}

	if err := s.reqTopic.Publish(ctx, buf.Bytes()); err != nil {
		return out, err
	}

	go s.responseLoop(ctx, m.PayloadCID, out)

	return out, nil
}

func (s *Session) responseLoop(ctx context.Context, root cid.Cid, out chan blocks.Block) {
	fmt.Println("Waiting for response")

	for {
		select {
		case pid := <-s.res:
			response := s.responses[pid]
			// We can decide here some extra logic to reject the response
			// For now we always accept the first one
			fmt.Println("Received response", response.Size)
			err := s.Retrieve(ctx, root, pid)
			if err != nil {
				// Maybe retry?
				fmt.Println("Failed to retrieve, err")
				continue
			}
			// This is kinda dumb
			block, err := s.blockstore.Get(root)
			if err != nil {
				fmt.Println("Failed to get synced block from store", err)
				continue
			}

			out <- block
			return
		}
	}
}

// AllSelector to get all the nodes for now. TODO` support custom selectors
func AllSelector() iprime.Node {
	ssb := builder.NewSelectorSpecBuilder(basicnode.Prototype.Any)
	return ssb.ExploreRecursive(selector.RecursionLimitNone(),
		ssb.ExploreAll(ssb.ExploreRecursiveEdge())).Node()
}

// Retrieve an entire dag from the root. TODO: add FIL payment channel
func (s *Session) Retrieve(ctx context.Context, root cid.Cid, sender peer.ID) error {

	done := make(chan datatransfer.Event, 1)
	unsubscribe := s.dataTransfer.SubscribeToEvents(func(event datatransfer.Event, channelState datatransfer.ChannelState) {
		if channelState.Status() == datatransfer.Completed {
			done <- event

		}
		if event.Code == datatransfer.Error {
			done <- event
		}
	})
	defer unsubscribe()

	voucher := StorageDataTransferVoucher{Proposal: root}
	_, err := s.dataTransfer.OpenPullDataChannel(ctx, sender, &voucher, root, AllSelector())
	if err != nil {
		return err
	}

	select {
	case event := <-done:
		if event.Code == datatransfer.Error {
			return fmt.Errorf("Retrieval error: %v", event.Message)
		}
		return nil
	}
}
