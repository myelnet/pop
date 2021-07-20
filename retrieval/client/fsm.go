package client

import (
	"context"
	"fmt"

	"github.com/filecoin-project/go-address"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/go-statemachine/fsm"
	"github.com/hannahhoward/go-pubsub"
	"github.com/ipfs/go-cid"
	peer "github.com/libp2p/go-libp2p-peer"
	"github.com/rs/zerolog/log"

	"github.com/myelnet/pop/payments"
	"github.com/myelnet/pop/retrieval/deal"
)

// Subscriber is a callback that is registered to listen for retrieval events
type Subscriber func(event Event, state deal.ClientState)

// InternalEvent is an atomic state change in the client
type InternalEvent struct {
	Evt   Event
	State deal.ClientState
}

// Dispatcher casts a pubsub event into a provider event and publishes it to a subscriber
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

func recordReceived(deal *deal.ClientState, totalReceived uint64) error {
	deal.TotalReceived = totalReceived
	return nil
}

var paymentChannelCreationStates = []fsm.StateKey{
	deal.StatusWaitForAcceptance,
	deal.StatusAccepted,
	deal.StatusPaymentChannelCreating,
	deal.StatusPaymentChannelAllocatingLane,
	deal.StatusPaymentChannelAddingInitialFunds,
}

