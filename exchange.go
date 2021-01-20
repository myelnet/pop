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
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-storedcounter"
	blocks "github.com/ipfs/go-block-format"
	cid "github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
	"github.com/ipfs/go-graphsync"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	exchange "github.com/ipfs/go-ipfs-exchange-interface"
	pin "github.com/ipfs/go-ipfs-pinner"
	"github.com/libp2p/go-libp2p-core/host"
	peer "github.com/libp2p/go-libp2p-core/peer"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/myelnet/go-hop-exchange/filecoin"
	"github.com/myelnet/go-hop-exchange/supply"
	"github.com/myelnet/go-hop-exchange/wallet"
)

var _ exchange.SessionExchange = (*Exchange)(nil)

// RequestTopic listens for peers looking for content blocks
const RequestTopic = "/myel/hop/request/1.0"

// DefaultPricePerByte is the charge per byte retrieved if the miner does
// not specifically set it
var DefaultPricePerByte = abi.NewTokenAmount(2)

// DefaultPaymentInterval is the baseline interval, set to 1Mb
// if the miner does not explicitly set it otherwise
var DefaultPaymentInterval = uint64(1 << 20)

// DefaultPaymentIntervalIncrease is the amount interval increases on each payment,
// set to to 1Mb if the miner does not explicitly set it otherwise
var DefaultPaymentIntervalIncrease = uint64(1 << 20)

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
	// Start our lotus api.
	// TODO: add a Type to fEndpoint so we can config what type of implementation we want
	// to connect to. Should be fine for now.
	ex.fAPI, err = filecoin.NewLotusRPC(ctx, ex.fEndpoint.Address, ex.fEndpoint.Header)
	if err != nil {
		return nil, err
	}
	// Setup the messaging protocol for communicating retrieval deals
	ex.net = NewFromLibp2pHost(ex.Host)

	// Retrieval data transfer setup
	ex.dataTransfer, err = NewDataTransfer(ex.Host, ex.GraphSync, ex.Datastore, "retrieval", ex.cidListDir)
	err = ex.dataTransfer.Start(ctx)
	if err != nil {
		return nil, err
	}
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

	// Setup a separate data transfer instance for supplying new blocks to serve
	// TODO: not sure if it would be better to reuse the same data transfer manager but it seems
	// safer to separate the instances in case our supply breaks we can still serve blocks or the other
	// way around. It is probably better to create isolated store instances for supply and retrieval
	// transactions. Will improve when I have more evidence.
	sdataTransfer, err := NewDataTransfer(ex.Host, ex.GraphSync, ex.Datastore, "supply", ex.cidListDir)
	if err != nil {
		return nil, err
	}
	// TODO: validate AddRequest
	sdataTransfer.RegisterVoucherType(&supply.AddRequest{}, &UnifiedRequestValidator{})

	ex.supply = supply.New(ctx, ex.Host, sdataTransfer)

	return ex, nil
}

// Exchange is a gossip based exchange for retrieving blocks from Filecoin
type Exchange struct {
	Datastore   datastore.Batching
	Blockstore  blockstore.Blockstore
	SelfAddress address.Address
	Host        host.Host
	PubSub      *pubsub.PubSub
	GraphSync   graphsync.GraphExchange
	Pinner      pin.Pinner

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
	fEndpoint filecoin.APIEndpoint
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
	session := &Session{
		blockstore:   e.Blockstore,
		reqTopic:     e.reqTopic,
		net:          e.net,
		dataTransfer: e.dataTransfer,
		responses:    make(map[peer.ID]QueryResponse),
		res:          make(chan peer.ID),
	}
	e.net.SetDelegate(session)
	return session.GetBlocks(ctx, keys)
}

// HasBlock to stay consistent with Bitswap anounces a new block to our peers
// the name is a bit ambiguous, not to be confused with checking if a block is cached locally
func (e *Exchange) HasBlock(bl blocks.Block) error {
	return e.Announce(context.Background(), bl.Cid())
}

// Announce new content to the network
func (e *Exchange) Announce(ctx context.Context, c cid.Cid) error {
	size, err := e.Blockstore.GetSize(c)
	if err != nil {
		return err
	}
	return e.supply.SendAddRequest(ctx, c, uint64(size))
}

// IsOnline just to respect the exchange interface
func (e *Exchange) IsOnline() bool {
	return true
}

// NewSession creates a Hop session for streaming blocks
func (e *Exchange) NewSession(ctx context.Context) exchange.Fetcher {
	return &Session{
		blockstore:   e.Blockstore,
		reqTopic:     e.reqTopic,
		net:          e.net,
		dataTransfer: e.dataTransfer,
		responses:    make(map[peer.ID]QueryResponse),
		res:          make(chan peer.ID),
	}
}

// Retrieve creates a new session and calls retrieve on specified root cid
func (e *Exchange) Retrieve(ctx context.Context, root cid.Cid, peerID peer.ID) error {
	session := &Session{
		blockstore:   e.Blockstore,
		reqTopic:     e.reqTopic,
		net:          e.net,
		dataTransfer: e.dataTransfer,
		responses:    make(map[peer.ID]QueryResponse),
		res:          make(chan peer.ID),
	}
	return session.Retrieve(ctx, root, peerID)
}

// Close the Hop exchange
func (e *Exchange) Close() error {
	e.fAPI.Close()
	return nil
}

// requestLoop runs by default in the background when the Hop client is initialized
// it iterates over new gossip messages and sends a response if we have the block in store
func (e *Exchange) requestLoop(ctx context.Context) {
	fmt.Println("waiting for requests")
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

// NewDataTransfer packages together all the things needed for a new manager to work
func NewDataTransfer(h host.Host, gs graphsync.GraphExchange, ds datastore.Batching, dsprefix string, dir string) (datatransfer.Manager, error) {
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
	return dtfimpl.NewDataTransfer(dtDs, dir, dtNet, tp, storedCounter)
}
