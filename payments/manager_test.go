package payments

import (
	"context"
	"testing"
	"time"

	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/specs-actors/v3/actors/builtin"
	"github.com/filecoin-project/specs-actors/v3/actors/builtin/paych"
	"github.com/filecoin-project/specs-actors/v3/support/mock"
	tutils "github.com/filecoin-project/specs-actors/v3/support/testing"
	block "github.com/ipfs/go-block-format"
	"github.com/ipfs/go-cid"
	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"
	keystore "github.com/ipfs/go-ipfs-keystore"
	cbor "github.com/ipfs/go-ipld-cbor"
	fil "github.com/myelnet/pop/filecoin"
	"github.com/myelnet/pop/wallet"
	"github.com/stretchr/testify/require"
)

// Losely copied from Lotus paychmgr test to make sure we pass the same tests
func TestAddFunds(t *testing.T) {
	bgCtx := context.Background()

	ctx, cancel := context.WithTimeout(bgCtx, 10*time.Second)
	defer cancel()

	api := fil.NewMockLotusAPI()

	ks := keystore.NewMemKeystore()

	w := wallet.NewIPFS(ks, api)

	addr1, err := w.NewKey(ctx, wallet.KTSecp256k1)
	require.NoError(t, err)

	addr2, err := w.NewKey(ctx, wallet.KTSecp256k1)
	require.NoError(t, err)

	ds := dssync.MutexWrap(ds.NewMapDatastore())
	cborstore := cbor.NewCborStore(&mockBlocks{make(map[cid.Cid]block.Block)})

	mgr := New(bgCtx, api, w, ds, cborstore)

	act := &fil.Actor{
		Code:    blockGen.Next().Cid(),
		Head:    blockGen.Next().Cid(),
		Nonce:   1,
		Balance: fil.NewInt(1000),
	}
	api.SetActor(act)
	chAddr := tutils.NewIDAddr(t, 101)

	res1, err := mgr.GetChannel(ctx, addr1, addr2, big.NewInt(10))
	require.NoError(t, err)
	require.NotNil(t, res1)

	// Should have no channels yet (message sent but channel not created)
	cis, err := mgr.ListChannels()
	require.NoError(t, err)
	require.Len(t, cis, 0)

	lookup := formatMsgLookup(t, chAddr)
	done := make(chan struct{})
	go func() {
		defer close(done)

		amt2 := big.NewInt(5)
		res2, err := mgr.GetChannel(ctx, addr1, addr2, amt2)
		require.NoError(t, err)

		require.Equal(t, chAddr, res2.Channel)

		cis, err := mgr.ListChannels()
		require.NoError(t, err)
		require.Len(t, cis, 1)
		require.Equal(t, chAddr, cis[0])

		// Amount should be amount sent to first GetPaych (to create
		// channel).
		// PendingAmount should be amount sent in second GetPaych
		// (second GetPaych triggered add funds, which has not yet been confirmed)
		ci, err := mgr.GetChannelInfo(chAddr)
		require.NoError(t, err)
		require.EqualValues(t, 10, ci.Amount.Int64())
		require.EqualValues(t, 5, ci.PendingAmount.Int64())
		require.Nil(t, ci.CreateMsg)

		// Trigger add funds confirmation
		api.SetMsgLookup(lookup)

		// Wait for add funds confirmation to be processed by manager
		_, err = mgr.WaitForChannel(ctx, res2.WaitSentinel)
		require.NoError(t, err)

		// Should still have one channel
		cis, err = mgr.ListChannels()
		require.NoError(t, err)
		require.Len(t, cis, 1)
		require.Equal(t, chAddr, cis[0])

		// Channel amount should include last amount sent to GetPaych
		ci, err = mgr.GetChannelInfo(chAddr)
		require.NoError(t, err)
		require.EqualValues(t, 15, ci.Amount.Int64())
		require.EqualValues(t, 0, ci.PendingAmount.Int64())
		require.Nil(t, ci.AddFundsMsg)
	}()

	// Send message confirmation to create channel
	api.SetMsgLookup(lookup)

	select {
	case <-ctx.Done():
		t.Error("Timeout")
	case <-done:
	}
}

