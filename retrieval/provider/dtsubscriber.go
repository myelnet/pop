package provider

import (
	"fmt"
	"math"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-statemachine/fsm"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/myelnet/pop/retrieval/deal"
	"github.com/rs/zerolog/log"
)

// EventReceiver is any thing that can receive FSM events
type EventReceiver interface {
	Has(id interface{}) (bool, error)
	Send(id interface{}, name fsm.EventName, args ...interface{}) (err error)
}

const noProviderEvent = Event(math.MaxUint64)

func eventFromDataTransfer(event datatransfer.Event, channelState datatransfer.ChannelState) (Event, []interface{}) {
	switch event.Code {
	case datatransfer.Accept:
		return EventDealAccepted, []interface{}{channelState.ChannelID()}
	case datatransfer.Disconnected:
		return EventDataTransferError, []interface{}{fmt.Errorf("deal data transfer stalled (peer hungup)")}
	case datatransfer.Error:
		return EventDataTransferError, []interface{}{fmt.Errorf("deal data transfer failed: %s", event.Message)}
	case datatransfer.Cancel:
		return EventClientCancelled, nil
	default:
		return noProviderEvent, nil
	}
}

// DataTransferSubscriber is the function called when an event occurs in a data
// transfer received by a provider -- it reads the voucher to verify this event occurred
// in a storage market deal, then, based on the data transfer event that occurred, it generates
// and update message for the deal -- either moving to staged for a completion
// event or moving to error if a data transfer error occurs
func DataTransferSubscriber(deals EventReceiver, host peer.ID) datatransfer.Subscriber {
	return func(event datatransfer.Event, channelState datatransfer.ChannelState) {
		dealProposal, ok := deal.ProposalFromVoucher(channelState.Voucher())
		// if this event is for a transfer not related to storage, ignore
		if !ok {
			return
		}

		// If this host is not the Sender this event is about a deal it's receiving
		if channelState.Sender() != host {
			return
		}

		if channelState.Status() == datatransfer.Completed {
			err := deals.Send(deal.ProviderDealIdentifier{DealID: dealProposal.ID, Receiver: channelState.Recipient()}, EventComplete)
			if err != nil {
				log.Error().Err(err).Str("event", datatransfer.Events[event.Code]).Msg("processing provider dt event")
			}
		}

		retrievalEvent, params := eventFromDataTransfer(event, channelState)
		if retrievalEvent == noProviderEvent {
			return
		}

		err := deals.Send(deal.ProviderDealIdentifier{DealID: dealProposal.ID, Receiver: channelState.Recipient()}, retrievalEvent, params...)
		if err != nil {
			log.Error().Err(err).Str("event", datatransfer.Events[event.Code]).Msg("processing provider dt event")
		}

	}
}
