package hop

import (
	"bytes"
	"context"
	"fmt"

	"github.com/filecoin-project/go-address"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	dtfimpl "github.com/filecoin-project/go-data-transfer/impl"
	dtnet "github.com/filecoin-project/go-data-transfer/network"
	gstransport "github.com/filecoin-project/go-data-transfer/transport/graphsync"
	"github.com/filecoin-project/go-multistore"
	"github.com/filecoin-project/go-storedcounter"
	blocks "github.com/ipfs/go-block-format"
	cid "github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
	"github.com/ipfs/go-graphsync"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	exchange "github.com/ipfs/go-ipfs-exchange-interface"
	pin "github.com/ipfs/go-ipfs-pinner"
	cbor "github.com/ipfs/go-ipld-cbor"
	"github.com/libp2p/go-libp2p-core/host"
	peer "github.com/libp2p/go-libp2p-core/peer"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/myelnet/go-hop-exchange/filecoin"
	"github.com/myelnet/go-hop-exchange/payments"
	"github.com/myelnet/go-hop-exchange/retrieval"
	"github.com/myelnet/go-hop-exchange/retrieval/client"
	"github.com/myelnet/go-hop-exchange/retrieval/deal"
	"github.com/myelnet/go-hop-exchange/supply"
	"github.com/myelnet/go-hop-exchange/wallet"
)

var _ exchange.SessionExchange = (*Exchange)(nil)

// RequestTopic listens for peers looking for content blocks
const RequestTopic = "/myel/hop/request/1.0"

// NewExchange creates a Hop exchange struct
func NewExchange(ctx context.Context, options ...func(*Exchange) error) (*Exchange, error) {
	var err error
	ex := &Exchange{}

	// For ease of customizing all the exchange components
	for _, option := range options {
		err := option(ex)
		if err != nil {
			return nil, err
		}
	}
	// Start our lotus api if we have an endpoint.
	// TODO: add a Type to fEndpoint so we can config what type of implementation we want
	// to connect to. Should be fine for now.
	if ex.fEndpoint != nil {
		ex.fAPI, err = filecoin.NewLotusRPC(ctx, ex.fEndpoint.Address, ex.fEndpoint.Header)
		if err != nil {
			return nil, err
		}
	}
	// Set wallet from IPFS Keystore, we should make this more generic eventually
	ex.wallet = wallet.NewIPFS(ex.Keystore, ex.fAPI)
	// Make a new default key to be sure we have an address where to receive our payments
	ex.SelfAddress, err = ex.wallet.NewKey(ctx, wallet.KTSecp256k1)
	if err != nil {
		return nil, err
	}
	// Setup the messaging protocol for communicating retrieval deals
	ex.net = NewFromLibp2pHost(ex.Host)

	// Retrieval data transfer setup
	ex.dataTransfer, err = NewDataTransfer(ctx, ex.Host, ex.GraphSync, ex.Datastore, "retrieval", ex.cidListDir)
	if err != nil {
		return nil, err
	}
	ex.multiStore, err = multistore.NewMultiDstore(ex.Datastore)
	if err != nil {
		return nil, err
	}
	// Add a special adaptor to use the blockstore with cbor encoding
	cborblocks := cbor.NewCborStore(ex.Blockstore)
	// Create our payment manager
	paym := payments.New(ctx, ex.fAPI, ex.wallet, ex.Datastore, cborblocks)
	// Create our retrieval manager
	ex.Retrieval, err = retrieval.New(ctx, ex.multiStore, ex.Datastore, paym, ex.dataTransfer, ex.Host.ID())
	if err != nil {
		return nil, err
	}
	// TODO: this is an empty validator for now
	ex.dataTransfer.RegisterVoucherType(&StorageDataTransferVoucher{}, &UnifiedRequestValidator{})

	// Gossip sub subscription for incoming content queries
	topic, err := ex.PubSub.Join(RequestTopic)
	if err != nil {
		return nil, err
	}
	ex.reqTopic = topic

	sub, err := topic.Subscribe()
	if err != nil {
		return nil, err
	}
	ex.reqSub = sub

	go ex.requestLoop(ctx)

	// TODO: validate AddRequest
	ex.dataTransfer.RegisterVoucherType(&supply.AddRequest{}, &UnifiedRequestValidator{})

	ex.supply = supply.New(ctx, ex.Host, ex.dataTransfer)

	return ex, nil
}

// Exchange is a gossip based exchange for retrieving blocks from Filecoin
type Exchange struct {
	// TODO: prob should move these to an Option struct and select what we need
	Datastore   datastore.Batching
	Blockstore  blockstore.Blockstore
	SelfAddress address.Address
	Host        host.Host
	PubSub      *pubsub.PubSub
	GraphSync   graphsync.GraphExchange
	Pinner      pin.Pinner
	Keystore    wallet.Keystore
	Retrieval   retrieval.Manager

	multiStore   *multistore.MultiStore
	supply       supply.Manager
	provTopic    *pubsub.Topic
	reqSub       *pubsub.Subscription
	reqTopic     *pubsub.Topic
	net          RetrievalMarketNetwork
	dataTransfer datatransfer.Manager
	wallet       wallet.Driver
	cidListDir   string
	// filecoin api
	fAPI      filecoin.API
	fEndpoint *filecoin.APIEndpoint // optional
}

// GetBlock gets a single block from a blocks channel
func (e *Exchange) GetBlock(p context.Context, k cid.Cid) (blocks.Block, error) {
	ctx, cancel := context.WithCancel(p)
	defer cancel()

	promise, err := e.GetBlocks(ctx, []cid.Cid{k})
	if err != nil {
		return nil, err
	}
	select {
	case block, ok := <-promise:
		if !ok {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
				return nil, fmt.Errorf("promise channel was closed")
			}
		}
		return block, nil
	case <-p.Done():
		return nil, p.Err()
	}
}

