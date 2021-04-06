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
	pb "github.com/libp2p/go-libp2p-pubsub/pb"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/myelnet/pop/filecoin"
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
	ex.net = retrieval.NewQueryNetwork(ex.h, retrieval.NetMetadata(set.GossipTracer))
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
	err = ex.supply.Start(ctx)
	if err != nil {
		return nil, err
	}
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

	mu           sync.Mutex
	regionSubs   map[string]*pubsub.Subscription
	regionTopics map[string]*pubsub.Topic
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
		m := new(deal.Query)
		if err := m.UnmarshalCBOR(bytes.NewReader(msg.Data)); err != nil {
			continue
		}

		store, err := e.supply.GetStore(m.PayloadCID)
		if err != nil {
			// TODO: we need to log when we couldn't find some content so we can try looking for it
			fmt.Println("no store found for", m.PayloadCID)
			continue
		}
		// DAGStat is both a way of checking if we have the blocks and returning its size
		// TODO: support selector in Query
		stats, err := DAGStat(ctx, store.Bstore, m.PayloadCID, AllSelector())
		if err != nil {
			fmt.Println("failed to get content stat", err, e.h.ID())
		}
		// We don't have the block we don't even reply to avoid taking bandwidth
		// On the client side we assume no response means they don't have it
		if err == nil && stats.Size > 0 {
			qs, err := e.net.NewQueryStream(msg.ReceivedFrom)
			if err != nil {
				fmt.Println("failed to create response query stream", err)
				continue
			}
			addrs, err := e.net.Addrs()
			if err != nil {
				continue
			}
			mid := pubsub.DefaultMsgIdFn(msg.Message)
			// Our message payload includes the message ID and the recipient peer address
			// The index indicates where to slice the string to extract both values
			p := fmt.Sprintf("%d%s%s", len(mid), mid, string(addrs[0].Bytes()))
			answer := deal.QueryResponse{
				Status:                     deal.QueryResponseAvailable,
				Size:                       uint64(stats.Size),
				PaymentAddress:             e.wallet.DefaultAddress(),
				MinPricePerByte:            r.PPB, // TODO: dynamic pricing
				MaxPaymentInterval:         deal.DefaultPaymentInterval,
				MaxPaymentIntervalIncrease: deal.DefaultPaymentIntervalIncrease,
				Message:                    p,
			}
			if err := qs.WriteQueryResponse(answer); err != nil {
				fmt.Printf("retrieval query: WriteCborRPC: %s\n", err)
				continue
			}
			// We need to remember the offer we made so we can validate against it once
			// clients start the retrieval
			e.retrieval.Provider().SetAsk(m.PayloadCID, answer)
		}
	}
}

// NewSession returns a new retrieval session
func (e *Exchange) NewSession(ctx context.Context, root cid.Cid, strategy SelectionStrategy) *Session {
	// This cancel allows us to shutdown the retrieval process with the session if needed
	ctx, cancel := context.WithCancel(ctx)
	// Track when the session is completed
	done := make(chan error)
	// Track any issues with the transfer
	err := make(chan deal.Status)
	// Subscribe to client events to send to the channel
	cl := e.retrieval.Client()
	unsubscribe := cl.SubscribeToEvents(func(event client.Event, state deal.ClientState) {
		switch state.Status {
		case deal.StatusCompleted:
			done <- nil
			return
		case deal.StatusCancelled, deal.StatusErrored:
			err <- state.Status
			return
		}
	})
	session := &Session{
		ctx:          ctx,
		cancelCtx:    cancel,
		regionTopics: e.regionTopics,
		net:          e.net,
		root:         root,
		retriever:    cl,
		clientAddr:   e.wallet.DefaultAddress(),
		done:         done,
		err:          err,
		ongoing:      make(chan DealRef),
		selecting:    make(chan DealSelection),
		unsub:        unsubscribe,
		// We create a fresh new store for this session
		storeID: e.multiStore.Next(),
	}
	session.worker = strategy(session)
	session.worker.Start()
	session.net.SetReceiver(session.worker)
	return session
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

// GossipTracer tracks messages we've seen so we can relay responses back to the publisher
type GossipTracer struct {
	published map[string]bool
	senders   map[string]peer.ID
}

func NewGossipTracer() *GossipTracer {
	return &GossipTracer{
		published: make(map[string]bool),
		senders:   make(map[string]peer.ID),
	}
}

// Trace gets triggered for every internal gossip sub operation
func (gt *GossipTracer) Trace(evt *pb.TraceEvent) {
	if evt.PublishMessage != nil {
		gt.published[string(evt.PublishMessage.MessageID)] = true
	}
	if evt.DeliverMessage != nil {
		msg := evt.DeliverMessage
		gt.senders[string(msg.MessageID)] = peer.ID(msg.ReceivedFrom)
	}
}

// Published checks if we were the publisher of a message
func (gt *GossipTracer) Published(mid string) bool {
	return gt.published[mid]
}

// Sender returns the peer who sent us a message
func (gt *GossipTracer) Sender(mid string) (peer.ID, error) {
	p, ok := gt.senders[mid]
	if !ok {
		return "", errors.New("no sender found")
	}
	return p, nil
}
