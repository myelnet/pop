package client

import (
	"fmt"
	"math"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-statemachine/fsm"
	"github.com/myelnet/go-hop-exchange/retrieval/deal"
)

// EventReceiver is any thing that can receive FSM events
type EventReceiver interface {
	Send(id interface{}, name fsm.EventName, args ...interface{}) (err error)
}

func eventFromDealStatus(response *deal.Response) (Event, []interface{}) {
	switch response.Status {
	case deal.StatusRejected:
		return EventDealRejected, []interface{}{response.Message}
	case deal.StatusDealNotFound:
		return EventDealNotFound, []interface{}{response.Message}
	case deal.StatusAccepted:
		return EventDealAccepted, nil
	case deal.StatusFundsNeededUnseal:
		return EventUnsealPaymentRequested, []interface{}{response.PaymentOwed}
	case deal.StatusFundsNeededLastPayment:
		return EventLastPaymentRequested, []interface{}{response.PaymentOwed}
	case deal.StatusCompleted:
		return EventComplete, nil
	case deal.StatusFundsNeeded:
		return EventPaymentRequested, []interface{}{response.PaymentOwed}
	default:
		return EventUnknownResponseReceived, nil
	}
}

const noEvent = Event(math.MaxUint64)

func eventFromDataTransfer(event datatransfer.Event, channelState datatransfer.ChannelState) (Event, []interface{}) {
	switch event.Code {
	case datatransfer.DataReceived:
		return EventBlocksReceived, []interface{}{channelState.Received()}
	case datatransfer.FinishTransfer:
		return EventAllBlocksReceived, nil
	case datatransfer.Cancel:
		return EventProviderCancelled, nil
	case datatransfer.NewVoucherResult:
		response, ok := deal.ResponseFromVoucherResult(channelState.LastVoucherResult())
		if !ok {
			fmt.Println("unexpected voucher result received:", channelState.LastVoucher().Type())
			return noEvent, nil
		}

		return eventFromDealStatus(response)
	case datatransfer.Disconnected:
		return EventDataTransferError, []interface{}{fmt.Errorf("deal data transfer stalled (peer hungup)")}
	case datatransfer.Error:
		if channelState.Message() == datatransfer.ErrRejected.Error() {
			return EventDealRejected, []interface{}{"rejected for unknown reasons"}
		}
		return EventDataTransferError, []interface{}{fmt.Errorf("deal data transfer failed: %s", event.Message)}
	default:
	}

	return noEvent, nil
}

// DataTransferSubscriber is the function called when an event occurs in a data
// transfer initiated on the client -- it reads the voucher to verify this even occurred
// in a storage market deal, then, based on the data transfer event that occurred, it dispatches
// an event to the appropriate state machine
func DataTransferSubscriber(deals EventReceiver) datatransfer.Subscriber {
	return func(event datatransfer.Event, channelState datatransfer.ChannelState) {
		dealProposal, ok := deal.ProposalFromVoucher(channelState.Voucher())

		// if this event is for a transfer not related to retrieval, ignore
		if !ok {
			return
		}

		retrievalEvent, params := eventFromDataTransfer(event, channelState)
		if retrievalEvent == noEvent {
			return
		}

		// data transfer events for progress do not affect deal state
		err := deals.Send(dealProposal.ID, retrievalEvent, params...)
		if err != nil {
			fmt.Println("processing dt client event:", err)
		}
	}
}
