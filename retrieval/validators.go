package retrieval

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"runtime"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-multistore"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/ipfs/go-cid"
	"github.com/ipld/go-ipld-prime"
	"github.com/ipld/go-ipld-prime/codec/dagcbor"
	peer "github.com/libp2p/go-libp2p-peer"
	"github.com/myelnet/pop/payments"
	"github.com/myelnet/pop/retrieval/deal"
	"github.com/myelnet/pop/retrieval/provider"
	"github.com/myelnet/pop/selectors"
)

var allSelectorBytes []byte

func init() {
	buf := new(bytes.Buffer)
	_ = dagcbor.Encoder(selectors.All(), buf)
	allSelectorBytes = buf.Bytes()
}

// ValidationEnvironment contains the dependencies needed to validate deals
type ValidationEnvironment interface {
	// CheckDealParams verifies the given deal params are acceptable
	CheckDealParams(deal.ProviderState) error
	// RunDealDecisioningLogic runs custom deal decision logic to decide if a deal is accepted, if present
	RunDealDecisioningLogic(ctx context.Context, state deal.ProviderState) (bool, string, error)
	// StateMachines returns the FSM Group to begin tracking with
	BeginTracking(pds deal.ProviderState) error
	// NextStoreID allocates a store for this deal
	NextStoreID() (multistore.StoreID, error)
	// GetStoreID gets an existing store for this deal
	GetStoreID(cid.Cid) (multistore.StoreID, error)
}

// ProviderRequestValidator validates incoming requests for the Retrieval Provider
type ProviderRequestValidator struct {
	env ValidationEnvironment
}

// NewProviderRequestValidator returns a new instance of the ProviderRequestValidator
func NewProviderRequestValidator(env ValidationEnvironment) *ProviderRequestValidator {
	return &ProviderRequestValidator{env}
}

// ValidatePush validates a push request received from the peer that will send data
func (rv *ProviderRequestValidator) ValidatePush(isRestart bool, sender peer.ID, voucher datatransfer.Voucher, baseCid cid.Cid, selector ipld.Node) (datatransfer.VoucherResult, error) {
	return nil, fmt.Errorf("No pushes accepted")
}

// ValidatePull validates a pull request received from the peer that will receive data
func (rv *ProviderRequestValidator) ValidatePull(isRestart bool, receiver peer.ID, voucher datatransfer.Voucher, baseCid cid.Cid, selector ipld.Node) (datatransfer.VoucherResult, error) {
	proposal, ok := voucher.(*deal.Proposal)
	if !ok {
		return nil, fmt.Errorf("wrong voucher type")
	}

	if proposal.PayloadCID != baseCid {
		return nil, fmt.Errorf("incorrect CID for this proposal")
	}

	buf := new(bytes.Buffer)
	err := dagcbor.Encoder(selector, buf)
	if err != nil {
		return nil, err
	}
	bytesCompare := allSelectorBytes
	if proposal.SelectorSpecified() {
		bytesCompare = proposal.Selector.Raw
	}
	if !bytes.Equal(buf.Bytes(), bytesCompare) {
		return nil, fmt.Errorf("incorrect selector for this proposal")
	}

	// If the validation is for a restart request, return nil, which means
	// the data-transfer should not be explicitly paused or resumed
	if isRestart {
		return nil, nil
	}

	pds := deal.ProviderState{
		Proposal:        *proposal,
		Receiver:        receiver,
		CurrentInterval: proposal.PaymentInterval,
	}

	status, err := rv.acceptDeal(&pds)

	response := deal.Response{
		ID:     proposal.ID,
		Status: status,
	}

	if err != nil {
		response.Message = err.Error()
		return &response, err
	}

	err = rv.env.BeginTracking(pds)
	if err != nil {
		return nil, err
	}

	return &response, nil
}

func (rv *ProviderRequestValidator) acceptDeal(d *deal.ProviderState) (deal.Status, error) {
	// check that the deal parameters match our required parameters or
	// reject outright
	err := rv.env.CheckDealParams(*d)
	if err != nil {
		return deal.StatusRejected, err
	}

	accepted, reason, err := rv.env.RunDealDecisioningLogic(context.TODO(), *d)
	if err != nil {
		return deal.StatusErrored, err
	}
	if !accepted {
		return deal.StatusRejected, fmt.Errorf(reason)
	}

	// This also verifies we do have the content ready to provide
	d.StoreID, err = rv.env.GetStoreID(d.PayloadCID)
	if err != nil {
		return deal.StatusDealNotFound, err
	}

	return deal.StatusAccepted, nil
}

// RevalidatorEnvironment are the dependencies needed to
// build the logic of revalidation -- essentially, access to the node at statemachines
type RevalidatorEnvironment interface {
	Payments() payments.Manager
	SendEvent(dealID deal.ProviderDealIdentifier, evt provider.Event, args ...interface{}) error
	Get(dealID deal.ProviderDealIdentifier) (deal.ProviderState, error)
}

