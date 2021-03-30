package pop

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-multistore"
	cid "github.com/ipfs/go-cid"
	iprime "github.com/ipld/go-ipld-prime"
	basicnode "github.com/ipld/go-ipld-prime/node/basic"
	"github.com/ipld/go-ipld-prime/traversal/selector"
	"github.com/ipld/go-ipld-prime/traversal/selector/builder"
	peer "github.com/libp2p/go-libp2p-core/peer"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/myelnet/pop/retrieval"
	"github.com/myelnet/pop/retrieval/deal"
)

// Session to exchange multiple blocks with a set of connected peers
type Session struct {
	// storeID is the unique store used to load the content retrieved during this session
	storeID multistore.StoreID
	// regionTopics are all the region gossip subscriptions this session can query to find the content
	regionTopics map[string]*pubsub.Topic
	// net is the network procotol used by providers to send their offers
	net retrieval.QueryNetwork
	// retriever manages the state of the transfer once we have a good offer
	retriever *retrieval.Client
	// clientAddr is the address that will be used to make any payment for retrieving the content
	clientAddr address.Address
	// root is the root cid of the dag we are retrieving during this session
	root cid.Cid
	// sel is the selector used to select specific nodes only to retrieve. if not provided we select
	// all the nodes by default
	sel iprime.Node
	// done is the final message telling us we have received all the blocks and all is well. if the error
	// is not nil, then all is actually not well and we may need to try again.
	done chan error
	// unsubscribes is used to clear any subscriptions to our retrieval events when we have received
	// all the content
	unsub retrieval.Unsubscribe
	// offers it the global offer queue
	offers *OfferQueue

	mu sync.Mutex
	// dealID is the ID of any ongoing deal we might have with a provider during this session
	dealID *deal.ID
}

// QueryMiner asks a storage miner for retrieval conditions
func (s *Session) QueryMiner(ctx context.Context, pid peer.ID) error {
	stream, err := s.net.NewQueryStream(pid)
	if err != nil {
		return err
	}
	defer stream.Close()

	err = stream.WriteQuery(deal.Query{
		PayloadCID:  s.root,
		QueryParams: deal.QueryParams{},
	})
	if err != nil {
		return err
	}

	res, err := stream.ReadQueryResponse()
	if err != nil {
		return err
	}
	s.offers.Receive(pid, res)
	return nil
}

// QueryGossip asks the gossip network of providers if anyone can provide the blocks we're looking for
// it blocks execution until our conditions are satisfied
func (s *Session) QueryGossip(ctx context.Context) error {
	m := deal.Query{
		PayloadCID:  s.root,
		QueryParams: deal.QueryParams{},
	}

	buf := new(bytes.Buffer)
	if err := m.MarshalCBOR(buf); err != nil {
		return err
	}

	// publish to all regions this exchange joined
	for _, topic := range s.regionTopics {
		if err := topic.Publish(ctx, buf.Bytes()); err != nil {
			return err
		}
	}

	return nil
}

// StartTransfer sends a transfer job to the offer queue worker and waits for it to start
// it will get assigned to the first offer in the queue
// TODO: we should be able to pass some rules to select a best offer
func (s *Session) StartTransfer(ctx context.Context) error {
	// Queue the job to handle once we get an offer
	job := func(ctx context.Context, of deal.Offer) (deal.ID, error) {
		params, err := deal.NewParams(
			of.Response.MinPricePerByte,
			of.Response.MaxPaymentInterval,
			of.Response.MaxPaymentIntervalIncrease,
			AllSelector(),
			nil,
			of.Response.UnsealPrice,
		)
		if err != nil {
			return 0, err
		}

		return s.retriever.Retrieve(
			ctx,
			s.root,
			params,
			of.Response.PieceRetrievalPrice(),
			of.PeerID,
			s.clientAddr,
			of.Response.PaymentAddress,
			&s.storeID,
		)
	}
	s.offers.HandleNext(job)
	select {
	case r := <-s.offers.results:
		if r.Err != nil {
			return r.Err
		}
		s.setDealID(r.DealID)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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

func (s *Session) setDealID(d deal.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dealID = &d
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
}

// SetAddress to use for funding the retriebal
func (s *Session) SetAddress(addr address.Address) {
	s.clientAddr = addr
}

// StoreID returns the store ID used for this session
func (s *Session) StoreID() multistore.StoreID {
	return s.storeID
}

// OfferQueue returns the global offer queue
func (s *Session) OfferQueue() *OfferQueue {
	return s.offers
}
