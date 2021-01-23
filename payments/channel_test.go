package payments

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/filecoin-project/go-address"
	cborutil "github.com/filecoin-project/go-cbor-util"
	"github.com/filecoin-project/go-state-types/abi"
	tutils "github.com/filecoin-project/specs-actors/support/testing"
	init2 "github.com/filecoin-project/specs-actors/v2/actors/builtin/init"
	"github.com/filecoin-project/specs-actors/v3/actors/builtin"
	"github.com/filecoin-project/specs-actors/v3/actors/builtin/paych"
	"github.com/filecoin-project/specs-actors/v3/support/mock"
	block "github.com/ipfs/go-block-format"
	"github.com/ipfs/go-cid"
	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"
	blocksutil "github.com/ipfs/go-ipfs-blocksutil"
	"github.com/ipfs/go-ipfs/keystore"
	cbor "github.com/ipfs/go-ipld-cbor"
	"github.com/myelnet/go-hop-exchange/filecoin"
	fil "github.com/myelnet/go-hop-exchange/filecoin"
	"github.com/myelnet/go-hop-exchange/wallet"
	"github.com/stretchr/testify/require"
)

var blockGen = blocksutil.NewBlockGenerator()

type testMgr struct {
	lk sync.RWMutex
}

type mockBlocks struct {
	data map[cid.Cid]block.Block
}

func (mb *mockBlocks) Get(c cid.Cid) (block.Block, error) {
	d, ok := mb.data[c]
	if ok {
		return d, nil
	}
	return nil, fmt.Errorf("Not Found")
}

func (mb *mockBlocks) Put(b block.Block) error {
	mb.data[b.Cid()] = b
	return nil
}

func TestCreateAndAddChannel(t *testing.T) {
	bgCtx := context.Background()

	ctx, cancel := context.WithTimeout(bgCtx, 10*time.Second)
	defer cancel()

	api := fil.NewMockLotusAPI()

	act := &fil.Actor{
		Code:    blockGen.Next().Cid(),
		Head:    blockGen.Next().Cid(),
		Nonce:   1,
		Balance: fil.NewInt(1000),
	}
	api.SetActor(act)

	ks := keystore.NewMemKeystore()

	w := wallet.NewIPFS(ks, api)

	addr1, err := w.NewKey(ctx, wallet.KTSecp256k1)
	require.NoError(t, err)

	addr2, err := w.NewKey(ctx, wallet.KTSecp256k1)
	require.NoError(t, err)

	store := NewStore(dssync.MutexWrap(ds.NewMapDatastore()))
	cborstore := cbor.NewCborStore(&mockBlocks{make(map[cid.Cid]block.Block)})

	mgr := testMgr{}

	ch := &channel{
		from:         addr1,
		to:           addr2,
		ctx:          bgCtx,
		api:          api,
		wal:          w,
		actStore:     cborstore,
		store:        store,
		lk:           &multiLock{globalLock: &mgr.lk},
		msgListeners: newMsgListeners(),
	}

	c, err := ch.create(ctx, filecoin.NewInt(123))
	require.NoError(t, err)

	chInfo, err := ch.store.OutboundActiveByFromTo(ch.from, ch.to)
	require.NoError(t, err)

	// We should be storing a pending message if the channel is pending confirmation
	require.Equal(t, c, *chInfo.CreateMsg)

	chAddr := tutils.NewIDAddr(t, 100)
	lookup := formatMsgLookup(t, chAddr)

	confirmed := make(chan bool, 2)
	ch.msgListeners.onMsgComplete(c, func(e error) {
		require.NoError(t, e)
		// Now we should have confirmation
		confChInfo, err := ch.store.OutboundActiveByFromTo(ch.from, ch.to)
		require.NoError(t, err)

		// Channel address should be set
		require.Equal(t, *confChInfo.Channel, chAddr)
		confirmed <- true
	})

	api.SetMsgLookup(lookup)

	select {
	case <-ctx.Done():
		t.Error("onMsgComplete never called for create")
	case <-confirmed:
	}

	// Now let's add more funds to the channel
	addChInfo, err := ch.store.OutboundActiveByFromTo(ch.from, ch.to)
	require.NoError(t, err)

	addChCid, err := ch.addFunds(ctx, addChInfo, filecoin.NewInt(123))
	require.NoError(t, err)

	// Check if our pending message is there
	addChInfo2, err := ch.store.OutboundActiveByFromTo(ch.from, ch.to)
	require.NoError(t, err)

	require.Equal(t, *addChCid, *addChInfo2.AddFundsMsg)

	// Trigger confirmation
	ch.msgListeners.onMsgComplete(*addChCid, func(e error) {
		require.NoError(t, e)

		info, err := ch.store.OutboundActiveByFromTo(ch.from, ch.to)
		require.NoError(t, err)

		// Our amount should be updated
		require.Equal(t, filecoin.NewInt(246), info.Amount)
		confirmed <- true
	})

	// Lookup is the same as before
	api.SetMsgLookup(lookup)

	select {
	case <-ctx.Done():
		t.Error("onMsgComplete never called for add funds")
	case <-confirmed:
	}
}

func formatMsgLookup(t *testing.T, chAddr address.Address) *filecoin.MsgLookup {
	createChannelRet := init2.ExecReturn{
		IDAddress:     chAddr,
		RobustAddress: chAddr,
	}
	createChannelRetBytes, err := cborutil.Dump(&createChannelRet)
	require.NoError(t, err)
	lookup := &fil.MsgLookup{
		Message: blockGen.Next().Cid(),
		Receipt: fil.MessageReceipt{
			ExitCode: 0,
			Return:   createChannelRetBytes,
			GasUsed:  10,
		},
	}

	return lookup
}

func TestLoadActorState(t *testing.T) {
	bgCtx := context.Background()

	ctx, cancel := context.WithTimeout(bgCtx, 10*time.Second)
	defer cancel()

	ks := keystore.NewMemKeystore()

	api := fil.NewMockLotusAPI()

	initActorAddr := tutils.NewIDAddr(t, 100)
	chAddr := tutils.NewIDAddr(t, 101)
	payerAddr := tutils.NewIDAddr(t, 102)
	payeeAddr := tutils.NewIDAddr(t, 103)
	payChBalance := abi.NewTokenAmount(9)

	hasher := func(data []byte) [32]byte { return [32]byte{} }

	// Build a payment channel actor straight from the fil actors package
	builder := mock.NewBuilder(ctx, chAddr).
		WithBalance(payChBalance, abi.NewTokenAmount(0)).
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
		Balance: filecoin.NewInt(9),
		State:   st,
	}

	api.SetActorState(&actState)

	api.SetObject([]byte("testing"))

	w := wallet.NewIPFS(ks, api)

	store := NewStore(dssync.MutexWrap(ds.NewMapDatastore()))
	cborstore := cbor.NewCborStore(&mockBlocks{make(map[cid.Cid]block.Block)})

	mgr := testMgr{}

	ch := &channel{
		from:         payerAddr,
		to:           payeeAddr,
		ctx:          bgCtx,
		api:          api,
		wal:          w,
		actStore:     cborstore,
		store:        store,
		lk:           &multiLock{globalLock: &mgr.lk},
		msgListeners: newMsgListeners(),
	}

	state, err := ch.loadActorState(chAddr)
	require.NoError(t, err)

	from, err := state.From()
	require.NoError(t, err)

	require.Equal(t, ch.from, from)
}