// FSMEvents is the state chart defining the events that can happen in a retrieval client
// it is almost identical to go-fil-markets implementation except we don't support legacy events
var FSMEvents = fsm.Events{
	fsm.Event(EventOpen).
		From(deal.StatusNew).ToNoChange(),

	// ProposeDeal handler events
	fsm.Event(EventWriteDealProposalErrored).
		FromAny().To(deal.StatusErrored).
		Action(func(ds *deal.ClientState, err error) error {
			ds.Message = fmt.Errorf("proposing deal: %w", err).Error()
			return nil
		}),
	fsm.Event(EventDealProposed).
		From(deal.StatusNew).To(deal.StatusWaitForAcceptance).
		Action(func(ds *deal.ClientState, channelID datatransfer.ChannelID) error {
			ds.ChannelID = channelID
			ds.Message = ""
			return nil
		}),

	// Initial deal acceptance events
	fsm.Event(EventDealRejected).
		From(deal.StatusWaitForAcceptance).To(deal.StatusRejected).
		Action(func(deal *deal.ClientState, message string) error {
			deal.Message = fmt.Sprintf("deal rejected: %s", message)
			return nil
		}),
	fsm.Event(EventDealNotFound).
		From(deal.StatusWaitForAcceptance).To(deal.StatusDealNotFound).
		Action(func(ds *deal.ClientState, message string) error {
			ds.Message = fmt.Sprintf("deal not found: %s", message)
			return nil
		}),
	fsm.Event(EventDealAccepted).
		From(deal.StatusWaitForAcceptance).To(deal.StatusAccepted),
	fsm.Event(EventUnknownResponseReceived).
		FromAny().To(deal.StatusFailing).
		Action(func(ds *deal.ClientState, status deal.Status) error {
			ds.Message = fmt.Sprintf("Unexpected deal response status: %s", deal.Statuses[status])
			return nil
		}),

	// Payment channel setup
	fsm.Event(EventPaymentChannelSkip).
		From(deal.StatusAccepted).To(deal.StatusOngoing),

	fsm.Event(EventPaymentChannelErrored).
		FromMany(deal.StatusAccepted, deal.StatusPaymentChannelCreating, deal.StatusPaymentChannelAddingFunds).To(deal.StatusFailing).
		Action(func(ds *deal.ClientState, err error) error {
			ds.Message = fmt.Errorf("error from payment channel: %w", err).Error()
			return nil
		}),
	fsm.Event(EventPaymentChannelCreateInitiated).
		From(deal.StatusAccepted).To(deal.StatusPaymentChannelCreating).
		Action(func(ds *deal.ClientState, msgCID cid.Cid) error {
			ds.WaitMsgCID = &msgCID
			return nil
		}),
	fsm.Event(EventPaymentChannelAddingFunds).
		// If the deal has just been accepted, we are adding the initial funds
		// to the payment channel
		From(deal.StatusAccepted).To(deal.StatusPaymentChannelAddingInitialFunds).
		// If the deal was already ongoing, and ran out of funds, we are
		// topping up funds in the payment channel
		From(deal.StatusCheckFunds).To(deal.StatusPaymentChannelAddingFunds).
		Action(func(ds *deal.ClientState, msgCID cid.Cid, payCh address.Address) error {
			ds.WaitMsgCID = &msgCID
			if ds.PaymentInfo == nil {
				ds.PaymentInfo = &deal.PaymentInfo{
					PayCh: payCh,
				}
			}
			return nil
		}),
	fsm.Event(EventPaymentChannelReady).
		// If the payment channel between client and provider was being created
		// for the first time, or if the payment channel had already been
		// created for an earlier deal but the initial funding for this deal
		// was being added, then we still need to allocate a payment channel
		// lane
		FromMany(deal.StatusPaymentChannelCreating, deal.StatusPaymentChannelAddingInitialFunds, deal.StatusAccepted).
		To(deal.StatusPaymentChannelAllocatingLane).
		// If the payment channel ran out of funds and needed to be topped up,
		// then the payment channel lane already exists so just move straight
		// to the ongoing state
		From(deal.StatusPaymentChannelAddingFunds).To(deal.StatusOngoing).
		From(deal.StatusCheckFunds).To(deal.StatusOngoing).
		Action(func(ds *deal.ClientState, payCh address.Address) error {
			if ds.PaymentInfo == nil {
				ds.PaymentInfo = &deal.PaymentInfo{
					PayCh: payCh,
				}
			}
			ds.WaitMsgCID = nil
			// remove any insufficient funds message
			ds.Message = ""
			return nil
		}),
	fsm.Event(EventAllocateLaneErrored).
		FromMany(deal.StatusPaymentChannelAllocatingLane).
		To(deal.StatusFailing).
		Action(func(ds *deal.ClientState, err error) error {
			ds.Message = fmt.Errorf("allocating payment lane: %w", err).Error()
			return nil
		}),

	fsm.Event(EventLaneAllocated).
		From(deal.StatusPaymentChannelAllocatingLane).To(deal.StatusOngoing).
		Action(func(ds *deal.ClientState, lane uint64) error {
			ds.PaymentInfo.Lane = lane
			return nil
		}),

	// Provider Errors
	fsm.Event(EventProviderErrored).
		FromAny().To(deal.StatusErrored).
		Action(func(ds *deal.ClientState, msg string) error {
			ds.Message = msg
			return nil
		}),

	// Transfer Channel Errors
	fsm.Event(EventDataTransferError).
		FromAny().To(deal.StatusErrored).
		Action(func(ds *deal.ClientState, err error) error {
			ds.Message = fmt.Sprintf("error generated by data transfer: %s", err.Error())
			return nil
		}),

	// Receiving requests for payment
	fsm.Event(EventLastPaymentRequested).
		FromMany(
			deal.StatusOngoing,
			deal.StatusFundsNeededLastPayment,
			deal.StatusFundsNeeded).To(deal.StatusFundsNeededLastPayment).
		From(deal.StatusSendFunds).To(deal.StatusOngoing).
		From(deal.StatusCheckComplete).ToNoChange().
		From(deal.StatusBlocksComplete).To(deal.StatusSendFundsLastPayment).
		FromMany(
			paymentChannelCreationStates...).ToJustRecord().
		Action(func(ds *deal.ClientState, paymentOwed abi.TokenAmount) error {
			ds.PaymentRequested = big.Add(ds.PaymentRequested, paymentOwed)
			ds.LastPaymentRequested = true
			return nil
		}),
	fsm.Event(EventPaymentRequested).
		FromMany(
			deal.StatusOngoing,
			deal.StatusBlocksComplete,
			deal.StatusFundsNeededLastPayment,
			deal.StatusFundsNeeded).To(deal.StatusFundsNeeded).
		From(deal.StatusSendFunds).To(deal.StatusOngoing).
		From(deal.StatusCheckComplete).ToNoChange().
		FromMany(
			paymentChannelCreationStates...).ToJustRecord().
		Action(func(ds *deal.ClientState, paymentOwed abi.TokenAmount) error {
			ds.PaymentRequested = big.Add(ds.PaymentRequested, paymentOwed)
			return nil
		}),

	fsm.Event(EventUnsealPaymentRequested).
		From(deal.StatusWaitForAcceptance).To(deal.StatusAccepted).
		Action(func(ds *deal.ClientState, paymentOwed abi.TokenAmount) error {
			ds.PaymentRequested = big.Add(ds.PaymentRequested, paymentOwed)
			return nil
		}),

	// Receiving data
	fsm.Event(EventAllBlocksReceived).
		FromMany(
			deal.StatusOngoing,
			deal.StatusBlocksComplete,
		).To(deal.StatusBlocksComplete).
		FromMany(paymentChannelCreationStates...).ToJustRecord().
		FromMany(deal.StatusSendFunds, deal.StatusSendFundsLastPayment).To(deal.StatusOngoing).
		From(deal.StatusFundsNeeded).ToNoChange().
		From(deal.StatusFundsNeededLastPayment).To(deal.StatusSendFundsLastPayment).
		FromMany(
			deal.StatusClientWaitingForLastBlocks,
			// Should fix err: Invalid transition in queue, state `21`, event `16`
			deal.StatusCheckComplete,
		).To(deal.StatusCompleted).
		Action(func(ds *deal.ClientState) error {
			ds.AllBlocksReceived = true
			return nil
		}),
	fsm.Event(EventBlocksReceived).
		FromMany(deal.StatusOngoing,
			deal.StatusFundsNeeded,
			deal.StatusFundsNeededLastPayment,
			deal.StatusCheckComplete,
			deal.StatusClientWaitingForLastBlocks).ToNoChange().
		FromMany(deal.StatusSendFunds, deal.StatusSendFundsLastPayment).To(deal.StatusOngoing).
		FromMany(paymentChannelCreationStates...).ToJustRecord().
		Action(recordReceived),

	fsm.Event(EventSendFunds).
		FromMany(deal.StatusSendFunds, deal.StatusSendFundsLastPayment).To(deal.StatusOngoing).
		From(deal.StatusFundsNeeded).To(deal.StatusSendFunds).
		From(deal.StatusFundsNeededLastPayment).To(deal.StatusSendFundsLastPayment),

	// Sending Payments
	fsm.Event(EventFundsExpended).
		From(deal.StatusCheckFunds).To(deal.StatusInsufficientFunds).
		Action(func(ds *deal.ClientState, shortfall abi.TokenAmount) error {
			ds.Message = fmt.Sprintf("not enough current or pending funds in payment channel, shortfall of %s", shortfall.String())
			ds.VoucherShortfall = shortfall
			return nil
		}),
	fsm.Event(EventBadPaymentRequested).
		FromMany(deal.StatusSendFunds, deal.StatusSendFundsLastPayment).To(deal.StatusFailing).
		Action(func(ds *deal.ClientState, message string) error {
			ds.Message = message
			return nil
		}),
	fsm.Event(EventCreateVoucherFailed).
		FromMany(deal.StatusSendFunds, deal.StatusSendFundsLastPayment).To(deal.StatusFailing).
		Action(func(ds *deal.ClientState, err error) error {
			ds.Message = fmt.Errorf("creating payment voucher: %w", err).Error()
			return nil
		}),
	fsm.Event(EventVoucherShortfall).
		FromMany(deal.StatusSendFunds, deal.StatusSendFundsLastPayment).To(deal.StatusCheckFunds).
		Action(func(ds *deal.ClientState, shortfall abi.TokenAmount) error {
			return nil
		}),

	fsm.Event(EventWriteDealPaymentErrored).
		FromAny().To(deal.StatusErrored).
		Action(func(ds *deal.ClientState, err error) error {
			ds.Message = fmt.Errorf("writing deal payment: %w", err).Error()
			return nil
		}),
	// Payment was requested, but there was not actually any payment due, so
	// no payment voucher was actually sent
	fsm.Event(EventPaymentNotSent).
		From(deal.StatusOngoing).ToJustRecord().
		From(deal.StatusSendFunds).To(deal.StatusOngoing).
		From(deal.StatusSendFundsLastPayment).To(deal.StatusFinalizing),
	fsm.Event(EventPaymentSent).
		From(deal.StatusOngoing).ToJustRecord().
		From(deal.StatusBlocksComplete).To(deal.StatusCheckComplete).
		From(deal.StatusCheckComplete).ToNoChange().
		FromMany(
			deal.StatusFundsNeeded,
			deal.StatusFundsNeededLastPayment,
			deal.StatusSendFunds).To(deal.StatusOngoing).
		From(deal.StatusSendFundsLastPayment).To(deal.StatusFinalizing).
		Action(func(state *deal.ClientState, voucherAmt abi.TokenAmount) error {
			// Reduce the payment requested by the amount of funds sent.
			// Note that it may not be reduced to zero, if a new payment
			// request came in while this one was being processed.
			sentAmt := big.Sub(voucherAmt, state.FundsSpent)
			state.PaymentRequested = big.Sub(state.PaymentRequested, sentAmt)

			// Update the total funds sent to the provider
			state.FundsSpent = voucherAmt

			// If the unseal price hasn't yet been met, set the unseal funds
			// paid to the amount sent to the provider
			if state.UnsealPrice.GreaterThanEqual(state.FundsSpent) {
				state.UnsealFundsPaid = state.FundsSpent
				return nil
			}
			// The unseal funds have been fully paid
			state.UnsealFundsPaid = state.UnsealPrice

			// If the price per byte is zero, no further accounting needed
			if state.PricePerByte.IsZero() {
				return nil
			}

			// Calculate the amount spent on transferring data, and update the
			// bytes paid for accordingly
			paidSoFarForTransfer := big.Sub(state.FundsSpent, state.UnsealFundsPaid)
			state.BytesPaidFor = big.Div(paidSoFarForTransfer, state.PricePerByte).Uint64()

			// If the number of bytes paid for is above the current interval,
			// increase the interval
			if state.BytesPaidFor >= state.CurrentInterval {
				state.CurrentInterval = state.NextInterval()
			}

			return nil
		}),

	// completing deals
	fsm.Event(EventComplete).
		FromMany(
			deal.StatusSendFunds,
			deal.StatusSendFundsLastPayment,
			deal.StatusFundsNeeded,
			deal.StatusFundsNeededLastPayment,
			deal.StatusBlocksComplete,
			deal.StatusOngoing).To(deal.StatusCheckComplete).
		From(deal.StatusFinalizing).To(deal.StatusCompleted),
	fsm.Event(EventCompleteVerified).
		From(deal.StatusCheckComplete).To(deal.StatusCompleted),
	fsm.Event(EventEarlyTermination).
		From(deal.StatusCheckComplete).To(deal.StatusErrored).
		Action(func(state *deal.ClientState) error {
			state.Message = "Provider sent complete status without sending all data"
			return nil
		}),

	// the provider indicated that all blocks have been sent, so the client
	// should wait for the last blocks to arrive (only needed when price
	// per byte is zero)
	fsm.Event(EventWaitForLastBlocks).
		From(deal.StatusCheckComplete).To(deal.StatusClientWaitingForLastBlocks).
		From(deal.StatusCompleted).ToJustRecord(),

	// after cancelling a deal is complete
	fsm.Event(EventCancelComplete).
		From(deal.StatusFailing).To(deal.StatusErrored).
		From(deal.StatusCancelling).To(deal.StatusCancelled),

	// receiving a cancel indicating most likely that the provider experienced something wrong on their
	// end, unless we are already failing or cancelling
	fsm.Event(EventProviderCancelled).
		From(deal.StatusFailing).ToJustRecord().
		From(deal.StatusCancelling).ToJustRecord().
		FromAny().To(deal.StatusCancelling).Action(
		func(ds *deal.ClientState) error {
			if ds.Status != deal.StatusFailing && ds.Status != deal.StatusCancelling {
				ds.Message = "Provider cancelled retrieval"
			}
			return nil
		},
	),

	// user manually cancels retrieval
	fsm.Event(EventCancel).FromAny().To(deal.StatusCancelling).Action(func(ds *deal.ClientState) error {
		ds.Message = "Client cancelled retrieval"
		return nil
	}),

	// payment channel receives more money, we believe there may be reason to recheck the funds for this channel
	fsm.Event(EventRecheckFunds).From(deal.StatusInsufficientFunds).To(deal.StatusCheckFunds),
}

