package retrieval

import (
	"context"

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

	"github.com/myelnet/go-hop-exchange/retrieval/client"
	"github.com/myelnet/go-hop-exchange/retrieval/deal"
	"github.com/myelnet/go-hop-exchange/retrieval/provider"
)

// Unsubscribe is a function that unsubscribes a subscriber for either the
// client or the provider
type Unsubscribe func()

// Manager handles all retrieval operations both as client and provider
type Manager interface {
	SubscribeToProviderEvents(subscriber provider.Subscriber) Unsubscribe
	Retrieve(
		ctx context.Context,
		root cid.Cid,
		params deal.Params,
		totalFunds abi.TokenAmount,
		sender peer.ID,
		client address.Address,
		provider address.Address,
		storeID *multistore.StoreID,
	) (deal.ID, error)
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
	multiStore    *multistore.MultiStore
	dataTransfer  datatransfer.Manager
	stateMachines fsm.Group
	subscribers   *pubsub.PubSub
}

func (p *Provider) notifySubscribers(eventName fsm.EventName, state fsm.StateType) {
	evt := eventName.(provider.Event)
	ds := state.(deal.ProviderState)
	_ = p.subscribers.Publish(provider.InternalEvent{
		Evt:   evt,
		State: ds,
	})
}

// New creates a new retrieval instance
func New(
	ms *multistore.MultiStore,
	dt datatransfer.Manager,
	ds datastore.Batching,
	sc *storedcounter.StoredCounter,
) (Manager, error) {
	var err error
	c := &Client{
		multiStore:   ms,
		dataTransfer: dt,
		subscribers:  pubsub.New(client.Dispatcher),
		counter:      sc,
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
	if err := dt.RegisterVoucherResultType(&deal.Response{}); err != nil {
		return nil, err
	}
	if err := dt.RegisterVoucherType(&deal.Proposal{}, nil); err != nil {
		return nil, err
	}
	if err := dt.RegisterVoucherType(&deal.Payment{}, nil); err != nil {
		return nil, err
	}
	p := &Provider{
		multiStore:   ms,
		dataTransfer: dt,
		subscribers:  pubsub.New(provider.Dispatcher),
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
	r := &Retrieval{
		client:   c,
		provider: p,
	}
	return r, nil
}

// SubscribeToProviderEvents to listen to transfer state changes on the provider side
func (r *Retrieval) SubscribeToProviderEvents(subscriber provider.Subscriber) Unsubscribe {
	return Unsubscribe(r.provider.subscribers.Subscribe(subscriber))
}

// Retrieve content
func (r *Retrieval) Retrieve(
	ctx context.Context,
	root cid.Cid,
	params deal.Params,
	totalFunds abi.TokenAmount,
	sender peer.ID,
	clientAddr address.Address,
	providerAddr address.Address,
	storeID *multistore.StoreID,
) (deal.ID, error) {
	next, err := r.client.counter.Next()
	if err != nil {
		return 0, err
	}

	// make sure the store is loadable
	if storeID != nil {
		_, err = r.client.multiStore.Get(*storeID)
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
	err = r.client.stateMachines.Begin(dealState.ID, &dealState)
	if err != nil {
		return 0, err
	}

	err = r.client.stateMachines.Send(dealState.ID, client.EventOpen)
	if err != nil {
		return 0, err
	}

	return dealState.ID, nil
}
