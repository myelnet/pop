package hop

import (
	"bytes"
	"context"
	"fmt"

	"github.com/filecoin-project/go-address"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-multistore"
	"github.com/filecoin-project/go-state-types/big"
	cid "github.com/ipfs/go-cid"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	cbor "github.com/ipfs/go-ipld-cbor"
	"github.com/libp2p/go-libp2p-core/host"
	peer "github.com/libp2p/go-libp2p-core/peer"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/myelnet/go-hop-exchange/filecoin"
	"github.com/myelnet/go-hop-exchange/payments"
	"github.com/myelnet/go-hop-exchange/retrieval"
	"github.com/myelnet/go-hop-exchange/retrieval/client"
	"github.com/myelnet/go-hop-exchange/retrieval/deal"
	"github.com/myelnet/go-hop-exchange/supply"
	"github.com/myelnet/go-hop-exchange/wallet"
)

// RequestTopic listens for peers looking for content blocks
const RequestTopic = "/myel/hop/request/1.0"

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
	ex.net = NewFromLibp2pHost(ex.h)

	// Retrieval data transfer setup
	ex.dataTransfer, err = NewDataTransfer(ctx, ex.h, set.GraphSync, set.Datastore, "retrieval", set.RepoPath)
	if err != nil {
		return nil, err
	}
	ex.multiStore, err = multistore.NewMultiDstore(set.Datastore)
	if err != nil {
		return nil, err
	}
	// Add a special adaptor to use the blockstore with cbor encoding
	cborblocks := cbor.NewCborStore(set.Blockstore)
	// Create our payment manager
	paym := payments.New(ctx, ex.fAPI, ex.wallet, set.Datastore, cborblocks)
	// Create our retrieval manager
	ex.retrieval, err = retrieval.New(ctx, ex.multiStore, set.Datastore, paym, ex.dataTransfer, ex.h.ID())
	if err != nil {
		return nil, err
	}
	// TODO: this is an empty validator for now
	ex.dataTransfer.RegisterVoucherType(&StorageDataTransferVoucher{}, &UnifiedRequestValidator{})

	// TODO: validate AddRequest
	ex.dataTransfer.RegisterVoucherType(&supply.AddRequest{}, &UnifiedRequestValidator{})

	ex.supply = supply.New(ctx, ex.h, ex.dataTransfer, set.Regions)

	return ex, ex.joinRegions(ctx, set.Regions)
}

// Exchange is a gossip based exchange for retrieving blocks from Filecoin
type Exchange struct {
	h            host.Host
	bs           blockstore.Blockstore
	multiStore   *multistore.MultiStore
	retrieval    retrieval.Manager
	supply       supply.Manager
	ps           *pubsub.PubSub
	provTopic    *pubsub.Topic
	regionSubs   map[string]*pubsub.Subscription
	regionTopics map[string]*pubsub.Topic
	net          RetrievalMarketNetwork
	dataTransfer datatransfer.Manager
	wallet       wallet.Driver
	cidListDir   string
	fAPI         filecoin.API
}

// joinRegions allows a provider to handle request in specific CDN regions
// TODO: allow nodes to join and leave regions without restarting
func (e *Exchange) joinRegions(ctx context.Context, rgs []supply.Region) error {
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

		go e.requestLoop(ctx, sub)
	}

	return nil
}

// Dispatch new content to cache providers
func (e *Exchange) Dispatch(c cid.Cid) error {
	size, err := e.bs.GetSize(c)
	if err != nil {
		return err
	}
	return e.supply.SendAddRequest(c, uint64(size))
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
		blockstore:   e.bs,
		regionTopics: e.regionTopics,
		net:          e.net,
		root:         root,
		retriever:    cl,
		clientAddr:   e.wallet.DefaultAddress(),
		ctx:          ctx,
		done:         done,
		unsub:        unsubscribe,
	}
	return session, nil
}

// requestLoop runs by default in the background when the Hop client is initialized
// it iterates over new gossip messages and sends a response if we have the block in store
func (e *Exchange) requestLoop(ctx context.Context, sub *pubsub.Subscription) {
	for {
		msg, err := sub.Next(ctx)
		if err != nil {
			return
		}
		if msg.ReceivedFrom == e.h.ID() {
			continue
		}
		m := new(Query)
		if err := m.UnmarshalCBOR(bytes.NewReader(msg.Data)); err != nil {
			continue
		}
		// DAGStat is both a way of checking if we have the blocks and returning its size
		// TODO: support selector in Query
		stats, err := DAGStat(ctx, e.bs, m.PayloadCID, AllSelector())
		// We don't have the block we don't even reply to avoid taking bandwidth
		// On the client side we assume no response means they don't have it
		if err == nil && stats.Size > 0 {
			qs, err := e.net.NewQueryStream(msg.ReceivedFrom)
			if err != nil {
				fmt.Println("error", err)
				continue
			}
			e.sendQueryResponse(qs, QueryResponseAvailable, uint64(stats.Size))
		}
	}
}

func (e *Exchange) sendQueryResponse(stream RetrievalQueryStream, status QueryResponseStatus, size uint64) {
	ask := &Ask{
		PricePerByte:            big.Zero(),
		PaymentInterval:         DefaultPaymentInterval,
		PaymentIntervalIncrease: DefaultPaymentIntervalIncrease,
	}

	answer := QueryResponse{
		Status:                     status,
		Size:                       size,
		PaymentAddress:             e.wallet.DefaultAddress(),
		MinPricePerByte:            ask.PricePerByte,
		MaxPaymentInterval:         ask.PaymentInterval,
		MaxPaymentIntervalIncrease: ask.PaymentIntervalIncrease,
	}
	if err := stream.WriteQueryResponse(answer); err != nil {
		fmt.Printf("Retrieval query: WriteCborRPC: %s", err)
		return
	}
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
func (e *Exchange) Supply() supply.Manager {
	return e.supply
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