type channelData struct {
	dealID         deal.ProviderDealIdentifier
	totalSent      uint64
	totalPaidFor   uint64
	interval       uint64
	pricePerByte   abi.TokenAmount
	reload         bool
	legacyProtocol bool
}

// ProviderRevalidator defines data transfer revalidation logic in the context of
// a provider for a retrieval deal
type ProviderRevalidator struct {
	env               RevalidatorEnvironment
	trackedChannelsLk sync.RWMutex
	trackedChannels   map[datatransfer.ChannelID]*channelData
}

// NewProviderRevalidator returns a new instance of a ProviderRevalidator
func NewProviderRevalidator(env RevalidatorEnvironment) *ProviderRevalidator {
	return &ProviderRevalidator{
		env:             env,
		trackedChannels: make(map[datatransfer.ChannelID]*channelData),
	}
}

// TrackChannel indicates a retrieval deal tracked by this provider. It associates
// a given channel ID with a retrieval deal, so that checks run for data sent
// on the channel
func (pr *ProviderRevalidator) TrackChannel(d deal.ProviderState) {
	pr.trackedChannelsLk.Lock()
	defer pr.trackedChannelsLk.Unlock()
	pr.trackedChannels[d.ChannelID] = &channelData{
		dealID: d.Identifier(),
	}
	pr.writeDealState(d)
}

// UntrackChannel indicates a retrieval deal is finish and no longer is tracked
// by this provider
func (pr *ProviderRevalidator) UntrackChannel(d deal.ProviderState) {
	pr.trackedChannelsLk.Lock()
	defer pr.trackedChannelsLk.Unlock()
	delete(pr.trackedChannels, d.ChannelID)
}

func (pr *ProviderRevalidator) loadDealState(channel *channelData) error {
	if !channel.reload {
		return nil
	}
	deal, err := pr.env.Get(channel.dealID)
	if err != nil {
		return err
	}
	pr.writeDealState(deal)
	channel.reload = false
	return nil
}

func (pr *ProviderRevalidator) writeDealState(d deal.ProviderState) {
	channel := pr.trackedChannels[d.ChannelID]
	channel.totalSent = d.TotalSent
	if !d.PricePerByte.IsZero() {
		channel.totalPaidFor = big.Div(big.Max(big.Sub(d.FundsReceived, d.UnsealPrice), big.Zero()), d.PricePerByte).Uint64()
	}
	channel.interval = d.CurrentInterval
	channel.pricePerByte = d.PricePerByte
}

// Revalidate revalidates a request with a new voucher
func (pr *ProviderRevalidator) Revalidate(channelID datatransfer.ChannelID, voucher datatransfer.Voucher) (datatransfer.VoucherResult, error) {
	pr.trackedChannelsLk.RLock()
	defer pr.trackedChannelsLk.RUnlock()
	channel, ok := pr.trackedChannels[channelID]
	if !ok {
		return nil, nil
	}

	// read payment, or fail
	payment, ok := voucher.(*deal.Payment)
	if !ok {
		return nil, fmt.Errorf("wrong voucher type")
	}

	response, err := pr.processPayment(channel.dealID, payment)
	if err == nil || err == datatransfer.ErrResume {
		channel.reload = true
	}
	if response == nil {
		return nil, err
	}
	return response, err
}

func (pr *ProviderRevalidator) processPayment(dealID deal.ProviderDealIdentifier, payment *deal.Payment) (*deal.Response, error) {

	d, err := pr.env.Get(dealID)
	if err != nil {
		return errorDealResponse(dealID, err), err
	}

	// Save voucher
	received, err := pr.env.Payments().AddVoucherInbound(context.TODO(), payment.PaymentChannel, payment.PaymentVoucher, nil, big.Zero())
	if err != nil {
		_ = pr.env.SendEvent(dealID, provider.EventSaveVoucherFailed, err)
		return errorDealResponse(dealID, err), err
	}

	totalPaid := big.Add(d.FundsReceived, received)

	// check if all payments are received to continue the deal, or send updated required payment
	owed := paymentOwed(d, totalPaid)

	if owed.GreaterThan(big.Zero()) {
		runtime.Breakpoint()
		_ = pr.env.SendEvent(dealID, provider.EventPartialPaymentReceived, received, payment.PaymentChannel)
		return &deal.Response{
			ID:          d.ID,
			Status:      d.Status,
			PaymentOwed: owed,
		}, datatransfer.ErrPause
	}

	// resume deal
	_ = pr.env.SendEvent(dealID, provider.EventPaymentReceived, received, payment.PaymentChannel)
	runtime.Breakpoint()
	if d.Status == deal.StatusFundsNeededLastPayment {
		return &deal.Response{
			ID:     d.ID,
			Status: deal.StatusCompleted,
		}, datatransfer.ErrResume
	}
	return nil, datatransfer.ErrResume
}

