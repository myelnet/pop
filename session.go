package hop

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/big"
	blocks "github.com/ipfs/go-block-format"
	cid "github.com/ipfs/go-cid"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	iprime "github.com/ipld/go-ipld-prime"
	basicnode "github.com/ipld/go-ipld-prime/node/basic"
	"github.com/ipld/go-ipld-prime/traversal/selector"
	"github.com/ipld/go-ipld-prime/traversal/selector/builder"
	peer "github.com/libp2p/go-libp2p-core/peer"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/myelnet/go-hop-exchange/retrieval"
	"github.com/myelnet/go-hop-exchange/retrieval/deal"
)

// Session to exchange multiple blocks with a set of connected peers
type Session struct {
	blockstore blockstore.Blockstore
	reqTopic   *pubsub.Topic
	net        RetrievalMarketNetwork
	retriever  *retrieval.Client
	addr       address.Address
	root       cid.Cid
	sel        iprime.Node
	ctx        context.Context
	done       chan error
	unsub      retrieval.Unsubscribe

	mu        sync.Mutex                // mutex to protext the following fields
	responses map[peer.ID]QueryResponse // List of all the responses peers sent us back for a gossip query
	res       chan peer.ID              // First response we get
	started   bool                      // If we found a satisfying response and we started retrieving
}

// HandleQueryStream for direct provider queries
func (s *Session) HandleQueryStream(stream RetrievalQueryStream) {

	defer stream.Close()

	response, err := stream.ReadQueryResponse()
	if err != nil {
		fmt.Println("Unable to read query response", err)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.responses[stream.OtherPeer()] = response

	if !s.started {
		s.Retrieve(s.ctx, stream.OtherPeer(), response.PaymentAddress)
	}
}

// GetBlock from the local blockstore. Not really used for anything but to comply with
// exchange session interface
func (s *Session) GetBlock(ctx context.Context, k cid.Cid) (blocks.Block, error) {
	return s.blockstore.Get(k)
}

// GetBlocks from one or multiple providers
// currently we only support a single root cid but may support multiple requests eventually
func (s *Session) GetBlocks(ctx context.Context, ks []cid.Cid) (<-chan blocks.Block, error) {
	out := make(chan blocks.Block)

	go func(k cid.Cid) {
		select {
		case err := <-s.Done():
			if err != nil {
				return
			}
			block, err := s.blockstore.Get(k)
			if err != nil {
				fmt.Println("Failed to get synced block from store", err)
				return
			}
			out <- block
		case <-ctx.Done():
			return
		}

	}(ks[0])

	return out, s.SyncBlocks(ctx)

}

// SyncBlocks will trigger a retrieval without returning the blocks
// It simply publishes a gossip message and only when providers reply will
// we trigger a retrieval
func (s *Session) SyncBlocks(ctx context.Context) error {
	m := Query{
		PayloadCID:  s.root,
		QueryParams: QueryParams{},
	}

	buf := new(bytes.Buffer)
	if err := m.MarshalCBOR(buf); err != nil {
		return err
	}

	if err := s.reqTopic.Publish(ctx, buf.Bytes()); err != nil {
		return err
	}
	return nil
}

// AllSelector to get all the nodes for now. TODO` support custom selectors
func AllSelector() iprime.Node {
	ssb := builder.NewSelectorSpecBuilder(basicnode.Prototype.Any)
	return ssb.ExploreRecursive(selector.RecursionLimitNone(),
		ssb.ExploreAll(ssb.ExploreRecursiveEdge())).Node()
}

// Retrieve an entire dag from the root.
func (s *Session) Retrieve(ctx context.Context, sender peer.ID, addr address.Address) error {
	// TODO: price should be determined based on provider proposal
	pricePerByte := big.Zero()
	paymentInterval := uint64(10000)
	paymentIntervalIncrease := uint64(1000)
	unsealPrice := big.Zero()
	params, err := deal.NewParams(pricePerByte, paymentInterval, paymentIntervalIncrease, AllSelector(), nil, unsealPrice)
	expectedTotal := big.Zero()

	_, err = s.retriever.Retrieve(ctx, s.root, params, expectedTotal, sender, s.addr, addr, nil)
	if err != nil {
		return err
	}
	return nil
}

// Done returns a channel that receives any resulting error from the latest operation
func (s *Session) Done() <-chan error {
	return s.done
}

// Close removes any listeners and stream handlers related to a session
func (s *Session) Close() {
	s.unsub()
	s.net.StopHandlingRequests()
}
