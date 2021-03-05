package hop

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	"github.com/filecoin-project/go-address"
	cid "github.com/ipfs/go-cid"
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
	regionTopics map[string]*pubsub.Topic
	net          retrieval.QueryNetwork
	retriever    *retrieval.Client
	clientAddr   address.Address
	root         cid.Cid
	sel          iprime.Node
	done         chan error
	unsub        retrieval.Unsubscribe

	mu        sync.Mutex
	responses map[peer.ID]deal.QueryResponse // List of all the responses peers sent us back for a gossip query
	dealID    *deal.ID                       // current ongoing deal if any
}

type gossipSourcing struct {
	offers chan deal.Offer // stream of offers coming from a gossip query
}

// HandleQueryStream for direct provider queries
func (g *gossipSourcing) HandleQueryStream(stream retrieval.QueryStream) {

	defer stream.Close()

	response, err := stream.ReadQueryResponse()
	if err != nil {
		fmt.Println("unable to read query response", err)
		return
	}

	g.offers <- deal.Offer{
		PeerID:   stream.OtherPeer(),
		Response: response,
	}
}

// QueryMiner asks a storage miner for retrieval conditions
func (s *Session) QueryMiner(ctx context.Context, pid peer.ID) (*deal.Offer, error) {
	stream, err := s.net.NewQueryStream(pid)
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	err = stream.WriteQuery(deal.Query{
		PayloadCID:  s.root,
		QueryParams: deal.QueryParams{},
	})
	if err != nil {
		return nil, err
	}

	res, err := stream.ReadQueryResponse()
	if err != nil {
		return nil, err
	}
	return &deal.Offer{
		PeerID:   pid,
		Response: res,
	}, nil
}

// QueryGossip asks the gossip network of providers if anyone can provide the blocks we're looking for
// it blocks execution until our conditions are satisfied
func (s *Session) QueryGossip(ctx context.Context) (*deal.Offer, error) {
	offers := make(chan deal.Offer, 1)
	disc := &gossipSourcing{offers}
	s.net.SetDelegate(disc)

	m := deal.Query{
		PayloadCID:  s.root,
		QueryParams: deal.QueryParams{},
	}

	buf := new(bytes.Buffer)
	if err := m.MarshalCBOR(buf); err != nil {
		return nil, err
	}

	// publish to all regions this exchange joined
	for _, topic := range s.regionTopics {
		if err := topic.Publish(ctx, buf.Bytes()); err != nil {
			return nil, err
		}
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

// SyncBlocks will trigger a retrieval without returning the blocks
func (s *Session) SyncBlocks(ctx context.Context, of *deal.Offer) error {
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

// SetAddress to use for funding the retriebal
func (s *Session) SetAddress(addr address.Address) {
	s.clientAddr = addr
}
