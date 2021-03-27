package pop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/filecoin-project/go-address"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-multistore"
	cid "github.com/ipfs/go-cid"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	cbor "github.com/ipfs/go-ipld-cbor"
	"github.com/libp2p/go-libp2p-core/host"
	peer "github.com/libp2p/go-libp2p-core/peer"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/myelnet/pop/filecoin"
	"github.com/myelnet/pop/internal/utils"
	"github.com/myelnet/pop/payments"
	"github.com/myelnet/pop/retrieval"
	"github.com/myelnet/pop/retrieval/client"
	"github.com/myelnet/pop/retrieval/deal"
	"github.com/myelnet/pop/supply"
	"github.com/myelnet/pop/wallet"
)

// RequestTopic listens for peers looking for content blocks
const RequestTopic = "/myel/pop/request/1.0"

// NewExchange creates a Hop exchange struct
func NewExchange(ctx context.Context, set Settings) (*Exchange, error) {
	var err error
	ex := &Exchange{
		h:            set.Host,
		ps:           set.PubSub,
		bs:           set.Blockstore,
		regionSubs:   make(map[string]*pubsub.Subscription),
		regionTopics: make(map[string]*pubsub.Topic),
	}

	// Start our lotus api if we have an endpoint.
	if set.FilecoinRPCEndpoint != "" {
		ex.fAPI, err = filecoin.NewLotusRPC(ctx, set.FilecoinRPCEndpoint, set.FilecoinRPCHeader)
		if err != nil {
			return nil, err
		}
	}
	// Set wallet from IPFS Keystore, we should make this more generic eventually
	ex.wallet = wallet.NewIPFS(set.Keystore, ex.fAPI)
	// Make a new default key to be sure we have an address where to receive our payments
	if ex.wallet.DefaultAddress() == address.Undef {
		_, err = ex.wallet.NewKey(ctx, wallet.KTSecp256k1)
		if err != nil {
			return nil, err
		}
	}
	// Setup the messaging protocol for communicating retrieval deals
	ex.net = retrieval.NewQueryNetwork(ex.h)
	// We must start the offer queue now otherwise there is a race condition when trying to set it
	// at the moment we're querying for offers
	ex.ofq = NewOfferQueue(ctx)
	ex.net.Start(ex.ofq)
	// Retrieval data transfer setup
	ex.dataTransfer, err = NewDataTransfer(ctx, ex.h, set.GraphSync, set.Datastore, "retrieval", set.RepoPath)
	if err != nil {
		return nil, err
	}
	ex.multiStore = set.MultiStore
	// Add a special adaptor to use the blockstore with cbor encoding
	cborblocks := cbor.NewCborStore(set.Blockstore)
	// Create our payment manager
	paym := payments.New(ctx, ex.fAPI, ex.wallet, set.Datastore, cborblocks)
	// create the supply manager to handle optimisations of the block supply
	ex.supply = supply.New(ex.h, ex.dataTransfer, set.Datastore, ex.multiStore, set.Regions)
	// Create our retrieval manager
	ex.retrieval, err = retrieval.New(
		ctx,
		ex.multiStore,
		set.Datastore,
		paym,
		ex.dataTransfer,
		ex.supply,
		ex.h.ID(),
	)
	if err != nil {
		return nil, err
	}

	return ex, ex.joinRegions(ctx, set.Regions)
}

// Exchange is a gossip based exchange for retrieving blocks from Filecoin
type Exchange struct {
	h            host.Host
	bs           blockstore.Blockstore
	multiStore   *multistore.MultiStore
	ps           *pubsub.PubSub
	dataTransfer datatransfer.Manager

	retrieval retrieval.Manager
	net       retrieval.QueryNetwork
	supply    *supply.Supply
	wallet    wallet.Driver
	fAPI      filecoin.API
	ofq       *OfferQueue

	mu           sync.Mutex
	regionSubs   map[string]*pubsub.Subscription
	regionTopics map[string]*pubsub.Topic
}

// ErrQueueEmpty is returned when peeking if the queue is empty
var ErrQueueEmpty = errors.New("queue is empty")

// OfferQueue receives query responses from the network and queues them up in a buffer
// the goal is to fallback on other offers if a transfer goes wrong which means being able
// to seemlessly continue the transfer with another peer
// Eventually we will want to function with multiple providers at once
type OfferQueue struct {
	offers  chan deal.Offer
	results chan JobResult
	jobs    chan Job
	peek    chan chan deal.Offer
}