// TestPaychAddVoucherAfterAddFunds tests adding a voucher to a channel with
// insufficient funds, then adding funds to the channel, then adding the
// voucher again
func TestPaychAddVoucherAfterAddFunds(t *testing.T) {
	bgCtx := context.Background()

	ctx, cancel := context.WithTimeout(bgCtx, 10*time.Second)
	defer cancel()

	api := fil.NewMockLotusAPI()

	ks := keystore.NewMemKeystore()

	w := wallet.NewIPFS(ks, api)

	from, err := w.NewKey(ctx, wallet.KTSecp256k1)
	require.NoError(t, err)

	to, err := w.NewKey(ctx, wallet.KTSecp256k1)
	require.NoError(t, err)

	payerAddr := tutils.NewIDAddr(t, 102)
	payeeAddr := tutils.NewIDAddr(t, 103)

	ds := dssync.MutexWrap(ds.NewMapDatastore())
	cborstore := cbor.NewCborStore(&mockBlocks{make(map[cid.Cid]block.Block)})

	mgr := New(bgCtx, api, w, ds, cborstore)

	createAmt := big.NewInt(10)
	act := &fil.Actor{
		Code:    blockGen.Next().Cid(),
		Head:    blockGen.Next().Cid(),
		Nonce:   1,
		Balance: createAmt,
	}
	api.SetActor(act)
	chAddr := tutils.NewIDAddr(t, 101)

	// Send create message for a channel with value 10
	createRes, err := mgr.GetChannel(ctx, from, to, createAmt)
	require.NoError(t, err)

	lookup := formatMsgLookup(t, chAddr)
	// Send message confirmation to create channel
	api.SetMsgLookup(lookup)

	// Wait for create response to be processed by manager
	waitRes, err := mgr.WaitForChannel(ctx, createRes.WaitSentinel)
	require.NoError(t, err)
	require.Equal(t, waitRes, chAddr)

	initActorAddr := tutils.NewIDAddr(t, 100)
	hasher := func(data []byte) [32]byte { return [32]byte{} }

	builder := mock.NewBuilder(ctx, chAddr).
		WithBalance(createAmt, abi.NewTokenAmount(0)).
		WithEpoch(abi.ChainEpoch(1)).
		WithCaller(initActorAddr, builtin.InitActorCodeID).
		WithActorType(payeeAddr, builtin.AccountActorCodeID).
		WithActorType(payerAddr, builtin.AccountActorCodeID).
		WithHasher(hasher)

	rt := builder.Build(t)
	params := &paych.ConstructorParams{To: payeeAddr, From: payerAddr}
	rt.ExpectValidateCallerType(builtin.InitActorCodeID)
	actor := paych.Actor{}
	rt.Call(actor.Constructor, params)

	var st paych.State
	rt.GetState(&st)

	actState := fil.ActorState{
		Balance: createAmt,
		State:   st,
	}
	// We need to set an actor state as creating a voucher will query the state from the chain
	api.SetActorState(&actState)
	// See channel tests for note about this
	objReader := func(c cid.Cid) []byte {
		var bg bytesGetter
		rt.StoreGet(c, &bg)
		return bg.Bytes()
	}
	api.SetObjectReader(objReader)

	api.SetAccountKey(from)

	// Create a voucher with a value equal to the channel balance
	vouchRes, err := mgr.CreateVoucher(ctx, chAddr, createAmt, 1)
	require.NoError(t, err)
	require.NotNil(t, vouchRes.Voucher)

	// Create a voucher in a different lane with an amount that exceeds the
	// channel balance
	excessAmt := fil.NewInt(5)
	vouchRes, err = mgr.CreateVoucher(ctx, chAddr, excessAmt, 2)
	require.NoError(t, err)
	require.Nil(t, vouchRes.Voucher)
	require.Equal(t, vouchRes.Shortfall, excessAmt)

	// Add funds so as to cover the voucher shortfall
	addResp, err := mgr.GetChannel(ctx, from, to, excessAmt)
	require.NoError(t, err)

	// Trigger add funds confirmation
	api.SetMsgLookup(lookup)

	// Update actor test case balance to reflect added funds
	newBalance := fil.BigAdd(createAmt, excessAmt)
	rt.SetBalance(newBalance)
	act = &fil.Actor{
		Code:    blockGen.Next().Cid(),
		Head:    blockGen.Next().Cid(),
		Nonce:   1,
		Balance: newBalance,
	}
	api.SetActor(act)

	// Wait for add funds confirmation to be processed by manager
	_, err = mgr.WaitForChannel(ctx, addResp.WaitSentinel)
	require.NoError(t, err)

	// Adding same voucher that previously exceeded channel balance
	// should succeed now that the channel balance has been increased
	vouchRes, err = mgr.CreateVoucher(ctx, chAddr, excessAmt, 2)
	require.NoError(t, err)
	require.NotNil(t, vouchRes.Voucher)
}