// FinalityStates are terminal states after which no further events are received
var FinalityStates = []fsm.StateKey{
	deal.StatusErrored,
	deal.StatusCompleted,
	deal.StatusCancelled,
	deal.StatusRejected,
	deal.StatusDealNotFound,
}

// StateEntryFuncs are the handlers for different states in a retrieval client
var StateEntryFuncs = fsm.StateEntryFuncs{
	deal.StatusNew:                              ProposeDeal,
	deal.StatusAccepted:                         SetupPaymentChannelStart,
	deal.StatusPaymentChannelCreating:           WaitPaymentChannelReady,
	deal.StatusPaymentChannelAddingInitialFunds: WaitPaymentChannelReady,
	deal.StatusPaymentChannelAllocatingLane:     AllocateLane,
	deal.StatusOngoing:                          Ongoing,
	deal.StatusFundsNeeded:                      ProcessPaymentRequested,
	deal.StatusFundsNeededLastPayment:           ProcessPaymentRequested,
	deal.StatusSendFunds:                        SendFunds,
	deal.StatusSendFundsLastPayment:             SendFunds,
	deal.StatusCheckFunds:                       CheckFunds,
	deal.StatusPaymentChannelAddingFunds:        WaitPaymentChannelReady,
	deal.StatusFailing:                          CancelDeal,
	deal.StatusCancelling:                       CancelDeal,
	deal.StatusCheckComplete:                    CheckComplete,
}