// JobResult tells us if our job was started successfully
// and the offer it's running on
type JobResult struct {
	DealID deal.ID
	Offer  deal.Offer
	Err    error
}

// Job is a retrieval operation to be performed on an offer
type Job func(context.Context, deal.Offer) (deal.ID, error)

// NewOfferQueue starts a worker to process received offers and run jobs on queued up offers
func NewOfferQueue(ctx context.Context) *OfferQueue {
	oq := &OfferQueue{
		offers:  make(chan deal.Offer),
		results: make(chan JobResult),
		peek:    make(chan chan deal.Offer),
		jobs:    make(chan Job, 8),
	}
	go oq.processOffers(ctx)
	return oq
}

// HandleNext adds a job in the queue to run on the next available offer
func (oq *OfferQueue) HandleNext(fn Job) {
	oq.jobs <- fn
}

// Receive sends a new offer to the queue
func (oq *OfferQueue) Receive(p peer.ID, res deal.QueryResponse) {
	oq.offers <- deal.Offer{
		PeerID:   p,
		Response: res,
	}
}

// Peek returns the first item in the queue or an error if no item is loaded yet
func (oq *OfferQueue) Peek(ctx context.Context) (deal.Offer, error) {
	result := make(chan deal.Offer)
	oq.peek <- result

	select {
	case r := <-result:
		return r, nil
	case <-ctx.Done():
		return deal.Offer{}, ctx.Err()
	}
}

// processOffers  is a worker that receives jobs to handle queued up offers
// TODO: we need to make sure we are matching the job with the right offer
func (oq *OfferQueue) processOffers(ctx context.Context) {
	// This is our offer queue
	maxQSize := 17
	var q []deal.Offer
	for {
		var first deal.Offer
		// if there are no offers the jobs channel should be
		// nil so wait for the next offer to execute it
		var jobs chan Job
		var peek chan chan deal.Offer
		if len(q) > 0 {
			first = q[0]
			jobs = oq.jobs
			peek = oq.peek
		}
		select {
		// If we have a job and a queued up offer we run the job on the first offer
		case j := <-jobs:
			did, err := j(ctx, first)
			oq.results <- JobResult{did, first, err}

			// remove the offer from the queue
			q = q[1:]
			// If we receive a new offer we append it to the queue
		case of := <-oq.offers:
			// If we already have too many offers we drop them
			if len(q) < maxQSize {
				q = append(q, of)
			}
		// if we get a peek request we send our first item to it
		case req := <-peek:
			req <- first
		case <-ctx.Done():
			// exit when the context is done
			return
		}
	}
}

// joinRegions allows a provider to handle request in specific CDN regions
// TODO: allow nodes to join and leave regions without restarting
func (e *Exchange) joinRegions(ctx context.Context, rgs []supply.Region) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	// Gossip sub subscription for incoming content queries
	for _, r := range rgs {
		topic, err := e.ps.Join(fmt.Sprintf("%s/%s", RequestTopic, r.Name))
		if err != nil {
			return err
		}
		e.regionTopics[r.Name] = topic

		sub, err := topic.Subscribe()
		if err != nil {
			return err
		}
		e.regionSubs[r.Name] = sub
		// each request loop may provide different pricings based on the region
		go e.requestLoop(ctx, sub, r)
	}

	return nil
}