func paymentOwed(d deal.ProviderState, totalPaid big.Int) big.Int {
	// Check if the payment covers unsealing
	if totalPaid.LessThan(d.UnsealPrice) {
		return big.Sub(d.UnsealPrice, totalPaid)
	}

	// Calculate how much payment has been made for transferred data
	transferPayment := big.Sub(totalPaid, d.UnsealPrice)

	// The provider sends data and the client sends payment for the data.
	// The provider will send a limited amount of extra data before receiving
	// payment. Given the current limit, check if the client has paid enough
	// to unlock the next interval.
	currentLimitLower := d.IntervalLowerBound()

	// Calculate the minimum required payment
	totalPaymentRequired := big.Mul(big.NewInt(int64(currentLimitLower)), d.PricePerByte)

	// Calculate payment owed
	owed := big.Sub(totalPaymentRequired, transferPayment)

	runtime.Breakpoint()
	return owed
}

func errorDealResponse(dealID deal.ProviderDealIdentifier, err error) *deal.Response {
	return &deal.Response{
		ID:      dealID.DealID,
		Message: err.Error(),
		Status:  deal.StatusErrored,
	}
}

// OnPullDataSent is called on the responder side when more bytes are sent
// for a given pull request. It should return a VoucherResult + ErrPause to
// request revalidation or nil to continue uninterrupted,
// other errors will terminate the request
func (pr *ProviderRevalidator) OnPullDataSent(chid datatransfer.ChannelID, additionalBytesSent uint64) (bool, datatransfer.VoucherResult, error) {
	pr.trackedChannelsLk.RLock()
	defer pr.trackedChannelsLk.RUnlock()
	channel, ok := pr.trackedChannels[chid]
	if !ok {
		return false, nil, nil
	}

	err := pr.loadDealState(channel)
	if err != nil {
		return true, nil, err
	}

	channel.totalSent += additionalBytesSent
	if channel.pricePerByte.IsZero() || channel.totalSent < channel.interval {
		runtime.Breakpoint()
		return true, nil, pr.env.SendEvent(channel.dealID, provider.EventBlockSent, channel.totalSent)
	}

	paymentOwed := big.Mul(abi.NewTokenAmount(int64(channel.totalSent-channel.totalPaidFor)), channel.pricePerByte)
	err = pr.env.SendEvent(channel.dealID, provider.EventPaymentRequested, channel.totalSent)
	if err != nil {
		return true, nil, err
	}
	runtime.Breakpoint()
	return true, &deal.Response{
		ID:          channel.dealID.DealID,
		Status:      deal.StatusFundsNeeded,
		PaymentOwed: paymentOwed,
	}, datatransfer.ErrPause
}

// OnPushDataReceived is called on the responder side when more bytes are received
// for a given push request.  It should return a VoucherResult + ErrPause to
// request revalidation or nil to continue uninterrupted,
// other errors will terminate the request
func (pr *ProviderRevalidator) OnPushDataReceived(chid datatransfer.ChannelID, additionalBytesReceived uint64) (bool, datatransfer.VoucherResult, error) {
	return false, nil, nil
}

// OnComplete is called to make a final request for revalidation -- often for the
// purpose of settlement.
// if VoucherResult is non nil, the request will enter a settlement phase awaiting
// a final update
func (pr *ProviderRevalidator) OnComplete(chid datatransfer.ChannelID) (bool, datatransfer.VoucherResult, error) {
	pr.trackedChannelsLk.RLock()
	defer pr.trackedChannelsLk.RUnlock()
	channel, ok := pr.trackedChannels[chid]
	if !ok {
		return false, nil, nil
	}

	err := pr.loadDealState(channel)
	if err != nil {
		return true, nil, err
	}

	err = pr.env.SendEvent(channel.dealID, provider.EventBlocksCompleted)
	if err != nil {
		return true, nil, err
	}

	paymentOwed := big.Mul(abi.NewTokenAmount(int64(channel.totalSent-channel.totalPaidFor)), channel.pricePerByte)
	if paymentOwed.Equals(big.Zero()) {
		return true, &deal.Response{
			ID:     channel.dealID.DealID,
			Status: deal.StatusCompleted,
		}, nil
	}
	err = pr.env.SendEvent(channel.dealID, provider.EventPaymentRequested, channel.totalSent)
	if err != nil {
		return true, nil, err
	}
	return true, &deal.Response{
		ID:          channel.dealID.DealID,
		Status:      deal.StatusFundsNeededLastPayment,
		PaymentOwed: paymentOwed,
	}, datatransfer.ErrPause
}
