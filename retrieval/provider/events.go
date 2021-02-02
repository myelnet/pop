package provider

// Retrieval events as implemented by Lotus

// Event that occurs in a deal lifecycle on the provider
type Event uint64

const (
	// EventOpen indicates a new deal was received from a client
	EventOpen Event = iota

	// EventDealNotFound happens when the provider cannot find the piece for the
	// deal proposed by the client
	EventDealNotFound

	// EventDealRejected happens when a provider rejects a deal proposed
	// by the client
	EventDealRejected

	// EventDealAccepted happens when a provider accepts a deal
	EventDealAccepted

	// EventBlockSent happens when the provider reads another block
	// in the piece
	EventBlockSent

	// EventBlocksCompleted happens when the provider reads the last block
	// in the piece
	EventBlocksCompleted

	// EventPaymentRequested happens when a provider asks for payment from
	// a client for blocks sent
	EventPaymentRequested

	// EventSaveVoucherFailed happens when an attempt to save a payment
	// voucher fails
	EventSaveVoucherFailed

	// EventPartialPaymentReceived happens when a provider receives and processes
	// a payment that is less than what was requested to proceed with the deal
	EventPartialPaymentReceived

	// EventPaymentReceived happens when a provider receives a payment
	// and resumes processing a deal
	EventPaymentReceived

	// EventComplete indicates a retrieval deal was completed for a client
	EventComplete

	// EventUnsealError emits when something wrong occurs while unsealing data
	EventUnsealError

	// EventUnsealComplete emits when the unsealing process is done
	EventUnsealComplete

	// EventDataTransferError emits when something go wrong at the data transfer level
	EventDataTransferError

	// EventCancelComplete happens when a deal cancellation is transmitted to the provider
	EventCancelComplete

	// EventCleanupComplete happens when a deal is finished cleaning up and enters a complete state
	EventCleanupComplete

	// EventMultiStoreError occurs when an error happens attempting to operate on the multistore
	EventMultiStoreError

	// EventClientCancelled happens when the provider gets a cancel message from the client's data transfer
	EventClientCancelled
)

// Events is a human readable map of provider event name -> event description
var Events = map[Event]string{
	EventOpen:                   "ProviderEventOpen",
	EventDealNotFound:           "ProviderEventDealNotFound",
	EventDealRejected:           "ProviderEventDealRejected",
	EventDealAccepted:           "ProviderEventDealAccepted",
	EventBlockSent:              "ProviderEventBlockSent",
	EventBlocksCompleted:        "ProviderEventBlocksCompleted",
	EventPaymentRequested:       "ProviderEventPaymentRequested",
	EventSaveVoucherFailed:      "ProviderEventSaveVoucherFailed",
	EventPartialPaymentReceived: "ProviderEventPartialPaymentReceived",
	EventPaymentReceived:        "ProviderEventPaymentReceived",
	EventComplete:               "ProviderEventComplete",
	EventUnsealError:            "ProviderEventUnsealError",
	EventUnsealComplete:         "ProviderEventUnsealComplete",
	EventDataTransferError:      "ProviderEventDataTransferError",
	EventCancelComplete:         "ProviderEventCancelComplete",
	EventCleanupComplete:        "ProviderEventCleanupComplete",
	EventMultiStoreError:        "ProviderEventMultiStoreError",
	EventClientCancelled:        "ProviderEventClientCancelled",
}
