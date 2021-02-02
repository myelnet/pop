package client

import (
	"context"
	"math/rand"
	"testing"

	"github.com/filecoin-project/go-address"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	testnet "github.com/filecoin-project/go-fil-markets/shared_testutil"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/go-statemachine/fsm"
	fsmtest "github.com/filecoin-project/go-statemachine/fsm/testutil"
	peer "github.com/libp2p/go-libp2p-peer"
	"github.com/stretchr/testify/require"

	"github.com/myelnet/go-hop-exchange/payments"
	"github.com/myelnet/go-hop-exchange/retrieval/deal"
)

func TestClientFSM(t *testing.T) {
	ctx := context.Background()
	eventMachine, err := fsm.NewEventProcessor(deal.ClientState{}, "Status", FSMEvents)
	require.NoError(t, err)

	t.Run("it works", func(t *testing.T) {
		dealState := makeClientDealState(deal.StatusNew)
		environment := &mockClientEnvironment{}
		fsmCtx := fsmtest.NewTestContext(ctx, eventMachine)
		err := ProposeDeal(fsmCtx, environment, *dealState)
		require.NoError(t, err)
		fsmCtx.ReplayEvents(t, dealState)
	})
}

type mockClientEnvironment struct {
	OpenDataTransferError        error
	SendDataTransferVoucherError error
	CloseDataTransferError       error
	payments                     payments.Manager
}

func (e *mockClientEnvironment) OpenDataTransfer(ctx context.Context, to peer.ID, proposal *deal.Proposal) (datatransfer.ChannelID, error) {
	return datatransfer.ChannelID{ID: datatransfer.TransferID(rand.Uint64()), Responder: to, Initiator: testnet.GeneratePeers(1)[0]}, e.OpenDataTransferError
}

func (e *mockClientEnvironment) SendDataTransferVoucher(_ context.Context, _ datatransfer.ChannelID, _ *deal.Payment) error {
	return e.SendDataTransferVoucherError
}

func (e *mockClientEnvironment) CloseDataTransfer(_ context.Context, _ datatransfer.ChannelID) error {
	return e.CloseDataTransferError
}

func (e *mockClientEnvironment) Payments() payments.Manager {
	return e.payments
}

func makeClientDealState(status deal.Status) *deal.ClientState {
	var defaultTotalFunds = abi.NewTokenAmount(4000000)
	var defaultCurrentInterval = uint64(1000)
	var defaultIntervalIncrease = uint64(500)
	var defaultPricePerByte = abi.NewTokenAmount(500)
	var defaultTotalReceived = uint64(6000)
	var defaultBytesPaidFor = uint64(5000)
	var defaultFundsSpent = abi.NewTokenAmount(2500000)
	var defaultPaymentRequested = abi.NewTokenAmount(500000)
	var defaultUnsealFundsPaid = abi.NewTokenAmount(0)

	paymentInfo := &deal.PaymentInfo{}

	switch status {
	case deal.StatusNew, deal.StatusAccepted, deal.StatusPaymentChannelCreating:
		paymentInfo = nil
	}

	params := deal.Params{
		PricePerByte:            defaultPricePerByte,
		PaymentInterval:         0,
		PaymentIntervalIncrease: defaultIntervalIncrease,
		UnsealPrice:             big.Zero(),
	}

	return &deal.ClientState{
		TotalFunds:       defaultTotalFunds,
		MinerWallet:      address.TestAddress,
		ClientWallet:     address.TestAddress2,
		PaymentInfo:      paymentInfo,
		Status:           status,
		BytesPaidFor:     defaultBytesPaidFor,
		TotalReceived:    defaultTotalReceived,
		CurrentInterval:  defaultCurrentInterval,
		FundsSpent:       defaultFundsSpent,
		UnsealFundsPaid:  defaultUnsealFundsPaid,
		PaymentRequested: defaultPaymentRequested,
		Proposal: deal.Proposal{
			ID:     deal.ID(10),
			Params: params,
		},
	}
}
