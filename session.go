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
	"github.com/myelnet/go-hop-exchange/retrieval/client"
	"github.com/myelnet/go-hop-exchange/retrieval/deal"
)

// Session to exchange multiple blocks with a set of connected peers
type Session struct {
	blockstore blockstore.Blockstore
	reqTopic   *pubsub.Topic
	net        RetrievalMarketNetwork
	retriever  *retrieval.Client
	addr       address.Address
	responses  map[peer.ID]QueryResponse // List of all the responses peers sent us back for a gossip query
	res        chan peer.ID              // First response we get
	started    bool                      // If we found a satisfying response and we started retrieving
	lk         sync.Mutex
}

// HandleQueryStream for direct provider queries
func (s *Session) HandleQueryStream(stream RetrievalQueryStream) {

	defer stream.Close()

	response, err := stream.ReadQueryResponse()
	if err != nil {
		fmt.Println("Unable to read query response", err)
		return
	}

	s.lk.Lock()
	defer s.lk.Unlock()

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
	for {
		select {
		case pid := <-s.res:

			s.lk.Lock()

			response := s.responses[pid]

			s.lk.Unlock()

			// We can decide here some extra logic to reject the response
			// For now we always accept the first one
			err := s.Retrieve(ctx, root, pid, response.PaymentAddress)
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
		case <-ctx.Done():
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

// Retrieve an entire dag from the root.
func (s *Session) Retrieve(ctx context.Context, root cid.Cid, sender peer.ID, addr address.Address) error {

	done := make(chan deal.ClientState, 1)
	unsubscribe := s.retriever.SubscribeToEvents(func(event client.Event, state deal.ClientState) {
		switch state.Status {
		case deal.StatusCompleted, deal.StatusCancelled, deal.StatusErrored:
			done <- state
			return
		}
	})
	defer unsubscribe()

	// TODO: price should be determined based on provider proposal
	pricePerByte := big.Zero()
	paymentInterval := uint64(10000)
	paymentIntervalIncrease := uint64(1000)
	unsealPrice := big.Zero()
	params, err := deal.NewParams(pricePerByte, paymentInterval, paymentIntervalIncrease, AllSelector(), nil, unsealPrice)
	expectedTotal := big.Zero()

	_, err = s.retriever.Retrieve(ctx, root, params, expectedTotal, sender, s.addr, addr, nil)
	if err != nil {
		return err
	}

	select {
	case state := <-done:
		if state.Status == deal.StatusCompleted {
			return nil
		}
		return fmt.Errorf("Retrieval error: %v, %v", deal.Statuses[state.Status], state.Message)
	}
}
