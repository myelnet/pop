package pop

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"time"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-multistore"
	"github.com/filecoin-project/go-state-types/abi"
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
	ctx       context.Context
	cancelCtx context.CancelFunc
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
	// is not nil we've run out of options and nothing we can do at this time will get us the content.
	done chan error
	// err receives any kind of error status from execution so we can try to fix it.
	err chan deal.Status
	// unsubscribes is used to clear any subscriptions to our retrieval events when we have received
	// all the content
	unsub retrieval.Unsubscribe
	// worker executes retrieval over one or more offers
	worker OfferWorker
	// ongoing
	ongoing chan DealRef
	// selecting is a stream of deals that require confirmation
	// if it's nil we don't need confirmation
	selecting chan DealSelection
}

// DealRef is the reference to an ongoing deal
type DealRef struct {
	ID    deal.ID
	Offer deal.Offer
}

// DealSelection sends the selected offer with a channel to expect confirmation on
type DealSelection struct {
	Offer   deal.Offer
	confirm chan bool
}

// Incline accepts execution for an offer
func (ds DealSelection) Incline() {
	ds.confirm <- true
}

// Decline an offer
func (ds DealSelection) Decline() {
	ds.confirm <- false
}

// QueryMiner asks a storage miner for retrieval conditions
func (s *Session) QueryMiner(ctx context.Context, p peer.AddrInfo) error {
	stream, err := s.net.NewQueryStream(p.ID)
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
	s.worker.Receive(p, res)
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

// Execute starts a retrieval operation for a given offer and returns the deal ID for that operation
func (s *Session) Execute(of deal.Offer) error {
	// Make sure our provider is in our peerstore
	s.net.AddAddrs(of.Provider.ID, of.Provider.Addrs)
	params, err := deal.NewParams(
		of.Response.MinPricePerByte,
		of.Response.MaxPaymentInterval,
		of.Response.MaxPaymentIntervalIncrease,
		AllSelector(),
		nil,
		of.Response.UnsealPrice,
	)
	if err != nil {
		return err
	}

	id, err := s.retriever.Retrieve(
		s.ctx,
		s.root,
		params,
		of.Response.PieceRetrievalPrice(),
		of.Provider.ID,
		s.clientAddr,
		of.Response.PaymentAddress,
		&s.storeID,
	)
	if err != nil {
		return err
	}
	s.ongoing <- DealRef{
		ID:    id,
		Offer: of,
	}
	select {
	case status := <-s.err:
		// For now we just return the error and assume the transfer is failed
		// we do have access to the status in order to try and restart the deal or something else
		return errors.New(deal.Statuses[status])
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

// Confirm takes an offer and blocks to wait for user confirmation before returning true or false
func (s *Session) Confirm(of deal.Offer) bool {
	if s.selecting != nil {
		dch := make(chan bool, 1)
		s.selecting <- DealSelection{
			Offer:   of,
			confirm: dch,
		}
		select {
		case d := <-dch:
			return d
		case <-s.ctx.Done():
			return false
		}
	}
	return true
}

// Checkout the next selection
func (s *Session) Checkout() (DealSelection, error) {
	select {
	case dc := <-s.selecting:
		return dc, nil
	case <-s.ctx.Done():
		return DealSelection{}, s.ctx.Err()
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

// Ongoing exposes the ongoing channel to get the reference of any in progress deals
func (s *Session) Ongoing() <-chan DealRef {
	return s.ongoing
}

// Close removes any listeners and stream handlers related to a session
func (s *Session) Close() {
	s.unsub()
	s.cancelCtx()
}

// SetAddress to use for funding the retriebal
func (s *Session) SetAddress(addr address.Address) {
	s.clientAddr = addr
}

// StoreID returns the store ID used for this session
func (s *Session) StoreID() multistore.StoreID {
	return s.storeID
}

// ErrUserDeniedOffer is returned when a user denies an offer
var ErrUserDeniedOffer = errors.New("user denied offer")

// OfferWorker is a generic interface to manage the lifecycle of offers
type OfferWorker interface {
	retrieval.OfferReceiver
	Start()
	Close() []deal.Offer
}

// OfferExecutor exposes the methods required to execute offers
type OfferExecutor interface {
	Execute(deal.Offer) error
	Confirm(deal.Offer) bool
}

// SelectionStrategy is a function that returns an OfferWorker with a defined strategy
// for selecting offers over a given session
type SelectionStrategy func(OfferExecutor) OfferWorker

// We offer a useful presets

// SelectFirst executes the first offer received and buffers other offers during the
// duration of the transfer. If the transfer hard fails it tries continuing with the following offer and so on.
func SelectFirst(oe OfferExecutor) OfferWorker {
	return sessionWorker{
		executor:      oe,
		offersIn:      make(chan deal.Offer),
		closing:       make(chan chan []deal.Offer, 1),
		numThreshold:  -1,
		timeThreshold: -1,
		priceCeiling:  abi.NewTokenAmount(-1),
	}
}

// SelectCheapest waits for a given amount of offers or delay whichever comes first and selects the cheapest then continues
// receiving offers while the transfer executes. If the transfer fails it will select the next cheapest
// given the buffered offers
func SelectCheapest(after int, t time.Duration) func(OfferExecutor) OfferWorker {
	return func(oe OfferExecutor) OfferWorker {
		return sessionWorker{
			executor:      oe,
			offersIn:      make(chan deal.Offer),
			closing:       make(chan chan []deal.Offer, 1),
			numThreshold:  after,
			timeThreshold: t,
			priceCeiling:  abi.NewTokenAmount(-1),
		}
	}
}

// SelectFirstLowerThan returns the first offer which price is lower than given amount
// it keeps collecting offers below price threshold to fallback on before completing execution
func SelectFirstLowerThan(amount abi.TokenAmount) func(oe OfferExecutor) OfferWorker {
	return func(oe OfferExecutor) OfferWorker {
		return sessionWorker{
			executor:      oe,
			offersIn:      make(chan deal.Offer),
			closing:       make(chan chan []deal.Offer, 1),
			numThreshold:  -1,
			timeThreshold: -1,
			priceCeiling:  amount,
		}
	}
}

type sessionWorker struct {
	executor OfferExecutor
	offersIn chan deal.Offer
	closing  chan chan []deal.Offer
	// numThreshold is the number of offers after which we can start execution
	// if -1 we execute as soon as the first offer gets in
	numThreshold int
	// timeThreshold is the duration after which we can start execution
	// if -1 we execute as soon as the first offer gets in
	timeThreshold time.Duration
	// priceCeiling is the price over which we are ignoring an offer for this session
	priceCeiling abi.TokenAmount
}

func (s sessionWorker) exec(offer deal.Offer, result chan error) {
	// Confirm may block until user sends a response
	// if we are blocking for a while the worker acts as if we'd started the transfer and will continue
	// buffering offers according to the given rules
	if s.executor.Confirm(offer) {
		result <- s.executor.Execute(offer)
		return
	}
	result <- ErrUserDeniedOffer
}

// Start a background routine which can be shutdown by sending a channel to the closing channel
func (s sessionWorker) Start() {
	// nil by default if we have a timeThreshold we assign it
	var delay <-chan time.Time
	if s.timeThreshold >= 0 {
		// delay after which we can start executing the first offer
		delay = time.After(s.timeThreshold)
	}
	// Use the price ceiling if the value is not -1
	useCeiling := !s.priceCeiling.Equals(abi.NewTokenAmount(-1))
	// Start a routine to collect a set of offers
	go func() {
		// Offers are queued in this slice
		var q []deal.Offer
		var execDone chan error
		for {
			var updates chan error
			if len(q) > 0 {
				// We only want to receive updates when we have some offers in the queue
				// otherwise we have no way to pick up execution with the next offer
				updates = execDone
			}
			select {
			case resc := <-s.closing:
				resc <- q
				return
			case of := <-s.offersIn:
				if useCeiling && of.Response.MinPricePerByte.LessThan(s.priceCeiling) {
					continue
				}
				if s.numThreshold < 0 && s.timeThreshold < 0 && execDone == nil {
					execDone = make(chan error, 1)
					go s.exec(of, execDone)
					continue
				}

				q = append(q, of)
				// We're already executing an offer we can ignore the rest
				if execDone != nil {
					continue
				}
				// If after this one we've reached the threshold let's execute the cheapest offer
				if len(q) == s.numThreshold {
					execDone = make(chan error, 1)
					sortOffers(q)
					go s.exec(q[0], execDone)
					q = q[1:]
				}
			case <-delay:
				// We may already be executing if we've reached another threshold
				if execDone != nil {
					continue
				}
				execDone = make(chan error, 1)
				sortOffers(q)
				go s.exec(q[0], execDone)
				q = q[1:]
			case err := <-updates:
				// If the execution returns an error we assume it is not fixable
				// and automatically try the next offer
				if err != nil && len(q) > 0 {
					execDone = make(chan error, 1)
					go s.exec(q[0], execDone)
					q = q[1:]
				}
			}
		}
	}()
}

// Close the selection returns the last unused offers
func (s sessionWorker) Close() []deal.Offer {
	resc := make(chan []deal.Offer)
	s.closing <- resc
	return <-resc
}

// Receive sends a new offer to the queue
func (s sessionWorker) Receive(p peer.AddrInfo, res deal.QueryResponse) {
	// This never blocks as our queue is always receiving and decides when to drop offers
	s.offersIn <- deal.Offer{
		Provider: p,
		Response: res,
	}
}

func sortOffers(offers []deal.Offer) {
	sort.Slice(offers, func(i, j int) bool {
		return offers[i].Response.MinPricePerByte.LessThan(offers[j].Response.MinPricePerByte)
	})
}
