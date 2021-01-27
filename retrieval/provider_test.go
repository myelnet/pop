package retrieval

import (
	"context"
	"testing"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-multistore"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/go-statemachine/fsm"
	fsmtest "github.com/filecoin-project/go-statemachine/fsm/testutil"
	"github.com/stretchr/testify/require"
)

func TestProviderFSM(t *testing.T) {
	ctx := context.Background()
	eventMachine, err := fsm.NewEventProcessor(ProviderDealState{}, "Status", ProviderChart)
	require.NoError(t, err)

	t.Run("it works", func(t *testing.T) {
		dealState := makeProviderDealState(DealStatusFailing)
		dealState.Message = "Existing error"
		environment := &mockProviderEnvironment{}
		fsmCtx := fsmtest.NewTestContext(ctx, eventMachine)
		err := CancelDeal(fsmCtx, environment, *dealState)
		require.NoError(t, err)
		fsmCtx.ReplayEvents(t, dealState)
		require.Equal(t, dealState.Status, DealStatusErrored)
		require.Equal(t, dealState.Message, "Existing error")
	})
}

type mockProviderEnvironment struct {
	TrackTransferError     error
	UntrackTransferError   error
	CloseDataTransferError error
	DeleteStoreError       error
}

func (te *mockProviderEnvironment) DeleteStore(storeID multistore.StoreID) error {
	return te.DeleteStoreError
}

func (te mockProviderEnvironment) TrackTransfer(deal ProviderDealState) error {
	return te.TrackTransferError
}

func (te *mockProviderEnvironment) UntrackTransfer(deal ProviderDealState) error {
	return te.UntrackTransferError
}

func (te *mockProviderEnvironment) CloseDataTransfer(_ context.Context, _ datatransfer.ChannelID) error {
	return te.CloseDataTransferError
}

func makeProviderDealState(status DealStatus) *ProviderDealState {
	var dealID = DealID(10)
	var defaultCurrentInterval = uint64(1000)
	var defaultIntervalIncrease = uint64(500)
	var defaultPricePerByte = abi.NewTokenAmount(500)
	var defaultTotalSent = uint64(5000)
	var defaultFundsReceived = abi.NewTokenAmount(2500000)

	params := Params{
		PricePerByte:            defaultPricePerByte,
		PaymentInterval:         0,
		PaymentIntervalIncrease: defaultIntervalIncrease,
		UnsealPrice:             big.Zero(),
	}
	return &ProviderDealState{
		Status:          status,
		TotalSent:       defaultTotalSent,
		CurrentInterval: defaultCurrentInterval,
		FundsReceived:   defaultFundsReceived,
		DealProposal: DealProposal{
			ID:     dealID,
			Params: params,
		},
	}
}
