package client

// Retrieval events as implemented by Lotus

// Event is an event that occurs in a deal lifecycle on the client
type Event uint64

const (
	// EventOpen indicates a deal was initiated
	EventOpen Event = iota

	// EventWriteDealProposalErrored means a network error writing a deal proposal
	EventWriteDealProposalErrored

	// EventDealProposed means a deal was successfully sent to a miner
	EventDealProposed

	// EventDealRejected means a deal was rejected by the provider
	EventDealRejected

	// EventDealNotFound means a provider could not find a piece for a deal
	EventDealNotFound

	// EventDealAccepted means a provider accepted a deal
	EventDealAccepted

	// EventProviderCancelled means a provider has sent a message to cancel a deal
	EventProviderCancelled

	// EventUnknownResponseReceived means a client received a response it doesn't
	// understand from the provider
	EventUnknownResponseReceived

	// EventPaymentChannelErrored means there was a failure creating a payment channel
	EventPaymentChannelErrored

	// EventAllocateLaneErrored means there was a failure creating a lane in a payment channel
	EventAllocateLaneErrored

	// EventPaymentChannelCreateInitiated means we are waiting for a message to
	// create a payment channel to appear on chain
	EventPaymentChannelCreateInitiated

	// EventPaymentChannelReady means the newly created payment channel is ready for the
	// deal to resume
	EventPaymentChannelReady

	// EventPaymentChannelSkip means we can skip payment channel because the deal price is 0
	EventPaymentChannelSkip

	// EventPaymentChannelAddingFunds mean we are waiting for funds to be
	// added to a payment channel
	EventPaymentChannelAddingFunds

	// EventPaymentChannelAddFundsErrored means that adding funds to the payment channel
	// failed
	EventPaymentChannelAddFundsErrored

	// EventLastPaymentRequested indicates the provider requested a final payment
	EventLastPaymentRequested

	// EventAllBlocksReceived indicates the provider has sent all blocks
	EventAllBlocksReceived

	// EventPaymentRequested indicates the provider requested a payment
	EventPaymentRequested

	// EventPaymentNotSent indicates that payment was requested, but no
	// payment was actually due, so a voucher was not sent to the provider
	EventPaymentNotSent

	// EventUnsealPaymentRequested indicates the provider requested a payment for unsealing the sector
	EventUnsealPaymentRequested

	// EventBlocksReceived indicates the provider has sent blocks
	EventBlocksReceived

	// EventSendFunds emits when we reach the threshold to send the next payment
	EventSendFunds

	// EventFundsExpended indicates a deal has run out of funds in the payment channel
	// forcing the client to add more funds to continue the deal
	EventFundsExpended // when totalFunds is expended

	// EventBadPaymentRequested indicates the provider asked for funds
	// in a way that does not match the terms of the deal
	EventBadPaymentRequested

	// EventCreateVoucherFailed indicates an error happened creating a payment voucher
	EventCreateVoucherFailed

	// EventWriteDealPaymentErrored indicates a network error trying to write a payment
	EventWriteDealPaymentErrored

	// EventPaymentSent indicates a payment was sent to the provider
	EventPaymentSent

	// EventComplete indicates a deal has completed
	EventComplete

	// EventDataTransferError emits when something go wrong at the data transfer level
	EventDataTransferError

	// EventCancelComplete happens when a deal cancellation is transmitted to the provider
	EventCancelComplete

	// EventEarlyTermination indications a provider send a deal complete without sending all data
	EventEarlyTermination

	// EventCompleteVerified means that a provider completed without requesting a final payment but
	// we verified we received all data
	EventCompleteVerified

	// EventLaneAllocated is called when a lane is allocated
	EventLaneAllocated

	// EventVoucherShortfall means we tried to create a voucher but did not have enough funds in channel
	// to create it
	EventVoucherShortfall

	// EventRecheckFunds runs when an external caller indicates there may be new funds in a payment channel
	EventRecheckFunds

	// EventCancel runs when a user cancels a deal
	EventCancel

	// EventWaitForLastBlocks is fired when the provider has told
	// the client that all blocks were sent for the deal, and the client is
	// waiting for the last blocks to arrive
	EventWaitForLastBlocks

	// EventProviderErrored happens when we receive a status in response voucher
	// telling us something went wrong on the provider side but they don't know what (500)
	EventProviderErrored
)

// Events is a human readable map of client event name -> event description
var Events = map[Event]string{
	EventOpen:                          "ClientEventOpen",
	EventPaymentChannelErrored:         "ClientEventPaymentChannelErrored",
	EventDealProposed:                  "ClientEventDealProposed",
	EventAllocateLaneErrored:           "ClientEventAllocateLaneErrored",
	EventPaymentChannelCreateInitiated: "ClientEventPaymentChannelCreateInitiated",
	EventPaymentChannelReady:           "ClientEventPaymentChannelReady",
	EventPaymentChannelAddingFunds:     "ClientEventPaymentChannelAddingFunds",
	EventPaymentChannelAddFundsErrored: "ClientEventPaymentChannelAddFundsErrored",
	EventPaymentChannelSkip:            "ClientEventPaymentChannelSkip",
	EventWriteDealProposalErrored:      "ClientEventWriteDealProposalErrored",
	EventDealRejected:                  "ClientEventDealRejected",
	EventDealNotFound:                  "ClientEventDealNotFound",
	EventDealAccepted:                  "ClientEventDealAccepted",
	EventProviderCancelled:             "ClientEventProviderCancelled",
	EventUnknownResponseReceived:       "ClientEventUnknownResponseReceived",
	EventLastPaymentRequested:          "ClientEventLastPaymentRequested",
	EventAllBlocksReceived:             "ClientEventAllBlocksReceived",
	EventPaymentRequested:              "ClientEventPaymentRequested",
	EventUnsealPaymentRequested:        "ClientEventUnsealPaymentRequested",
	EventBlocksReceived:                "ClientEventBlocksReceived",
	EventSendFunds:                     "ClientEventSendFunds",
	EventFundsExpended:                 "ClientEventFundsExpended",
	EventBadPaymentRequested:           "ClientEventBadPaymentRequested",
	EventCreateVoucherFailed:           "ClientEventCreateVoucherFailed",
	EventWriteDealPaymentErrored:       "ClientEventWriteDealPaymentErrored",
	EventPaymentSent:                   "ClientEventPaymentSent",
	EventPaymentNotSent:                "ClientEventPaymentNotSent",
	EventDataTransferError:             "ClientEventDataTransferError",
	EventComplete:                      "ClientEventComplete",
	EventCancelComplete:                "ClientEventCancelComplete",
	EventEarlyTermination:              "ClientEventEarlyTermination",
	EventCompleteVerified:              "ClientEventCompleteVerified",
	EventLaneAllocated:                 "ClientEventLaneAllocated",
	EventVoucherShortfall:              "ClientEventVoucherShortfall",
	EventRecheckFunds:                  "ClientEventRecheckFunds",
	EventCancel:                        "ClientEventCancel",
	EventWaitForLastBlocks:             "ClientEventWaitForLastBlocks",
	EventProviderErrored:               "ClientEventProviderErrored",
}
