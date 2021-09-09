package provider

import (
	"context"
	"testing"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/go-statemachine/fsm"
	fsmtest "github.com/filecoin-project/go-statemachine/fsm/testutil"
	"github.com/myelnet/go-multistore"
	"github.com/stretchr/testify/require"

	"github.com/myelnet/pop/retrieval/deal"
)

func TestProviderFSM(t *testing.T) {
	ctx := context.Background()
	eventMachine, err := fsm.NewEventProcessor(deal.ProviderState{}, "Status", FSMEvents)
	require.NoError(t, err)

	t.Run("it works", func(t *testing.T) {
		dealState := makeProviderDealState(deal.StatusFailing)
		dealState.Message = "Existing error"
		environment := &mockProviderEnvironment{}
		fsmCtx := fsmtest.NewTestContext(ctx, eventMachine)
		err := CancelDeal(fsmCtx, environment, *dealState)
		require.NoError(t, err)
		fsmCtx.ReplayEvents(t, dealState)
		require.Equal(t, dealState.Status, deal.StatusErrored)
		require.Equal(t, dealState.Message, "Existing error")
	})
}

type mockProviderEnvironment struct {
	TrackTransferError      error
	UntrackTransferError    error
	CloseDataTransferError  error
	DeleteStoreError        error
	ResumeDataTransferError error
}

func (te *mockProviderEnvironment) DeleteStore(storeID multistore.StoreID) error {
	return te.DeleteStoreError
}

func (te mockProviderEnvironment) TrackTransfer(ds deal.ProviderState) error {
	return te.TrackTransferError
}

func (te *mockProviderEnvironment) UntrackTransfer(ds deal.ProviderState) error {
	return te.UntrackTransferError
}

func (te *mockProviderEnvironment) CloseDataTransfer(_ context.Context, _ datatransfer.ChannelID) error {
	return te.CloseDataTransferError
}

func (te *mockProviderEnvironment) ResumeDataTransfer(_ context.Context, _ datatransfer.ChannelID) error {
	return te.ResumeDataTransferError
}

func makeProviderDealState(status deal.Status) *deal.ProviderState {
	var dealID = deal.ID(10)
	var defaultCurrentInterval = uint64(1000)
	var defaultIntervalIncrease = uint64(500)
	var defaultPricePerByte = abi.NewTokenAmount(500)
	var defaultTotalSent = uint64(5000)
	var defaultFundsReceived = abi.NewTokenAmount(2500000)

	params := deal.Params{
		PricePerByte:            defaultPricePerByte,
		PaymentInterval:         0,
		PaymentIntervalIncrease: defaultIntervalIncrease,
		UnsealPrice:             big.Zero(),
	}
	return &deal.ProviderState{
		Status:          status,
		TotalSent:       defaultTotalSent,
		CurrentInterval: defaultCurrentInterval,
		FundsReceived:   defaultFundsReceived,
		Proposal: deal.Proposal{
			ID:     dealID,
			Params: params,
		},
	}
}