// DealEnvironment is a bridge to the environment a client deal is executing in.
// It provides access to relevant functionality on the retrieval client
type DealEnvironment interface {
	Payments() payments.Manager
	OpenDataTransfer(ctx context.Context, to peer.ID, proposal *deal.Proposal) (datatransfer.ChannelID, error)
	SendDataTransferVoucher(context.Context, datatransfer.ChannelID, *deal.Payment) error
	CloseDataTransfer(context.Context, datatransfer.ChannelID) error
}

// ProposeDeal sends the proposal to the other party
func ProposeDeal(ctx fsm.Context, environment DealEnvironment, ds deal.ClientState) error {
	channelID, err := environment.OpenDataTransfer(ctx.Context(), ds.Sender, &ds.Proposal)
	if err != nil {
		return ctx.Trigger(EventWriteDealProposalErrored, err)
	}
	return ctx.Trigger(EventDealProposed, channelID)
}

// SetupPaymentChannelStart initiates setting up a payment channel for a deal
func SetupPaymentChannelStart(ctx fsm.Context, environment DealEnvironment, ds deal.ClientState) error {
	// If the total funds required for the deal are zero, skip creating the payment channel
	if ds.TotalFunds.IsZero() {
		return ctx.Trigger(EventPaymentChannelSkip)
	}
	// We may already have a payment channel ready to go otherwise the state machine will wait for it
	res, err := environment.Payments().GetChannel(ctx.Context(), ds.ClientWallet, ds.MinerWallet, ds.TotalFunds)
	if err != nil {
		log.Error().Err(err).Msg("failed to setup channel")
		return ctx.Trigger(EventPaymentChannelErrored, err)
	}

	if res.Channel == address.Undef {
		return ctx.Trigger(EventPaymentChannelCreateInitiated, res.WaitSentinel)
	}

	if res.WaitSentinel == cid.Undef {
		return ctx.Trigger(EventPaymentChannelReady, res.Channel)
	}

	return ctx.Trigger(EventPaymentChannelAddingFunds, res.WaitSentinel, res.Channel)
}

