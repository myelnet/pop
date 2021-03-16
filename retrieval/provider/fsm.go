package provider

import (
	"context"
	"fmt"

	"github.com/filecoin-project/go-address"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-multistore"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/go-statemachine"
	"github.com/filecoin-project/go-statemachine/fsm"
	"github.com/hannahhoward/go-pubsub"

	"github.com/myelnet/pop/retrieval/deal"
)

// InternalEvent wraps a provider event and the associated deal state
type InternalEvent struct {
	Evt   Event
	State deal.ProviderState
}

// Subscriber is a callback that is registered to listen for retrieval events on a provider
type Subscriber func(event Event, state deal.ProviderState)

// Dispatcher will publish a pubsub event cast as Provider event
func Dispatcher(evt pubsub.Event, subscriberFn pubsub.SubscriberFn) error {
	ie, ok := evt.(InternalEvent)
	if !ok {
		return fmt.Errorf("wrong type of event")
	}
	cb, ok := subscriberFn.(Subscriber)
	if !ok {
		return fmt.Errorf("wrong type of event")
	}
	cb(ie.Evt, ie.State)
	return nil
}

func recordError(ds *deal.ProviderState, err error) error {
	ds.Message = err.Error()
	return nil
}

// FSMEvents is the state chart defining the events that can happen in a retrieval provider
// it is almost identical to go-fil-markets except we don't have to unseal pieces
var FSMEvents = fsm.Events{
	// receiving new deal
	fsm.Event(EventOpen).
		From(deal.StatusNew).ToNoChange().
		Action(
			func(ds *deal.ProviderState) error {
				ds.TotalSent = 0
				ds.FundsReceived = abi.NewTokenAmount(0)
				ds.CurrentInterval = ds.PaymentInterval
				return nil
			},
		),

	// accepting
	fsm.Event(EventDealAccepted).
		From(deal.StatusNew).To(deal.StatusOngoing).
		Action(func(ds *deal.ProviderState, channelID datatransfer.ChannelID) error {
			ds.ChannelID = channelID
			return nil
		}),

	// sending blocks
	fsm.Event(EventBlockSent).
		FromMany(deal.StatusOngoing).ToNoChange().
		Action(func(ds *deal.ProviderState, totalSent uint64) error {
			ds.TotalSent = totalSent
			return nil
		}),
	fsm.Event(EventBlocksCompleted).
		FromMany(deal.StatusOngoing).To(deal.StatusBlocksComplete),

	// request payment
	fsm.Event(EventPaymentRequested).
		From(deal.StatusOngoing).To(deal.StatusFundsNeeded).
		From(deal.StatusBlocksComplete).To(deal.StatusFundsNeededLastPayment).
		Action(func(ds *deal.ProviderState, totalSent uint64) error {
			ds.TotalSent = totalSent
			return nil
		}),

	// receive and process payment
	fsm.Event(EventSaveVoucherFailed).
		FromMany(deal.StatusFundsNeeded, deal.StatusFundsNeededLastPayment).To(deal.StatusFailing).
		Action(recordError),
	fsm.Event(EventPartialPaymentReceived).
		FromMany(deal.StatusFundsNeeded, deal.StatusFundsNeededLastPayment).ToNoChange().
		Action(func(ds *deal.ProviderState, fundsReceived abi.TokenAmount, ch address.Address) error {
			ds.FundsReceived = big.Add(ds.FundsReceived, fundsReceived)
			ds.PayCh = &ch
			return nil
		}),
	fsm.Event(EventPaymentReceived).
		From(deal.StatusFundsNeeded).To(deal.StatusOngoing).
		From(deal.StatusFundsNeededLastPayment).To(deal.StatusFinalizing).
		Action(func(ds *deal.ProviderState, fundsReceived abi.TokenAmount, ch address.Address) error {
			ds.FundsReceived = big.Add(ds.FundsReceived, fundsReceived)
			ds.CurrentInterval += ds.PaymentIntervalIncrease
			ds.PayCh = &ch
			return nil
		}),

	// completing
	fsm.Event(EventComplete).FromMany(deal.StatusBlocksComplete, deal.StatusFinalizing).To(deal.StatusCompleting),
	fsm.Event(EventCleanupComplete).From(deal.StatusCompleting).To(deal.StatusCompleted),

	// Cancellation / Error cleanup
	fsm.Event(EventCancelComplete).
		From(deal.StatusCancelling).To(deal.StatusCancelled).
		From(deal.StatusFailing).To(deal.StatusErrored),

	// data transfer errors
	fsm.Event(EventDataTransferError).
		FromAny().To(deal.StatusErrored).
		Action(recordError),

	// multistore errors
	fsm.Event(EventMultiStoreError).
		FromAny().To(deal.StatusErrored).
		Action(recordError),

	fsm.Event(EventClientCancelled).
		From(deal.StatusFailing).ToJustRecord().
		From(deal.StatusCancelling).ToJustRecord().
		FromAny().To(deal.StatusCancelling).Action(
		func(ds *deal.ProviderState) error {
			if ds.Status != deal.StatusFailing {
				ds.Message = "Client cancelled retrieval"
			}
			return nil
		},
	),
}

// StateEntryFuncs are the handlers for different states in a retrieval provider
var StateEntryFuncs = fsm.StateEntryFuncs{
	deal.StatusOngoing:    TrackTransfer,
	deal.StatusFailing:    CancelDeal,
	deal.StatusCancelling: CancelDeal,
	deal.StatusCompleting: CleanupDeal,
}

// FinalityStates are the terminal states for a retrieval provider
var FinalityStates = []fsm.StateKey{
	deal.StatusErrored,
	deal.StatusCompleted,
	deal.StatusCancelled,
}

// DealEnvironment is a bridge to the environment a provider deal is executing in
type DealEnvironment interface {
	TrackTransfer(deal.ProviderState) error
	UntrackTransfer(deal.ProviderState) error
	DeleteStore(multistore.StoreID) error
	ResumeDataTransfer(context.Context, datatransfer.ChannelID) error
	CloseDataTransfer(context.Context, datatransfer.ChannelID) error
}

// TrackTransfer keeps track of a transfer during revalidation
func TrackTransfer(ctx fsm.Context, environment DealEnvironment, ds deal.ProviderState) error {
	err := environment.TrackTransfer(ds)
	if err != nil {
		return ctx.Trigger(EventDataTransferError, err)
	}
	return nil
}

// CancelDeal clears a deal that went wrong for an unknown reason
func CancelDeal(ctx fsm.Context, environment DealEnvironment, ds deal.ProviderState) error {
	// Read next response (or fail)
	err := environment.UntrackTransfer(ds)
	if err != nil {
		return ctx.Trigger(EventDataTransferError, err)
	}
	err = environment.DeleteStore(ds.StoreID)
	if err != nil {
		return ctx.Trigger(EventMultiStoreError, err)
	}
	err = environment.CloseDataTransfer(ctx.Context(), ds.ChannelID)
	if err != nil && err != statemachine.ErrTerminated {
		return ctx.Trigger(EventDataTransferError, err)
	}
	return ctx.Trigger(EventCancelComplete)
}

// CleanupDeal runs to do memory cleanup for an in progress deal
func CleanupDeal(ctx fsm.Context, environment DealEnvironment, ds deal.ProviderState) error {
	err := environment.UntrackTransfer(ds)
	if err != nil {
		return ctx.Trigger(EventDataTransferError, err)
	}
	err = environment.DeleteStore(ds.StoreID)
	if err != nil {
		return ctx.Trigger(EventMultiStoreError, err)
	}
	return ctx.Trigger(EventCleanupComplete)
}
