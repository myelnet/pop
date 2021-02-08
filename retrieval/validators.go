package retrieval

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-multistore"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/ipfs/go-cid"
	"github.com/ipld/go-ipld-prime"
	"github.com/ipld/go-ipld-prime/codec/dagcbor"
	peer "github.com/libp2p/go-libp2p-peer"
	"github.com/myelnet/go-hop-exchange/payments"
	"github.com/myelnet/go-hop-exchange/retrieval/deal"
	"github.com/myelnet/go-hop-exchange/retrieval/provider"
)

var allSelectorBytes []byte

func init() {
	buf := new(bytes.Buffer)
	_ = dagcbor.Encoder(AllSelector(), buf)
	allSelectorBytes = buf.Bytes()
}

// ValidationEnvironment contains the dependencies needed to validate deals
type ValidationEnvironment interface {
	// CheckDealParams verifies the given deal params are acceptable
	CheckDealParams(pricePerByte abi.TokenAmount, paymentInterval uint64, paymentIntervalIncrease uint64, unsealPrice abi.TokenAmount) error
	// RunDealDecisioningLogic runs custom deal decision logic to decide if a deal is accepted, if present
	RunDealDecisioningLogic(ctx context.Context, state deal.ProviderState) (bool, string, error)
	// StateMachines returns the FSM Group to begin tracking with
	BeginTracking(pds deal.ProviderState) error
	// NextStoreID allocates a store for this deal
	NextStoreID() (multistore.StoreID, error)
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
func (rv *ProviderRequestValidator) ValidatePush(sender peer.ID, voucher datatransfer.Voucher, baseCid cid.Cid, selector ipld.Node) (datatransfer.VoucherResult, error) {
	return nil, fmt.Errorf("No pushes accepted")
}

// ValidatePull validates a pull request received from the peer that will receive data
func (rv *ProviderRequestValidator) ValidatePull(receiver peer.ID, voucher datatransfer.Voucher, baseCid cid.Cid, selector ipld.Node) (datatransfer.VoucherResult, error) {
	proposal, ok := voucher.(*deal.Proposal)
	if !ok {
		return nil, fmt.Errorf("wrong voucher type")
	}

	response, err := rv.validatePull(receiver, proposal, baseCid, selector)
	if response == nil {
		return nil, err
	}

	return response, err
}

func (rv *ProviderRequestValidator) validatePull(receiver peer.ID, proposal *deal.Proposal, baseCid cid.Cid, selector ipld.Node) (*deal.Response, error) {

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

	pds := deal.ProviderState{
		Proposal: *proposal,
		Receiver: receiver,
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

	return &response, datatransfer.ErrPause
}

func (rv *ProviderRequestValidator) acceptDeal(d *deal.ProviderState) (deal.Status, error) {
	// check that the deal parameters match our required parameters or
	// reject outright
	err := rv.env.CheckDealParams(d.PricePerByte, d.PaymentInterval, d.PaymentIntervalIncrease, d.UnsealPrice)
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

	// TODO: verify we have the content
	// block, err := rv.env.GetBlock(d.PayloadCID)
	// if err != nil {
	// 	if err == retrievalmarket.ErrNotFound {
	// 		return retrievalmarket.DealStatusDealNotFound, err
	// 	}
	// 	return retrievalmarket.DealStatusErrored, err
	// }

	d.StoreID, err = rv.env.NextStoreID()
	if err != nil {
		return deal.StatusErrored, err
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
	if err == nil {
		channel.reload = true
	}
	return response, err
}

func (pr *ProviderRevalidator) processPayment(dealID deal.ProviderDealIdentifier, payment *deal.Payment) (*deal.Response, error) {

	// tok, _, err := pr.env.Node().GetChainHead(context.TODO())
	// if err != nil {
	// 	_ = pr.env.SendEvent(dealID, rm.ProviderEventSaveVoucherFailed, err)
	// 	return errorDealResponse(dealID, err), err
	// }

	d, err := pr.env.Get(dealID)
	if err != nil {
		return errorDealResponse(dealID, err), err
	}

	// attempt to redeem voucher
	// (totalSent * pricePerByte + unsealPrice) - fundsReceived
	paymentOwed := big.Sub(big.Add(big.Mul(abi.NewTokenAmount(int64(d.TotalSent)), d.PricePerByte), d.UnsealPrice), d.FundsReceived)
	received, err := pr.env.Payments().AddVoucherInbound(context.TODO(), payment.PaymentChannel, payment.PaymentVoucher, nil, paymentOwed)
	if err != nil {
		_ = pr.env.SendEvent(dealID, provider.EventSaveVoucherFailed, err)
		return errorDealResponse(dealID, err), err
	}

	// received = 0 / err = nil indicates that the voucher was already saved, but this may be ok
	// if we are making a deal with ourself - in this case, we'll instead calculate received
	// but subtracting from fund sent
	if big.Cmp(received, big.Zero()) == 0 {
		received = big.Sub(payment.PaymentVoucher.Amount, d.FundsReceived)
	}

	// check if all payments are received to continue the deal, or send updated required payment
	if received.LessThan(paymentOwed) {
		_ = pr.env.SendEvent(dealID, provider.EventPartialPaymentReceived, received)
		return &deal.Response{
			ID:          d.ID,
			Status:      d.Status,
			PaymentOwed: big.Sub(paymentOwed, received),
		}, datatransfer.ErrPause
	}

	// resume deal
	_ = pr.env.SendEvent(dealID, provider.EventPaymentReceived, received)
	if d.Status == deal.StatusFundsNeededLastPayment {
		return &deal.Response{
			ID:     d.ID,
			Status: deal.StatusCompleted,
		}, nil
	}
	return nil, nil
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
	if channel.pricePerByte.IsZero() || channel.totalSent-channel.totalPaidFor < channel.interval {
		return true, nil, pr.env.SendEvent(channel.dealID, provider.EventBlockSent, channel.totalSent)
	}

	paymentOwed := big.Mul(abi.NewTokenAmount(int64(channel.totalSent-channel.totalPaidFor)), channel.pricePerByte)
	err = pr.env.SendEvent(channel.dealID, provider.EventPaymentRequested, channel.totalSent)
	if err != nil {
		return true, nil, err
	}
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