// requestLoop runs by default in the background when the pop client is initialized
// it iterates over new gossip messages and sends a response if we have the block in store
func (e *Exchange) requestLoop(ctx context.Context, sub *pubsub.Subscription, r supply.Region) {
	for {
		msg, err := sub.Next(ctx)
		if err != nil {
			return
		}
		if msg.ReceivedFrom == e.h.ID() {
			continue
		}
		m := new(deal.GossipQuery)
		if err := m.UnmarshalCBOR(bytes.NewReader(msg.Data)); err != nil {
			continue
		}

		store, err := e.supply.GetStore(m.PayloadCID)
		if err != nil {
			// TODO: we need to log when we couldn't find some content so we can try looking for it
			fmt.Printf("no store found for %s \n", m.PayloadCID)
			continue
		}
		// DAGStat is both a way of checking if we have the blocks and returning its size
		// TODO: support selector in Query
		stats, err := DAGStat(ctx, store.Bstore, m.PayloadCID, AllSelector())
		if err != nil {
			fmt.Printf("failed to get content stat: %s\n", err)
		}
		// We don't have the block we don't even reply to avoid taking bandwidth
		// On the client side we assume no response means they don't have it
		if err == nil && stats.Size > 0 {
			// Only parse the addr info if we're gonna reply
			pi, err := utils.AddrStringToAddrInfo(m.Publisher)
			if err != nil {
				fmt.Println("invalid p2p addr", err)
				continue
			}
			e.net.AddAddrs(pi.ID, pi.Addrs)

			qs, err := e.net.NewQueryStream(pi.ID)
			if err != nil {
				fmt.Println("error", err)
				continue
			}
			answer := deal.QueryResponse{
				Status:                     deal.QueryResponseAvailable,
				Size:                       uint64(stats.Size),
				PaymentAddress:             e.wallet.DefaultAddress(),
				MinPricePerByte:            r.PPB, // TODO: dynamic pricing
				MaxPaymentInterval:         deal.DefaultPaymentInterval,
				MaxPaymentIntervalIncrease: deal.DefaultPaymentIntervalIncrease,
			}
			if err := qs.WriteQueryResponse(answer); err != nil {
				fmt.Printf("retrieval query: WriteCborRPC: %s\n", err)
				return
			}
			// We need to remember the offer we made so we can validate against it once
			// clients start the retrieval
			e.retrieval.Provider().SetAsk(pi.ID, answer)
		}
	}
}

// NewSession returns a new retrieval session
func (e *Exchange) NewSession(ctx context.Context, root cid.Cid) (*Session, error) {
	// Track when the session is completed
	done := make(chan error)
	// Subscribe to client events to send to the channel
	cl := e.retrieval.Client()
	unsubscribe := cl.SubscribeToEvents(func(event client.Event, state deal.ClientState) {
		switch state.Status {
		case deal.StatusCompleted:
			done <- nil
			return
		case deal.StatusCancelled, deal.StatusErrored:
			done <- fmt.Errorf("retrieval: %v, %v", deal.Statuses[state.Status], state.Message)
			return
		}
	})
	session := &Session{
		regionTopics: e.regionTopics,
		net:          e.net,
		root:         root,
		retriever:    cl,
		clientAddr:   e.wallet.DefaultAddress(),
		done:         done,
		unsub:        unsubscribe,
		offers:       e.ofq,
		// We create a fresh new store for this session
		storeID: e.multiStore.Next(),
	}
	return session, nil
}

// Wallet returns the wallet instance funding the exchange
func (e *Exchange) Wallet() wallet.Driver {
	return e.wallet
}

// DataTransfer gives access to the datatransfer manager instance powering all the transfers for the exchange
func (e *Exchange) DataTransfer() datatransfer.Manager {
	return e.dataTransfer
}

// Supply exposes the supply manager
func (e *Exchange) Supply() *supply.Supply {
	return e.supply
}

// Retrieval is the retrieval module and deal state manager
func (e *Exchange) Retrieval() retrieval.Manager {
	return e.retrieval
}

// FilecoinAPI exposes the low level Filecoin RPC
func (e *Exchange) FilecoinAPI() filecoin.API {
	return e.fAPI
}

// StoragePeerInfo resolves a Filecoin address to find the peer info and add to our address book
func (e *Exchange) StoragePeerInfo(ctx context.Context, addr address.Address) (*peer.AddrInfo, error) {
	miner, err := e.fAPI.StateMinerInfo(ctx, addr, filecoin.EmptyTSK)
	if err != nil {
		return nil, err
	}
	multiaddrs := make([]ma.Multiaddr, 0, len(miner.Multiaddrs))
	for _, a := range miner.Multiaddrs {
		maddr, err := ma.NewMultiaddrBytes(a)
		if err != nil {
			return nil, err
		}
		multiaddrs = append(multiaddrs, maddr)
	}
	if miner.PeerId == nil {
		return nil, fmt.Errorf("no peer id available")
	}
	if len(miner.Multiaddrs) == 0 {
		return nil, fmt.Errorf("no peer address available")
	}
	pi := peer.AddrInfo{
		ID:    *miner.PeerId,
		Addrs: multiaddrs,
	}
	e.net.AddAddrs(pi.ID, pi.Addrs)
	return &pi, nil
}

// IsFilecoinOnline tells us if we are connected to the Filecoin RPC api
func (e *Exchange) IsFilecoinOnline() bool {
	return e.fAPI != nil
}
