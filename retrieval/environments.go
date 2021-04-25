package retrieval

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-multistore"
	"github.com/ipfs/go-cid"
	"github.com/ipld/go-ipld-prime"
	"github.com/ipld/go-ipld-prime/codec/dagcbor"
	basicnode "github.com/ipld/go-ipld-prime/node/basic"
	peer "github.com/libp2p/go-libp2p-peer"
	cbg "github.com/whyrusleeping/cbor-gen"

	"github.com/myelnet/pop/payments"
	"github.com/myelnet/pop/retrieval/client"
	"github.com/myelnet/pop/retrieval/deal"
	"github.com/myelnet/pop/retrieval/provider"
	"github.com/myelnet/pop/selectors"
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
	sel := selectors.All()
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
func (pve *providerValidationEnvironment) CheckDealParams(ds deal.ProviderState) error {
	ask := pve.p.GetAsk(ds.PayloadCID)
	if ds.PricePerByte.LessThan(ask.MinPricePerByte) {
		return errors.New("price per byte too low")
	}
	if ds.PaymentInterval > ask.MaxPaymentInterval {
		return errors.New("payment interval too large")
	}
	if ds.PaymentIntervalIncrease > ask.MaxPaymentIntervalIncrease {
		return errors.New("payment interval increase too large")
	}
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

// GetStoreID finds a store where our content is currently
func (pve *providerValidationEnvironment) GetStoreID(c cid.Cid) (multistore.StoreID, error) {
	return pve.p.storeIDGetter.GetStoreID(c)
}

// NextStoreID allocates a store for this deal TODO: do we still need this?
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