// WaitPaymentChannelReady waits for a pending operation on a payment channel -- either creating or depositing funds
func WaitPaymentChannelReady(ctx fsm.Context, environment DealEnvironment, ds deal.ClientState) error {
	paych, err := environment.Payments().WaitForChannel(ctx.Context(), *ds.WaitMsgCID)
	if err != nil {
		return ctx.Trigger(EventPaymentChannelErrored, err)
	}
	return ctx.Trigger(EventPaymentChannelReady, paych)
}

// AllocateLane allocates a lane for this retrieval operation
func AllocateLane(ctx fsm.Context, environment DealEnvironment, ds deal.ClientState) error {
	lane, err := environment.Payments().AllocateLane(ctx.Context(), ds.PaymentInfo.PayCh)
	if err != nil {
		return ctx.Trigger(EventAllocateLaneErrored, err)
	}
	return ctx.Trigger(EventLaneAllocated, lane)
}

// Ongoing just double checks that we may need to move out of the ongoing state cause a payment was previously requested
func Ongoing(ctx fsm.Context, environment DealEnvironment, ds deal.ClientState) error {
	if ds.PaymentRequested.GreaterThan(big.Zero()) {
		if ds.LastPaymentRequested {
			return ctx.Trigger(EventLastPaymentRequested, big.Zero())
		}
		return ctx.Trigger(EventPaymentRequested, big.Zero())
	}
	return nil
}

