package retrieval

import (
	"context"
	"fmt"

	"github.com/filecoin-project/go-address"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	dtimpl "github.com/filecoin-project/go-data-transfer/impl"
	dtnet "github.com/filecoin-project/go-data-transfer/network"
	dtgstp "github.com/filecoin-project/go-data-transfer/transport/graphsync"
	"github.com/filecoin-project/go-multistore"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/go-statemachine/fsm"
	"github.com/filecoin-project/go-storedcounter"
	"github.com/hannahhoward/go-pubsub"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
	"github.com/ipfs/go-graphsync"
	"github.com/libp2p/go-libp2p-core/host"
	peer "github.com/libp2p/go-libp2p-peer"

	"github.com/myelnet/go-hop-exchange/payments"
	"github.com/myelnet/go-hop-exchange/retrieval/client"
	"github.com/myelnet/go-hop-exchange/retrieval/deal"
	"github.com/myelnet/go-hop-exchange/retrieval/provider"
)

// Unsubscribe is a function that unsubscribes a subscriber for either the
// client or the provider
type Unsubscribe func()

// Manager handles all retrieval operations both as client and provider
type Manager interface {
}

// Retrieval manager implementation
type Retrieval struct {
	client   *Client
	provider *Provider
}

// Client wraps all the client operations
type Client struct {
	multiStore    *multistore.MultiStore
	dataTransfer  datatransfer.Manager
	stateMachines fsm.Group
	subscribers   *pubsub.PubSub
	counter       *storedcounter.StoredCounter
	pay           payments.Manager
}

// NewClient setup a retrieval client
func NewClient(
	ctx context.Context,
	ms *multistore.MultiStore,
	ds datastore.Batching,
	sc *storedcounter.StoredCounter,
	dt datatransfer.Manager,
	pay payments.Manager,
	pid peer.ID,
) (*Client, error) {
	var err error
	// Client setup
	c := &Client{
		multiStore:   ms,
		subscribers:  pubsub.New(client.Dispatcher),
		counter:      sc,
		dataTransfer: dt,
		pay:          pay,
	}
	c.stateMachines, err = fsm.New(namespace.Wrap(ds, datastore.NewKey("client-v0")), fsm.Parameters{
		Environment:     &clientDealEnvironment{c},
		StateType:       deal.ClientState{},
		StateKeyField:   "Status",
		Events:          client.FSMEvents,
		StateEntryFuncs: client.StateEntryFuncs,
		FinalityStates:  client.FinalityStates,
		Notifier:        c.notifySubscribers,
	})
	if err != nil {
		return nil, err
	}
	err = c.dataTransfer.RegisterVoucherResultType(&deal.Response{})
	if err != nil {
		return nil, err
	}
	err = c.dataTransfer.RegisterVoucherType(&deal.Proposal{}, nil)
	if err != nil {
		return nil, err
	}
	err = c.dataTransfer.RegisterVoucherType(&deal.Payment{}, nil)
	if err != nil {
		return nil, err
	}
	c.dataTransfer.SubscribeToEvents(client.DataTransferSubscriber(c.stateMachines))
	// err = c.dataTransfer.RegisterTransportConfigurer(
	// 	&deal.Proposal{},
	// 	TransportConfigurer(pid, &clientStoreGetter{c}),
	// )
	// if err != nil {
	// 	return nil, err
	// }
	return c, nil
}

func (c *Client) notifySubscribers(eventName fsm.EventName, state fsm.StateType) {
	evt := eventName.(client.Event)
	ds := state.(deal.ClientState)
	_ = c.subscribers.Publish(client.InternalEvent{
		Evt:   evt,
		State: ds,
	})
}

// Provider wraps all the provider operations
type Provider struct {
	multiStore       *multistore.MultiStore
	dataTransfer     datatransfer.Manager
	stateMachines    fsm.Group
	subscribers      *pubsub.PubSub
	requestValidator *ProviderRequestValidator
	revalidator      *ProviderRevalidator
	pay              payments.Manager
}

// NewProvider creates a new retrieval provider
func NewProvider(
	ctx context.Context,
	ms *multistore.MultiStore,
	ds datastore.Batching,
	dt datatransfer.Manager,
	pay payments.Manager,
	pid peer.ID,
) (*Provider, error) {
	var err error
	p := &Provider{
		multiStore:   ms,
		subscribers:  pubsub.New(provider.Dispatcher),
		dataTransfer: dt,
		pay:          pay,
	}
	p.stateMachines, err = fsm.New(namespace.Wrap(ds, datastore.NewKey("provider-v0")), fsm.Parameters{
		Environment:     &providerDealEnvironment{p},
		StateType:       deal.ProviderState{},
		StateKeyField:   "Status",
		Events:          provider.FSMEvents,
		StateEntryFuncs: provider.StateEntryFuncs,
		FinalityStates:  provider.FinalityStates,
		Notifier:        p.notifySubscribers,
	})
	if err != nil {
		return nil, err
	}

	p.requestValidator = NewProviderRequestValidator(&providerValidationEnvironment{p})

	p.revalidator = NewProviderRevalidator(&providerRevalidatorEnvironment{p})

	err = p.dataTransfer.RegisterVoucherResultType(&deal.Response{})
	if err != nil {
		return nil, err
	}
	err = p.dataTransfer.RegisterVoucherType(&deal.Proposal{}, p.requestValidator)
	if err != nil {
		return nil, err
	}
	err = p.dataTransfer.RegisterRevalidator(&deal.Payment{}, p.revalidator)
	if err != nil {
		return nil, err
	}
	p.dataTransfer.SubscribeToEvents(provider.DataTransferSubscriber(p.stateMachines))
	// err = p.dataTransfer.RegisterTransportConfigurer(
	// 	&deal.Proposal{},
	// 	TransportConfigurer(pid, &providerStoreGetter{p}),
	// )
	// if err != nil {
	// 	return nil, err
	// }
	return p, nil
}

