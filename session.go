package hop

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	"github.com/filecoin-project/go-address"
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
	clientAddr address.Address
	root       cid.Cid
	sel        iprime.Node
	ctx        context.Context
	done       chan error
	unsub      retrieval.Unsubscribe

	mu        sync.Mutex
	responses map[peer.ID]QueryResponse // List of all the responses peers sent us back for a gossip query
	dealID    *deal.ID                  // current ongoing deal if any
}

// Offer is the conditions under which a provider is willing to approve a transfer
type Offer struct {
	PeerID   peer.ID
	Response QueryResponse
}

type gossipSourcing struct {
	offers chan Offer // stream of offers coming from a gossip query
}

// HandleQueryStream for direct provider queries
func (g *gossipSourcing) HandleQueryStream(stream RetrievalQueryStream) {

	defer stream.Close()

	response, err := stream.ReadQueryResponse()
	if err != nil {
		fmt.Println("unable to read query response", err)
		return
	}

	g.offers <- Offer{stream.OtherPeer(), response}
}

// QueryMiner asks a storage miner for retrieval conditions
func (s *Session) QueryMiner(ctx context.Context, pid peer.ID) (*Offer, error) {
	stream, err := s.net.NewQueryStream(pid)
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	err = stream.WriteQuery(Query{
		PayloadCID:  s.root,
		QueryParams: QueryParams{},
	})
	if err != nil {
		return nil, err
	}

	res, err := stream.ReadQueryResponse()
	if err != nil {
		return nil, err
	}
	return &Offer{
		PeerID:   pid,
		Response: res,
	}, nil
}

// QueryGossip asks the gossip network of providers if anyone can provide the blocks we're looking for
// it blocks execution until our conditions are satisfied
func (s *Session) QueryGossip(ctx context.Context) (*Offer, error) {
	offers := make(chan Offer, 1)
	disc := &gossipSourcing{offers}
	s.net.SetDelegate(disc)

	m := Query{
		PayloadCID:  s.root,
		QueryParams: QueryParams{},
	}

	buf := new(bytes.Buffer)
	if err := m.MarshalCBOR(buf); err != nil {
		return nil, err
	}

	if err := s.reqTopic.Publish(ctx, buf.Bytes()); err != nil {
		return nil, err
	}

	// TODO: add custom logic to select the right offer
	// for now we always take the first one we get = lowest latency
	for {
		select {
		case offer := <-offers:
			return &offer, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
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

	offer, err := s.QueryGossip(ctx)
	if err != nil {
		return out, err
	}

	return out, s.SyncBlocks(ctx, offer)

}

// SyncBlocks will trigger a retrieval without returning the blocks
func (s *Session) SyncBlocks(ctx context.Context, of *Offer) error {
	params, err := deal.NewParams(
		of.Response.MinPricePerByte,
		of.Response.MaxPaymentInterval,
		of.Response.MaxPaymentIntervalIncrease,
		AllSelector(),
		nil,
		of.Response.UnsealPrice,
	)

	id, err := s.retriever.Retrieve(
		ctx,
		s.root,
		params,
		of.Response.PieceRetrievalPrice(),
		of.PeerID,
		s.clientAddr,
		of.Response.PaymentAddress,
		nil,
	)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.dealID = &id
	s.mu.Unlock()
	return nil
}

// AllSelector to get all the nodes for now. TODO` support custom selectors
func AllSelector() iprime.Node {
	ssb := builder.NewSelectorSpecBuilder(basicnode.Prototype.Any)
	return ssb.ExploreRecursive(selector.RecursionLimitNone(),
		ssb.ExploreAll(ssb.ExploreRecursiveEdge())).Node()
}

// Done returns a channel that receives any resulting error from the latest operation
func (s *Session) Done() <-chan error {
	return s.done
}

// DealID returns the id of the current deal if any
func (s *Session) DealID() (deal.ID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dealID != nil {
		return *s.dealID, nil
	}
	return 0, fmt.Errorf("no active deal")
}

// Close removes any listeners and stream handlers related to a session
func (s *Session) Close() {
	s.unsub()
	s.net.StopHandlingRequests()
}