// GetBlocks creates a new session before getting a stream of blocks
func (e *Exchange) GetBlocks(ctx context.Context, keys []cid.Cid) (<-chan blocks.Block, error) {
	session, err := e.Session(ctx, keys[0])
	if err != nil {
		return nil, err
	}
	return session.GetBlocks(ctx, keys)
}

// HasBlock to stay consistent with Bitswap anounces a new block to our peers
// the name is a bit ambiguous, not to be confused with checking if a block is cached locally
func (e *Exchange) HasBlock(bl blocks.Block) error {
	return e.Announce(bl.Cid())
}

// Announce new content to the network
func (e *Exchange) Announce(c cid.Cid) error {
	size, err := e.Blockstore.GetSize(c)
	if err != nil {
		return err
	}
	return e.supply.SendAddRequest(c, uint64(size))
}

// IsOnline just to respect the exchange interface
func (e *Exchange) IsOnline() bool {
	return true
}

// NewSession creates a Hop session for streaming blocks
func (e *Exchange) NewSession(ctx context.Context) exchange.Fetcher {
	session, _ := e.Session(ctx, cid.Undef)
	return session
}

// Session returns a new sync session
func (e *Exchange) Session(ctx context.Context, root cid.Cid) (*Session, error) {
	// Track when the session is completed
	done := make(chan error)
	// Subscribe to client events to send to the channel
	cl := e.Retrieval.Client()
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
		blockstore: e.Blockstore,
		reqTopic:   e.reqTopic,
		net:        e.net,
		root:       root,
		retriever:  cl,
		addr:       e.SelfAddress,
		ctx:        ctx,
		done:       done,
		unsub:      unsubscribe,
		responses:  make(map[peer.ID]QueryResponse),
		res:        make(chan peer.ID),
	}
	err := e.net.SetDelegate(session)
	if err != nil {
		return nil, err
	}
	return session, nil
}

// Retrieve creates a new session and calls retrieve on specified root cid
// you do need an address to pay retrieval to
// TODO: improve this method is not super useful as is
func (e *Exchange) Retrieve(ctx context.Context, root cid.Cid, peerID peer.ID, addr address.Address) error {
	session, err := e.Session(ctx, root)
	if err != nil {
		return err
	}
	return session.Retrieve(ctx, peerID, addr)
}

// Close the Hop exchange
// TODO: shutdown gracefully
func (e *Exchange) Close() error {
	// e.fAPI.Close()
	// e.dataTransfer.Stop(context.TODO())
	// e.Host.Close()
	return nil
}

// requestLoop runs by default in the background when the Hop client is initialized
// it iterates over new gossip messages and sends a response if we have the block in store
func (e *Exchange) requestLoop(ctx context.Context) {
	for {
		msg, err := e.reqSub.Next(ctx)
		if err != nil {
			return
		}
		if msg.ReceivedFrom == e.Host.ID() {
			continue
		}
		m := new(Query)
		if err := m.UnmarshalCBOR(bytes.NewReader(msg.Data)); err != nil {
			continue
		}
		// GetSize is both a way of checking if we have the block and returning its size
		size, err := e.Blockstore.GetSize(m.PayloadCID)
		// We don't have the block we don't even reply to avoid taking bandwidth
		// On the client side we assume no response means they don't have it
		if err == nil && size > 0 {
			qs, err := e.net.NewQueryStream(msg.ReceivedFrom)
			if err != nil {
				fmt.Println("Error", err)
				continue
			}
			e.sendQueryResponse(qs, QueryResponseAvailable, uint64(size))
		}
	}
}

func (e *Exchange) sendQueryResponse(stream RetrievalQueryStream, status QueryResponseStatus, size uint64) {
	ask := &Ask{
		PricePerByte:            DefaultPricePerByte,
		PaymentInterval:         DefaultPaymentInterval,
		PaymentIntervalIncrease: DefaultPaymentIntervalIncrease,
	}

	answer := QueryResponse{
		Status:                     status,
		Size:                       size,
		PaymentAddress:             e.SelfAddress,
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

// NewDataTransfer packages together all the things needed for a new manager to work
func NewDataTransfer(ctx context.Context, h host.Host, gs graphsync.GraphExchange, ds datastore.Batching, dsprefix string, dir string) (datatransfer.Manager, error) {
	// Create a special key for persisting the datatransfer manager state
	dtDs := namespace.Wrap(ds, datastore.NewKey(dsprefix+"-datatransfer"))
	// Setup datatransfer network
	dtNet := dtnet.NewFromLibp2pHost(h)
	// Setup graphsync transport
	tp := gstransport.NewTransport(h.ID(), gs)
	// Make a special key for stored counter
	key := datastore.NewKey(dsprefix + "-counter")
	// persist ids for new transfers
	storedCounter := storedcounter.New(ds, key)
	// Build the manager
	dt, err := dtfimpl.NewDataTransfer(dtDs, dir, dtNet, tp, storedCounter)
	if err != nil {
		return nil, err
	}
	ready := make(chan error, 1)
	dt.OnReady(func(err error) {
		ready <- err
	})
	dt.Start(ctx)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-ready:
		return dt, err
	}
}

// DataTransfer gives access to the datatransfer manager instance powering all the transfers for the exchange
func (e *Exchange) DataTransfer() datatransfer.Manager {
	return e.dataTransfer
}

// Supply exposes the supply manager
func (e *Exchange) Supply() supply.Manager {
	return e.supply
}