// ProcessPaymentRequested processes a request for payment from the provider
func ProcessPaymentRequested(ctx fsm.Context, environment DealEnvironment, ds deal.ClientState) error {
	// If the unseal payment hasn't been made, we need to send funds
	if ds.UnsealPrice.GreaterThan(ds.UnsealFundsPaid) {
		return ctx.Trigger(EventSendFunds)
	}

	// If all bytes received have been paid for, we don't need to send funds
	if ds.BytesPaidFor >= ds.TotalReceived {
		return nil
	}

	// Not all bytes received have been paid for

	// If all blocks have been received we need to send a final payment
	if ds.AllBlocksReceived {
		return ctx.Trigger(EventSendFunds)
	}

	// Payments are made in intervals, as bytes are received from the provider.
	// If the number of bytes received is at or above the size of the current
	// interval, we need to send a payment.
	if ds.TotalReceived >= ds.CurrentInterval {
		return ctx.Trigger(EventSendFunds)
	}
	return nil
}

func calcAmountToSend(ds deal.ClientState) abi.TokenAmount {
	totalBytesToPayFor := ds.TotalReceived
	// If unsealing has been paid for, and not all blocks have been received
	if ds.UnsealFundsPaid.GreaterThanEqual(ds.UnsealPrice) && !ds.AllBlocksReceived {
		// If the number of bytes received is less than the number required
		// for the current payment interval, no need to send a payment
		if totalBytesToPayFor < ds.CurrentInterval {
			return big.Zero()
		}

		// Otherwise round the number of bytes to pay for down to the current interval
		totalBytesToPayFor = ds.CurrentInterval
	}

	// Calculate the payment amount due for data received
	transferPrice := big.Mul(abi.NewTokenAmount(int64(totalBytesToPayFor)), ds.PricePerByte)
	// Calculate the total amount including the unsealing cost
	return big.Add(transferPrice, ds.UnsealPrice)
}

