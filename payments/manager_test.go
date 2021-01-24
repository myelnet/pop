package payments

import (
	"context"
	"testing"
	"time"

	"github.com/filecoin-project/go-state-types/big"
	tutils "github.com/filecoin-project/specs-actors/support/testing"
	block "github.com/ipfs/go-block-format"
	"github.com/ipfs/go-cid"
	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"
	"github.com/ipfs/go-ipfs/keystore"
	cbor "github.com/ipfs/go-ipld-cbor"
	fil "github.com/myelnet/go-hop-exchange/filecoin"
	"github.com/myelnet/go-hop-exchange/wallet"
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
