package payments

import (
	"context"
	"fmt"
	"sync"
	"testing"

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

func TestCreateChannel(t *testing.T) {
	ctx := context.Background()

	api := &fil.MockLotusAPI{}

	act := &fil.Actor{
		Code:    blockGen.Next().Cid(),
		Head:    blockGen.Next().Cid(),
		Nonce:   1,
		Balance: fil.NewInt(1000),
	}
	api.SetActor(act)

	chAddr := tutils.NewIDAddr(t, 100)
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
	api.SetMsgLookup(lookup)

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
		ctx:          ctx,
		api:          api,
		wal:          w,
		actStore:     cborstore,
		store:        store,
		lk:           &multiLock{globalLock: &mgr.lk},
		msgListeners: newMsgListeners(),
	}

	c, err := ch.create(ctx, filecoin.NewInt(123))
	require.NoError(t, err)

	fmt.Println(c.String())
}