// SendFunds sends the next amount requested by the provider
func SendFunds(ctx fsm.Context, env DealEnvironment, ds deal.ClientState) error {
	totalPrice := calcAmountToSend(ds)

	// If we've already sent at or above the amount due, no need to send funds
	if totalPrice.LessThanEqual(ds.FundsSpent) {
		return ctx.Trigger(EventPaymentNotSent)
	}

	// Create a payment voucher
	voucher, err := env.Payments().CreateVoucher(ctx.Context(), ds.PaymentInfo.PayCh, totalPrice, ds.PaymentInfo.Lane)
	if err != nil {
		return ctx.Trigger(EventCreateVoucherFailed, err)
	}

	if voucher.Shortfall.GreaterThan(big.Zero()) {
		return ctx.Trigger(EventVoucherShortfall, voucher.Shortfall)
	}

	// send the payment voucher
	err = env.SendDataTransferVoucher(ctx.Context(), ds.ChannelID, &deal.Payment{
		ID:             ds.Proposal.ID,
		PaymentChannel: ds.PaymentInfo.PayCh,
		PaymentVoucher: voucher.Voucher,
	})
	if err != nil {
		return ctx.Trigger(EventWriteDealPaymentErrored, err)
	}

	return ctx.Trigger(EventPaymentSent, totalPrice)
}

// CheckFunds examines current available funds in a payment channel after a voucher shortfall to determine
// a course of action -- whether it's a good time to try again, wait for pending operations, or
// we've truly expended all funds and we need to wait for a manual readd
func CheckFunds(ctx fsm.Context, env DealEnvironment, ds deal.ClientState) error {
	// if we already have an outstanding operation, let's wait for that to complete
	if ds.WaitMsgCID != nil {
		return ctx.Trigger(EventPaymentChannelAddingFunds, *ds.WaitMsgCID, ds.PaymentInfo.PayCh)
	}
	// Check the state of the channel on chain
	availableFunds, err := env.Payments().ChannelAvailableFunds(ds.PaymentInfo.PayCh)
	if err != nil {
		return ctx.Trigger(EventPaymentChannelErrored, err)
	}

	total := calcAmountToSend(ds)
	unredeemedFunds := big.Sub(availableFunds.ConfirmedAmt, availableFunds.VoucherRedeemedAmt)
	shortfall := big.Sub(total, unredeemedFunds)

	// The shortfall is negative when there is more funds in the channel than the requested payment amount
	if shortfall.LessThanEqual(big.Zero()) {
		return ctx.Trigger(EventPaymentChannelReady, ds.PaymentInfo.PayCh)
	}

	// pending amount is in a message already submitted but not confirmed on chain yet
	// queued amount is ready to submit in the next message but not sent yet
	totalInFlight := big.Add(availableFunds.PendingAmt, availableFunds.QueuedAmt)
	// The amount in flight is still not enough and we need to expend it further
	if totalInFlight.LessThan(shortfall) || availableFunds.PendingWaitSentinel == nil {
		finalShortfall := big.Sub(shortfall, totalInFlight)
		return ctx.Trigger(EventFundsExpended, finalShortfall)
	}
	// There are enough funds waiting to be confirmed so just wait until then
	return ctx.Trigger(EventPaymentChannelAddingFunds, *availableFunds.PendingWaitSentinel, ds.PaymentInfo.PayCh)
}

// CancelDeal clears a deal that went wrong for an unknown reason
func CancelDeal(ctx fsm.Context, environment DealEnvironment, ds deal.ClientState) error {
	// Read next response (or fail)
	err := environment.CloseDataTransfer(ctx.Context(), ds.ChannelID)
	if err != nil {
		return ctx.Trigger(EventDataTransferError, err)
	}

	return ctx.Trigger(EventCancelComplete)
}

// CheckComplete verifies that a provider that completed without a last payment requested did in fact send us all the data
func CheckComplete(ctx fsm.Context, environment DealEnvironment, ds deal.ClientState) error {
	// This function is called when the provider tells the client that it has
	// sent all the blocks, so check if all blocks have been received.
	if ds.AllBlocksReceived {
		return ctx.Trigger(EventCompleteVerified)
	}

	// If the deal price per byte is zero, wait for the last blocks to
	// arrive
	if ds.PricePerByte.IsZero() {
		return ctx.Trigger(EventWaitForLastBlocks)
	}

	// If the deal price per byte is non-zero, the provider should only
	// have sent the complete message after receiving the last payment
	// from the client, which should happen after all blocks have been
	// received. So if they haven't been received the provider is trying
	// to terminate the deal early.
	return ctx.Trigger(EventEarlyTermination)
}
