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
)

var _ exchange.SessionExchange = (*Exchange)(nil)

// RequestTopic listens for peers looking for content blocks
const RequestTopic = "/myel/hop/request/1.0"

// ProvisionTopic listens for new content added to the network
const ProvisionTopic = "/myel/hop/provision/1.0"

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

	ex := &Exchange{}
	// For ease of customizing all the exchange components
	for _, option := range options {
		err := option(ex)
		if err != nil {
			return nil, err
		}
	}

	ex.net = NewFromLibp2pHost(ex.Host)

	dtDs := namespace.Wrap(ex.Datastore, datastore.NewKey("datatransfer"))
	dtNet := dtnet.NewFromLibp2pHost(ex.Host)
	tp := gstransport.NewTransport(ex.Host.ID(), ex.GraphSync)
	key := datastore.NewKey("counter")
	storedCounter := storedcounter.New(ex.Datastore, key)

	dataTransfer, err := dtfimpl.NewDataTransfer(dtDs, ex.cidListDir, dtNet, tp, storedCounter)
	if err != nil {
		return nil, err
	}
	ex.dataTransfer = dataTransfer
	err = ex.dataTransfer.Start(ctx)
	if err != nil {
		return nil, err
	}
	ex.dataTransfer.RegisterVoucherType(&StorageDataTransferVoucher{}, &UnifiedRequestValidator{})

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

	provTopic    *pubsub.Topic
	reqSub       *pubsub.Subscription
	reqTopic     *pubsub.Topic
	net          RetrievalMarketNetwork
	dataTransfer datatransfer.Manager
	cidListDir   string
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
func (e *Exchange) HasBlock(bl blocks.Block) error {
	return e.Announce(context.Background(), bl.Cid())
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
	return nil
}

// requestLoop runs by default in the background when the Hop client is initialized
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

// StartProvisioning is optional and can be called if the node desires
// to subscribe to new content to server retrieval deals
func (e *Exchange) StartProvisioning(ctx context.Context) error {
	topic, err := e.PubSub.Join(ProvisionTopic)
	if err != nil {
		return err
	}

	sub, err := topic.Subscribe()
	if err != nil {
		return err
	}

	go func() {
		for {
			msg, err := sub.Next(ctx)
			if err != nil {
				return
			}

			if msg.ReceivedFrom == e.Host.ID() {
				continue
			}
			m := new(Provision)
			if err := m.UnmarshalCBOR(bytes.NewReader(msg.Data)); err != nil {
				continue
			}
			// TODO: run custom logic to determine if we want to store this content or not
			err = e.Retrieve(ctx, m.PayloadCID, msg.ReceivedFrom)
			if err != nil {
				// TODO: logging
				continue
			}
			// If we have a Pinner we're probably running in an IPFS node so we need to
			// tell it not to garbage collect those blocks
			if e.Pinner != nil {
				e.Pinner.PinWithMode(m.PayloadCID, pin.Recursive)
			}
		}
	}()
	e.provTopic = topic

	return nil
}

// Announce content to the network to try and retrieve it later faster
// TODO: could require to pass the size to prevent a blockstore read
func (e *Exchange) Announce(ctx context.Context, payload cid.Cid) error {
	size, err := e.Blockstore.GetSize(payload)
	if err != nil {
		return err
	}
	m := Provision{
		PayloadCID: payload,
		Size:       uint64(size),
	}
	buf := new(bytes.Buffer)
	if err := m.MarshalCBOR(buf); err != nil {
		return err
	}
	return e.provTopic.Publish(ctx, buf.Bytes())
}
