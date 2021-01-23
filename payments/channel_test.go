package payments

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/filecoin-project/go-address"
	cborutil "github.com/filecoin-project/go-cbor-util"
	tutils "github.com/filecoin-project/specs-actors/support/testing"
	init2 "github.com/filecoin-project/specs-actors/v2/actors/builtin/init"
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
	cborstore := cbor.NewMemCborStore()

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
		t.Error("onMsgComplete never called")
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
