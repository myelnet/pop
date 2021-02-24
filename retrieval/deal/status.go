package deal

// DealStatus as implemented by lotus

// Status is the status of a retrieval deal returned by a provider
// in a DealResponse
type Status uint64

const (
	// StatusNew is a deal that nothing has happened with yet
	StatusNew Status = iota

	// StatusUnsealing means the provider is unsealing data
	StatusUnsealing

	// StatusUnsealed means the provider has finished unsealing data
	StatusUnsealed

	// StatusWaitForAcceptance means we're waiting to hear back if the provider accepted our deal
	StatusWaitForAcceptance

	// StatusPaymentChannelCreating is the status set while waiting for the
	// payment channel creation to complete
	StatusPaymentChannelCreating

	// StatusPaymentChannelAddingFunds is the status when we are waiting for funds
	// to finish being sent to the payment channel
	StatusPaymentChannelAddingFunds

	// StatusAccepted means a deal has been accepted by a provider
	// and its is ready to proceed with retrieval
	StatusAccepted

	// StatusFundsNeededUnseal means a deal has been accepted by a provider
	// and payment is needed to unseal the data
	StatusFundsNeededUnseal

	// StatusFailing indicates something went wrong during a retrieval,
	// and we are cleaning up before terminating with an error
	StatusFailing

	// StatusRejected indicates the provider rejected a client's deal proposal
	// for some reason
	StatusRejected

	// StatusFundsNeeded indicates the provider needs a payment voucher to
	// continue processing the deal
	StatusFundsNeeded

	// StatusSendFunds indicates the client is now going to send funds because we reached the threshold of the last payment
	StatusSendFunds

	// StatusSendFundsLastPayment indicates the client is now going to send final funds because
	// we reached the threshold of the final payment
	StatusSendFundsLastPayment

	// StatusOngoing indicates the provider is continuing to process a deal
	StatusOngoing

	// StatusFundsNeededLastPayment indicates the provider needs a payment voucher
	// in order to complete a deal
	StatusFundsNeededLastPayment

	// StatusCompleted indicates a deal is complete
	StatusCompleted

	// StatusDealNotFound indicates an update was received for a deal that could
	// not be identified
	StatusDealNotFound

	// StatusErrored indicates a deal has terminated in an error
	StatusErrored

	// StatusBlocksComplete indicates that all blocks have been processed for the piece
	StatusBlocksComplete

	// StatusFinalizing means the last payment has been received and
	// we are just confirming the deal is complete
	StatusFinalizing

	// StatusCompleting is just an inbetween state to perform final cleanup of
	// complete deals
	StatusCompleting

	// StatusCheckComplete is used for when the provided completes without a last payment
	// requested cycle, to verify we have received all blocks
	StatusCheckComplete

	// StatusCheckFunds means we are looking at the state of funding for the channel to determine
	// if more money is incoming
	StatusCheckFunds

	// StatusInsufficientFunds indicates we have depleted funds for the retrieval payment channel
	// - we can resume after funds are added
	StatusInsufficientFunds

	// StatusPaymentChannelAllocatingLane is the status when we are making a lane for this channel
	StatusPaymentChannelAllocatingLane

	// StatusCancelling means we are cancelling an inprogress deal
	StatusCancelling

	// StatusCancelled means a deal has been cancelled
	StatusCancelled

	// StatusClientWaitingForLastBlocks means that the provider has told
	// the client that all blocks were sent for the deal, and the client is
	// waiting for the last blocks to arrive. This should only happen when
	// the deal price per byte is zero (if it's not zero the provider asks
	// for final payment after sending the last blocks).
	StatusClientWaitingForLastBlocks

	// StatusPaymentChannelAddingInitialFunds means that a payment channel
	// exists from an earlier deal between client and provider, but we need
	// to add funds to the channel for this particular deal
	StatusPaymentChannelAddingInitialFunds
)

// Statuses maps deal status to a human readable representation
var Statuses = map[Status]string{
	StatusNew:                              "DealStatusNew",
	StatusUnsealing:                        "DealStatusUnsealing",
	StatusUnsealed:                         "DealStatusUnsealed",
	StatusWaitForAcceptance:                "DealStatusWaitForAcceptance",
	StatusPaymentChannelCreating:           "DealStatusPaymentChannelCreating",
	StatusPaymentChannelAddingFunds:        "DealStatusPaymentChannelAddingFunds",
	StatusAccepted:                         "DealStatusAccepted",
	StatusFundsNeededUnseal:                "DealStatusFundsNeededUnseal",
	StatusFailing:                          "DealStatusFailing",
	StatusRejected:                         "DealStatusRejected",
	StatusFundsNeeded:                      "DealStatusFundsNeeded",
	StatusSendFunds:                        "DealStatusSendFunds",
	StatusSendFundsLastPayment:             "DealStatusSendFundsLastPayment",
	StatusOngoing:                          "DealStatusOngoing",
	StatusFundsNeededLastPayment:           "DealStatusFundsNeededLastPayment",
	StatusCompleted:                        "DealStatusCompleted",
	StatusDealNotFound:                     "DealStatusDealNotFound",
	StatusErrored:                          "DealStatusErrored",
	StatusBlocksComplete:                   "DealStatusBlocksComplete",
	StatusFinalizing:                       "DealStatusFinalizing",
	StatusCompleting:                       "DealStatusCompleting",
	StatusCheckComplete:                    "DealStatusCheckComplete",
	StatusCheckFunds:                       "DealStatusCheckFunds",
	StatusInsufficientFunds:                "DealStatusInsufficientFunds",
	StatusPaymentChannelAllocatingLane:     "DealStatusPaymentChannelAllocatingLane",
	StatusCancelling:                       "DealStatusCancelling",
	StatusCancelled:                        "DealStatusCancelled",
	StatusClientWaitingForLastBlocks:       "DealStatusWaitingForLastBlocks",
	StatusPaymentChannelAddingInitialFunds: "DealStatusPaymentChannelAddingInitialFunds",
}
