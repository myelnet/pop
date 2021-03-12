package retrieval

import (
	"context"
	"fmt"

	"github.com/filecoin-project/go-address"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-multistore"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/go-statemachine/fsm"
	"github.com/filecoin-project/go-storedcounter"
	"github.com/hannahhoward/go-pubsub"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
	peer "github.com/libp2p/go-libp2p-peer"

	"github.com/myelnet/pop/payments"
	"github.com/myelnet/pop/retrieval/client"
	"github.com/myelnet/pop/retrieval/deal"
	"github.com/myelnet/pop/retrieval/provider"
)

// Unsubscribe is a function that unsubscribes a subscriber for either the
// client or the provider
type Unsubscribe func()

// Manager handles all retrieval operations both as client and provider
type Manager interface {
	Client() *Client
	Provider() *Provider
}

// StoreIDGetter is an interface required for finding the store associated with the content to provide
type StoreIDGetter interface {
	GetStoreID(cid.Cid) (multistore.StoreID, error)
}

// Retrieval manager implementation
type Retrieval struct {
	c *Client
	p *Provider
}

// Client to access our Retriever implementation
func (r *Retrieval) Client() *Client {
	return r.c
}

// Provider to access our Provider implementation
func (r *Retrieval) Provider() *Provider {
	return r.p
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
	askStore         *AskStore
	storeIDGetter    StoreIDGetter
}

// GetAsk returns the current deal parameters this provider accepts for a given peer
func (p *Provider) GetAsk(k peer.ID) deal.QueryResponse {
	return p.askStore.GetAsk(k)
}

// SetAsk sets the deal parameters this provider accepts
func (p *Provider) SetAsk(k peer.ID, ask deal.QueryResponse) {
	err := p.askStore.SetAsk(k, ask)

	if err != nil {
		fmt.Printf("Error setting retrieval ask: %v\v", err)
	}
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

// New creates a new retrieval instance
func New(
	ctx context.Context,
	ms *multistore.MultiStore,
	ds datastore.Batching,
	pay payments.Manager,
	dt datatransfer.Manager,
	sg StoreIDGetter,
	self peer.ID,
) (Manager, error) {
	sc := storedcounter.New(ds, datastore.NewKey("/retrieval/deal-id"))
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
	p := &Provider{
		multiStore:   ms,
		subscribers:  pubsub.New(provider.Dispatcher),
		dataTransfer: dt,
		pay:          pay,
		askStore: &AskStore{
			asks: make(map[peer.ID]deal.QueryResponse),
		},
		storeIDGetter: sg,
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

	err = dt.RegisterVoucherResultType(&deal.Response{})
	if err != nil {
		return nil, err
	}
	err = dt.RegisterVoucherType(&deal.Proposal{}, p.requestValidator)
	if err != nil {
		return nil, err
	}
	err = c.dataTransfer.RegisterVoucherType(&deal.Payment{}, nil)
	if err != nil {
		return nil, err
	}
	err = dt.RegisterRevalidator(&deal.Payment{}, p.revalidator)
	if err != nil {
		return nil, err
	}
	tconfig := TransportConfigurer(self, &dualStoreGetter{c, p})
	err = dt.RegisterTransportConfigurer(&deal.Proposal{}, tconfig)
	if err != nil {
		return nil, err
	}
	dt.SubscribeToEvents(provider.DataTransferSubscriber(p.stateMachines))
	dt.SubscribeToEvents(client.DataTransferSubscriber(c.stateMachines))

	return &Retrieval{c, p}, nil
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

// TryRestartInsufficientFunds attempts to restart any deals stuck in the insufficient funds state
// after funds are added to a given payment channel
func (c *Client) TryRestartInsufficientFunds(chAddr address.Address) error {
	var deals []deal.ClientState
	err := c.stateMachines.List(&deals)
	if err != nil {
		return err
	}
	for _, d := range deals {
		if d.Status == deal.StatusInsufficientFunds && d.PaymentInfo.PayCh == chAddr {
			if err := c.stateMachines.Send(d.ID, client.EventRecheckFunds); err != nil {
				return err
			}
		}
	}
	return nil
}