func (p *Provider) notifySubscribers(eventName fsm.EventName, state fsm.StateType) {
	evt := eventName.(provider.Event)
	ds := state.(deal.ProviderState)
	_ = p.subscribers.Publish(provider.InternalEvent{
		Evt:   evt,
		State: ds,
	})
}

// SubscribeToEvents to listen to transfer state changes on the provider side
func (p *Provider) SubscribeToEvents(subscriber provider.Subscriber) Unsubscribe {
	return Unsubscribe(p.subscribers.Subscribe(subscriber))
}

// Transporter exposes methods to access the underlying transport primitives
type Transporter interface {
	NewDataTransfer(context.Context, string) (datatransfer.Manager, error)
	PeerID() peer.ID
}

// Transport is the components necessary for retrieving content over the network
type Transport struct {
	Host      host.Host
	GraphSync graphsync.GraphExchange
	Datastore datastore.Batching
	DirPath   string
	Client    bool
}

// NewDataTransfer creates a new data transfer instance on a shared transport
func (t *Transport) NewDataTransfer(ctx context.Context, name string) (datatransfer.Manager, error) {

	sc := storedcounter.New(t.Datastore, datastore.NewKey(fmt.Sprintf("/datatransfer/%s/counter", name)))

	net := dtnet.NewFromLibp2pHost(t.Host)

	dtDs := namespace.Wrap(t.Datastore, datastore.NewKey(fmt.Sprintf("/datatransfer/%s/transfers", name)))
	transport := dtgstp.NewTransport(t.Host.ID(), t.GraphSync)

	dt, err := dtimpl.NewDataTransfer(dtDs, t.DirPath, net, transport, sc)
	if err != nil {
		return nil, err
	}
	ready := make(chan error, 1)
	dt.OnReady(func(err error) {
		ready <- err
	})
	err = dt.Start(ctx)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-ready:
		return dt, err
	}
}

// PeerID returns the peer id of the current host
func (t *Transport) PeerID() peer.ID {
	return t.Host.ID()
}

// New creates a new retrieval instance
func New(
	ctx context.Context,
	ms *multistore.MultiStore,
	ds datastore.Batching,
	sc *storedcounter.StoredCounter,
	pay payments.Manager,
	tp Transporter,
) (*Client, *Provider, error) {

	dt, err := tp.NewDataTransfer(ctx, "client")
	if err != nil {
		return nil, nil, err
	}
	c, err := NewClient(ctx, ms, ds, sc, dt, pay, tp.PeerID())
	if err != nil {
		return nil, nil, err
	}

	dt, err = tp.NewDataTransfer(ctx, "provider")
	if err != nil {
		return nil, nil, err
	}
	p, err := NewProvider(ctx, ms, ds, dt, pay, tp.PeerID())
	if err != nil {
		return nil, nil, err
	}

	return c, p, nil
}

// Retrieve content
func (c *Client) Retrieve(
	ctx context.Context,
	root cid.Cid,
	params deal.Params,
	totalFunds abi.TokenAmount,
	sender peer.ID,
	clientAddr address.Address,
	providerAddr address.Address,
	storeID *multistore.StoreID,
) (deal.ID, error) {
	next, err := c.counter.Next()
	if err != nil {
		return 0, err
	}

	// make sure the store is loadable
	if storeID != nil {
		_, err = c.multiStore.Get(*storeID)
		if err != nil {
			return 0, err
		}
	}

	dealState := deal.ClientState{
		Proposal: deal.Proposal{
			PayloadCID: root,
			ID:         deal.ID(next),
			Params:     params,
		},
		TotalFunds:       totalFunds,
		ClientWallet:     clientAddr,
		MinerWallet:      providerAddr,
		TotalReceived:    0,
		CurrentInterval:  params.PaymentInterval,
		BytesPaidFor:     0,
		PaymentRequested: abi.NewTokenAmount(0),
		FundsSpent:       abi.NewTokenAmount(0),
		Status:           deal.StatusNew,
		Sender:           sender,
		UnsealFundsPaid:  big.Zero(),
		StoreID:          storeID,
	}

	// start the deal processing
	err = c.stateMachines.Begin(dealState.ID, &dealState)
	if err != nil {
		return 0, err
	}

	err = c.stateMachines.Send(dealState.ID, client.EventOpen)
	if err != nil {
		return 0, err
	}

	return dealState.ID, nil
}

// SubscribeToEvents to listen to transfer state changes on the client side
func (c *Client) SubscribeToEvents(subscriber client.Subscriber) Unsubscribe {
	return Unsubscribe(c.subscribers.Subscribe(subscriber))
}
