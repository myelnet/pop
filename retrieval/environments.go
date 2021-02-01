package retrieval

import (
	"bytes"
	"context"
	"fmt"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-fil-markets/shared"
	"github.com/filecoin-project/go-multistore"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/ipld/go-ipld-prime"
	"github.com/ipld/go-ipld-prime/codec/dagcbor"
	basicnode "github.com/ipld/go-ipld-prime/node/basic"
	"github.com/ipld/go-ipld-prime/traversal/selector"
	"github.com/ipld/go-ipld-prime/traversal/selector/builder"
	peer "github.com/libp2p/go-libp2p-peer"
	cbg "github.com/whyrusleeping/cbor-gen"

	"github.com/myelnet/go-hop-exchange/payments"
	"github.com/myelnet/go-hop-exchange/retrieval/client"
	"github.com/myelnet/go-hop-exchange/retrieval/deal"
	"github.com/myelnet/go-hop-exchange/retrieval/provider"
)

var _ provider.DealEnvironment = new(providerDealEnvironment)

type providerDealEnvironment struct {
	p *Provider
}

func (pde *providerDealEnvironment) TrackTransfer(ds deal.ProviderState) error {
	pde.p.revalidator.TrackChannel(ds)
	return nil
}

func (pde *providerDealEnvironment) UntrackTransfer(ds deal.ProviderState) error {
	pde.p.revalidator.UntrackChannel(ds)
	return nil
}

func (pde *providerDealEnvironment) ResumeDataTransfer(ctx context.Context, chid datatransfer.ChannelID) error {
	return pde.p.dataTransfer.ResumeDataTransferChannel(ctx, chid)
}

func (pde *providerDealEnvironment) CloseDataTransfer(ctx context.Context, chid datatransfer.ChannelID) error {
	return pde.p.dataTransfer.CloseDataTransferChannel(ctx, chid)
}

func (pde *providerDealEnvironment) DeleteStore(storeID multistore.StoreID) error {
	return pde.p.multiStore.Delete(storeID)
}

var _ client.DealEnvironment = new(clientDealEnvironment)

type clientDealEnvironment struct {
	c *Client
}

func (cde *clientDealEnvironment) Payments() payments.Manager {
	return cde.c.pay
}

// AllSelector selects everything TODO: ipld sub module
func AllSelector() ipld.Node {
	ssb := builder.NewSelectorSpecBuilder(basicnode.Prototype.Any)
	return ssb.ExploreRecursive(selector.RecursionLimitNone(),
		ssb.ExploreAll(ssb.ExploreRecursiveEdge())).Node()
}

// DecodeNode validates and computes a decoded ipld.Node selector from the
// provided cbor-encoded selector TODO: ipld sub module
func DecodeNode(defnode *cbg.Deferred) (ipld.Node, error) {
	reader := bytes.NewReader(defnode.Raw)
	nb := basicnode.Prototype.Any.NewBuilder()
	err := dagcbor.Decoder(nb, reader)
	if err != nil {
		return nil, err
	}
	return nb.Build(), nil
}

func (cde *clientDealEnvironment) OpenDataTransfer(ctx context.Context, to peer.ID, proposal *deal.Proposal) (datatransfer.ChannelID, error) {
	sel := shared.AllSelector()
	if proposal.SelectorSpecified() {
		var err error
		sel, err = DecodeNode(proposal.Selector)
		if err != nil {
			return datatransfer.ChannelID{}, fmt.Errorf("selector is invalid: %w", err)
		}
	}

	var vouch datatransfer.Voucher = proposal
	return cde.c.dataTransfer.OpenPullDataChannel(ctx, to, vouch, proposal.PayloadCID, sel)
}

func (cde *clientDealEnvironment) SendDataTransferVoucher(ctx context.Context, channelID datatransfer.ChannelID, payment *deal.Payment) error {
	var vouch datatransfer.Voucher = payment
	return cde.c.dataTransfer.SendVoucher(ctx, channelID, vouch)
}

func (cde *clientDealEnvironment) CloseDataTransfer(ctx context.Context, channelID datatransfer.ChannelID) error {
	return cde.c.dataTransfer.CloseDataTransferChannel(ctx, channelID)
}

type providerValidationEnvironment struct {
	p *Provider
}

// CheckDealParams verifies the given deal params are acceptable
func (pve *providerValidationEnvironment) CheckDealParams(pricePerByte abi.TokenAmount, paymentInterval uint64, paymentIntervalIncrease uint64, unsealPrice abi.TokenAmount) error {
	// ask := pve.p.GetAsk()
	// if pricePerByte.LessThan(ask.PricePerByte) {
	// 	return errors.New("Price per byte too low")
	// }
	// if paymentInterval > ask.PaymentInterval {
	// 	return errors.New("Payment interval too large")
	// }
	// if paymentIntervalIncrease > ask.PaymentIntervalIncrease {
	// 	return errors.New("Payment interval increase too large")
	// }
	// if !ask.UnsealPrice.Nil() && unsealPrice.LessThan(ask.UnsealPrice) {
	// 	return errors.New("Unseal price too small")
	// }
	return nil
}

// RunDealDecisioningLogic runs custom deal decision logic to decide if a deal is accepted, if present
func (pve *providerValidationEnvironment) RunDealDecisioningLogic(ctx context.Context, state deal.ProviderState) (bool, string, error) {
	return true, "", nil
}

// StateMachines returns the FSM Group to begin tracking with
func (pve *providerValidationEnvironment) BeginTracking(pds deal.ProviderState) error {
	err := pve.p.stateMachines.Begin(pds.Identifier(), &pds)
	if err != nil {
		return err
	}

	return pve.p.stateMachines.Send(pds.Identifier(), provider.EventOpen)
}

// NextStoreID allocates a store for this deal
func (pve *providerValidationEnvironment) NextStoreID() (multistore.StoreID, error) {
	storeID := pve.p.multiStore.Next()
	_, err := pve.p.multiStore.Get(storeID)
	return storeID, err
}

type providerRevalidatorEnvironment struct {
	p *Provider
}

func (pre *providerRevalidatorEnvironment) Payments() payments.Manager {
	return pre.p.pay
}

func (pre *providerRevalidatorEnvironment) SendEvent(dealID deal.ProviderDealIdentifier, evt provider.Event, args ...interface{}) error {
	return pre.p.stateMachines.Send(dealID, evt, args...)
}

func (pre *providerRevalidatorEnvironment) Get(dealID deal.ProviderDealIdentifier) (deal.ProviderState, error) {
	var state deal.ProviderState
	err := pre.p.stateMachines.GetSync(context.TODO(), dealID, &state)
	return state, err
}
