package payments

import (
	"context"
	"testing"
	"time"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/specs-actors/v7/actors/builtin"
	"github.com/filecoin-project/specs-actors/v7/actors/builtin/paych"
	"github.com/filecoin-project/specs-actors/v7/support/mock"
	tutils "github.com/filecoin-project/specs-actors/v7/support/testing"
	block "github.com/ipfs/go-block-format"
	"github.com/ipfs/go-cid"
	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"
	keystore "github.com/ipfs/go-ipfs-keystore"
	fil "github.com/myelnet/pop/filecoin"
	"github.com/myelnet/pop/internal/testutil"
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

	w := wallet.NewFromKeystore(ks, wallet.WithFilAPI(api))

	addr1, err := w.NewKey(ctx, wallet.KTSecp256k1)
	require.NoError(t, err)

	addr2, err := w.NewKey(ctx, wallet.KTSecp256k1)
	require.NoError(t, err)

	payerAddr := tutils.NewIDAddr(t, 102)
	payeeAddr := tutils.NewIDAddr(t, 103)

	api.SetAccountKey(payerAddr, addr1)
	api.SetAccountKey(payeeAddr, addr2)

	ds := dssync.MutexWrap(ds.NewMapDatastore())

	mgr := New(bgCtx, api, w, ds, &mockBlocks{make(map[cid.Cid]block.Block)})

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

	lookup := testutil.FormatMsgLookup(t, chAddr)
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
	// simulate confirmation delay
	time.Sleep(time.Second)
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
// voucher again. It is happening on the payer side
func TestPaychAddVoucherAfterAddFunds(t *testing.T) {
	bgCtx := context.Background()

	ctx, cancel := context.WithTimeout(bgCtx, 10*time.Second)
	defer cancel()

	api := fil.NewMockLotusAPI()

	ks := keystore.NewMemKeystore()

	w := wallet.NewFromKeystore(ks, wallet.WithFilAPI(api))

	from, err := w.NewKey(ctx, wallet.KTSecp256k1)
	require.NoError(t, err)

	to, err := w.NewKey(ctx, wallet.KTSecp256k1)
	require.NoError(t, err)

	payerAddr := tutils.NewIDAddr(t, 102)
	payeeAddr := tutils.NewIDAddr(t, 103)

	ds := dssync.MutexWrap(ds.NewMapDatastore())

	mgr := New(bgCtx, api, w, ds, &mockBlocks{make(map[cid.Cid]block.Block)})

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

	lookup := testutil.FormatMsgLookup(t, chAddr)
	// Send message confirmation to create channel
	api.SetMsgLookup(lookup)

	// Wait for create response to be processed by manager
	waitRes, err := mgr.WaitForChannel(ctx, createRes.WaitSentinel)
	require.NoError(t, err)
	require.Equal(t, waitRes, chAddr)

	initActorAddr := tutils.NewIDAddr(t, 100)
	hasher := func(data []byte) [32]byte { return [32]byte{} }

	builder := mock.NewBuilder(chAddr).
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
		var bg testutil.BytesGetter
		rt.StoreGet(c, &bg)
		return bg.Bytes()
	}
	api.SetObjectReader(objReader)

	api.SetAccountKey(payerAddr, from)

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

// TestBestSpendable is on the payee side to test the process of receiving and storing vouchers
// then verifying which vouchers to submit
func TestBestSpendable(t *testing.T) {
	bgCtx := context.Background()

	ctx, cancel := context.WithTimeout(bgCtx, 10*time.Second)
	defer cancel()

	api := fil.NewMockLotusAPI()

	ks := keystore.NewMemKeystore()

	w := wallet.NewFromKeystore(ks, wallet.WithFilAPI(api))

	from, err := w.NewKey(ctx, wallet.KTSecp256k1)
	require.NoError(t, err)

	to, err := w.NewKey(ctx, wallet.KTSecp256k1)
	require.NoError(t, err)

	payerAddr := tutils.NewIDAddr(t, 102)
	payeeAddr := tutils.NewIDAddr(t, 103)

	ds := dssync.MutexWrap(ds.NewMapDatastore())

	mgr := New(bgCtx, api, w, ds, &mockBlocks{make(map[cid.Cid]block.Block)})

	createAmt := big.NewInt(20)
	act := &fil.Actor{
		Code:    blockGen.Next().Cid(),
		Head:    blockGen.Next().Cid(),
		Nonce:   0,
		Balance: createAmt,
	}
	api.SetActor(act)
	chAddr := tutils.NewIDAddr(t, 101)

	initActorAddr := tutils.NewIDAddr(t, 100)
	hasher := func(data []byte) [32]byte { return [32]byte{} }

	builder := mock.NewBuilder(chAddr).
		WithBalance(createAmt, abi.NewTokenAmount(0)).
		WithEpoch(abi.ChainEpoch(1)).
		WithCaller(initActorAddr, builtin.InitActorCodeID).
		WithActorType(payeeAddr, builtin.AccountActorCodeID).
		WithActorType(payerAddr, builtin.AccountActorCodeID).
		WithHasher(hasher)

	// builder our actor runtime
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
	// add our actor state to the api so it's queryable
	api.SetActorState(&actState)
	// object reader to send a serialized object
	objReader := func(c cid.Cid) []byte {
		var bg testutil.BytesGetter
		rt.StoreGet(c, &bg)
		return bg.Bytes()
	}
	api.SetObjectReader(objReader)

	api.SetAccountKey(payerAddr, from)
	api.SetAccountKey(payeeAddr, to)

	// Add vouchers to lane 1 with amounts: [1, 2, 3]
	voucherLane := uint64(1)
	minDelta := big.NewInt(0)
	nonce := uint64(1)
	voucherAmount := big.NewInt(1)
	svL1V1 := createTestVoucher(t, chAddr, voucherLane, nonce, voucherAmount, from, w)
	_, err = mgr.AddVoucherInbound(ctx, chAddr, svL1V1, nil, minDelta)
	require.NoError(t, err)

	nonce++
	voucherAmount = big.NewInt(2)
	svL1V2 := createTestVoucher(t, chAddr, voucherLane, nonce, voucherAmount, from, w)
	_, err = mgr.AddVoucherInbound(ctx, chAddr, svL1V2, nil, minDelta)
	require.NoError(t, err)

	nonce++
	voucherAmount = big.NewInt(3)
	svL1V3 := createTestVoucher(t, chAddr, voucherLane, nonce, voucherAmount, from, w)
	_, err = mgr.AddVoucherInbound(ctx, chAddr, svL1V3, nil, minDelta)
	require.NoError(t, err)

	// Add voucher to lane 2 with amounts: [2]
	voucherLane = uint64(2)
	nonce = uint64(1)
	voucherAmount = big.NewInt(2)
	svL2V1 := createTestVoucher(t, chAddr, voucherLane, nonce, voucherAmount, from, w)
	_, err = mgr.AddVoucherInbound(ctx, chAddr, svL2V1, nil, minDelta)
	require.NoError(t, err)

	// Return success exit code from calls to check if voucher is spendable
	api.SetInvocResult(&fil.InvocResult{
		MsgRct: &fil.MessageReceipt{
			ExitCode: 0,
		},
	})

	// Verify best spendable vouchers on each lane
	vouchers, err := mgr.bestSpendableByLane(ctx, chAddr)
	require.NoError(t, err)
	require.Len(t, vouchers, 2)

	vchr, ok := vouchers[1]
	require.True(t, ok)
	require.EqualValues(t, 3, vchr.Amount.Int64())

	vchr, ok = vouchers[2]
	require.True(t, ok)
	require.EqualValues(t, 2, vchr.Amount.Int64())

	// Submit voucher from lane 2
	_, err = mgr.SubmitVoucher(ctx, chAddr, svL2V1, nil, nil)
	require.NoError(t, err)

	// Best spendable voucher should no longer include lane 2
	// (because voucher has not been submitted)
	vouchers, err = mgr.bestSpendableByLane(ctx, chAddr)
	require.NoError(t, err)
	require.Len(t, vouchers, 1)

	// Submit first voucher from lane 1
	_, err = mgr.SubmitVoucher(ctx, chAddr, svL1V1, nil, nil)
	require.NoError(t, err)

	// Best spendable voucher for lane 1 should still be highest value voucher
	vouchers, err = mgr.bestSpendableByLane(ctx, chAddr)
	require.NoError(t, err)
	require.Len(t, vouchers, 1)

	vchr, ok = vouchers[1]
	require.True(t, ok)
	require.EqualValues(t, 3, vchr.Amount.Int64())
}

// TestCollectChannel is on the payee side once we've received all the vouchers
// we should be able to settle the channel and submit all the vouchers then collect it
func TestCollectChannel(t *testing.T) {
	bgCtx := context.Background()

	ctx, cancel := context.WithTimeout(bgCtx, 10*time.Second)
	defer cancel()

	api := fil.NewMockLotusAPI()

	ks := keystore.NewMemKeystore()

	w := wallet.NewFromKeystore(ks, wallet.WithFilAPI(api))

	from, err := w.NewKey(ctx, wallet.KTSecp256k1)
	require.NoError(t, err)
	to, err := w.NewKey(ctx, wallet.KTSecp256k1)
	require.NoError(t, err)

	payerAddr := tutils.NewIDAddr(t, 102)
	payeeAddr := tutils.NewIDAddr(t, 103)

	ds := dssync.MutexWrap(ds.NewMapDatastore())

	mgr := New(bgCtx, api, w, ds, &mockBlocks{make(map[cid.Cid]block.Block)})

	createAmt := big.NewInt(20)
	act := &fil.Actor{
		Code:    blockGen.Next().Cid(),
		Head:    blockGen.Next().Cid(),
		Nonce:   0,
		Balance: createAmt,
	}
	api.SetActor(act)
	chAddr := tutils.NewIDAddr(t, 101)

	initActorAddr := tutils.NewIDAddr(t, 100)
	hasher := func(data []byte) [32]byte { return [32]byte{} }

	builder := mock.NewBuilder(chAddr).
		WithBalance(createAmt, abi.NewTokenAmount(0)).
		WithEpoch(abi.ChainEpoch(1)).
		WithCaller(initActorAddr, builtin.InitActorCodeID).
		WithActorType(payeeAddr, builtin.AccountActorCodeID).
		WithActorType(payerAddr, builtin.AccountActorCodeID).
		WithHasher(hasher)

	// builder our actor runtime
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
	// add our actor state to the api so it's queryable
	api.SetActorState(&actState)
	// object reader to send a serialized object
	objReader := func(c cid.Cid) []byte {
		var bg testutil.BytesGetter
		rt.StoreGet(c, &bg)
		return bg.Bytes()
	}
	api.SetObjectReader(objReader)

	api.SetAccountKey(payerAddr, from)
	api.SetAccountKey(payeeAddr, to)

	// Add vouchers to lane 1 with amounts: [1, 2, 3]
	voucherLane := uint64(1)
	minDelta := big.NewInt(0)
	nonce := uint64(1)
	voucherAmount := big.NewInt(1)
	svL1V1 := createTestVoucher(t, chAddr, voucherLane, nonce, voucherAmount, from, w)
	_, err = mgr.AddVoucherInbound(ctx, chAddr, svL1V1, nil, minDelta)
	require.NoError(t, err)

	nonce++
	voucherAmount = big.NewInt(2)
	svL1V2 := createTestVoucher(t, chAddr, voucherLane, nonce, voucherAmount, from, w)
	_, err = mgr.AddVoucherInbound(ctx, chAddr, svL1V2, nil, minDelta)
	require.NoError(t, err)

	nonce++
	voucherAmount = big.NewInt(3)
	svL1V3 := createTestVoucher(t, chAddr, voucherLane, nonce, voucherAmount, from, w)
	_, err = mgr.AddVoucherInbound(ctx, chAddr, svL1V3, nil, minDelta)
	require.NoError(t, err)

	// Add voucher to lane 2 with amounts: [2]
	voucherLane = uint64(2)
	nonce = uint64(1)
	voucherAmount = big.NewInt(2)
	svL2V1 := createTestVoucher(t, chAddr, voucherLane, nonce, voucherAmount, from, w)
	_, err = mgr.AddVoucherInbound(ctx, chAddr, svL2V1, nil, minDelta)
	require.NoError(t, err)

	// Return success exit code from calls to check if voucher is spendable
	api.SetInvocResult(&fil.InvocResult{
		MsgRct: &fil.MessageReceipt{
			ExitCode: 0,
		},
	})

	go func() {
		require.NoError(t, mgr.SubmitAllVouchers(ctx, chAddr))
	}()

	go func() {
		_, err := mgr.Settle(ctx, chAddr)
		require.NoError(t, err)
	}()

	ep := abi.ChainEpoch(10)
	rt.SetEpoch(ep)

	rt.GetState(&st)
	rt.SetCaller(st.From, builtin.AccountActorCodeID)
	rt.ExpectValidateCallerAddr(st.From, st.To)
	rt.Call(actor.Settle, nil)

	rt.GetState(&st)
	actState = fil.ActorState{
		Balance: createAmt,
		State:   st,
	}
	// update our actor state to the api so it's queryable
	api.SetActorState(&actState)

	lookup := testutil.FormatMsgLookup(t, chAddr)
	// We should have 3 chain txs we're waiting for
	for i := 0; i < 3; i++ {
		api.SetMsgLookup(lookup)
	}
	// Now we should have 1 settling channel
	settling, err := mgr.store.ListSettlingChannels()
	require.NoError(t, err)
	require.Equal(t, 1, len(settling))

	rt.GetState(&st)

	done := make(chan struct{})
	go func() {
		err = mgr.collectForEpoch(ctx, st.SettlingAt+1)
		require.NoError(t, err)
		close(done)
	}()

	// confirm collect message
	api.SetMsgLookup(lookup)

	<-done

	// Now we should have no settling channels
	settling, err = mgr.store.ListSettlingChannels()
	require.NoError(t, err)
	require.Equal(t, 0, len(settling))
}

func createTestVoucher(t *testing.T, ch address.Address, voucherLane uint64, nonce uint64, voucherAmount big.Int, addr address.Address, w wallet.Driver) *paych.SignedVoucher {
	sv := &paych.SignedVoucher{
		ChannelAddr: ch,
		Lane:        voucherLane,
		Nonce:       nonce,
		Amount:      voucherAmount,
	}

	signingBytes, err := sv.SigningBytes()
	require.NoError(t, err)
	sig, err := w.Sign(context.TODO(), addr, signingBytes)
	require.NoError(t, err)
	sv.Signature = sig
	return sv
}
