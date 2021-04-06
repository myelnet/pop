package supply

import (
	"context"
	"fmt"
	"testing"
	"time"

	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	"github.com/libp2p/go-eventbus"
	peer "github.com/libp2p/go-libp2p-core/peer"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/myelnet/pop/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestMultiRequestStreams(t *testing.T) {
	// Loop is useful for detecting any flakiness
	for i := 0; i < 1; i++ {
		t.Run(fmt.Sprintf("Run %d", i), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()

			mn := mocknet.New(ctx)

			n1 := testutil.NewTestNode(mn, t)
			n1.SetupDataTransfer(ctx, t)
			t.Cleanup(func() {
				err := n1.Dt.Stop(ctx)
				require.NoError(t, err)
			})

			fname := n1.CreateRandomFile(t, 256000)

			root, storeID, origBytes := n1.LoadFileToNewStore(ctx, t, fname)
			rootCid := root.(cidlink.Link).Cid

			regions := []Region{
				{
					Name: "TestRegion",
					Code: CustomRegion,
				},
			}

			hn := New(n1.Host, n1.Dt, n1.Ds, n1.Ms, regions)
			hn.Register(rootCid, storeID)
			sub, err := hn.h.EventBus().Subscribe(new(PeerRegionEvt), eventbus.BufSize(16))
			require.NoError(t, err)
			require.NoError(t, hn.Start(ctx))

			tnds := make(map[peer.ID]*testutil.TestNode)
			receivers := make(map[peer.ID]*Supply)

			for i := 0; i < 7; i++ {
				tnode := testutil.NewTestNode(mn, t)
				tnode.SetupDataTransfer(ctx, t)
				t.Cleanup(func() {
					err := tnode.Dt.Stop(ctx)
					require.NoError(t, err)
				})

				hn1 := New(tnode.Host, tnode.Dt, tnode.Ds, tnode.Ms, regions)
				require.NoError(t, hn1.Start(ctx))
				receivers[tnode.Host.ID()] = hn1
				tnds[tnode.Host.ID()] = tnode
			}

			err = mn.LinkAll()
			require.NoError(t, err)

			err = mn.ConnectAllButSelf()
			require.NoError(t, err)

			// Wait for all peers to be received in the peer manager
			for i := 0; i < 7; i++ {
				select {
				case <-sub.Out():
				case <-ctx.Done():
					t.Fatal("all peers didn't get in the peermgr")
				}
			}

			res := hn.Dispatch(Request{rootCid, uint64(len(origBytes))}, DefaultDispatchOptions)

			var recs []PRecord
			for rec := range res {
				recs = append(recs, rec)
			}
			require.Equal(t, len(recs), 7)

			time.Sleep(time.Second)
			for _, r := range recs {
				store, err := receivers[r.Provider].GetStore(rootCid)
				require.NoError(t, err)
				tnds[r.Provider].VerifyFileTransferred(ctx, t, store.DAG, rootCid, origBytes)
			}

		})
	}
}

// In some rare cases where our node isn't connected to any peer we should still
// be able to fail gracefully
func TestSendRequestNoPeers(t *testing.T) {
	bgCtx := context.Background()

	mn := mocknet.New(bgCtx)

	n1 := testutil.NewTestNode(mn, t)
	n1.SetupDataTransfer(bgCtx, t)

	fname := n1.CreateRandomFile(t, 256000)

	link, storeID, origBytes := n1.LoadFileToNewStore(bgCtx, t, fname)
	rootCid := link.(cidlink.Link).Cid

	regions := []Region{
		{
			Name: "TestRegion",
			Code: CustomRegion,
		},
	}

	supply := New(n1.Host, n1.Dt, n1.Ds, n1.Ms, regions)
	supply.Register(rootCid, storeID)
	require.NoError(t, supply.Start(bgCtx))

	options := DispatchOptions{
		BackoffMin:     10 * time.Millisecond,
		BackoffAttemps: 4,
		RF:             5,
	}
	res := supply.Dispatch(Request{rootCid, uint64(len(origBytes))}, options)
	for range res {
	}
}

// The role of this test is to make sure we never dispatch content to unwanted regions
func TestSendRequestDiffRegions(t *testing.T) {
	bgCtx := context.Background()

	ctx, cancel := context.WithTimeout(bgCtx, 10*time.Second)
	defer cancel()

	mn := mocknet.New(bgCtx)

	n1 := testutil.NewTestNode(mn, t)
	n1.SetupDataTransfer(bgCtx, t)
	t.Cleanup(func() {
		err := n1.Dt.Stop(ctx)
		require.NoError(t, err)
	})

	fname := n1.CreateRandomFile(t, 512000)

	// n1 is our client is adding a file to the store
	link, storeID, origBytes := n1.LoadFileToNewStore(bgCtx, t, fname)
	rootCid := link.(cidlink.Link).Cid

	asia := []Region{
		Regions["Asia"],
	}

	supply := New(n1.Host, n1.Dt, n1.Ds, n1.Ms, asia)
	sub, err := n1.Host.EventBus().Subscribe(new(PeerRegionEvt), eventbus.BufSize(16))
	require.NoError(t, err)
	require.NoError(t, supply.Start(ctx))

	asiaNodes := make(map[peer.ID]*testutil.TestNode)
	asiaSupplies := make(map[peer.ID]*Supply)
	// We add a bunch of asian retrieval providers
	for i := 0; i < 5; i++ {
		n := testutil.NewTestNode(mn, t)
		n.SetupDataTransfer(bgCtx, t)
		t.Cleanup(func() {
			err := n.Dt.Stop(ctx)
			require.NoError(t, err)
		})

		// Create a supply for each node
		s := New(n.Host, n.Dt, n.Ds, n.Ms, asia)
		require.NoError(t, s.Start(ctx))

		asiaNodes[n.Host.ID()] = n
		asiaSupplies[n.Host.ID()] = s
	}

	africa := []Region{
		Regions["Africa"],
	}

	africaNodes := make(map[peer.ID]*testutil.TestNode)
	var africaSupplies []*Supply
	// Add african providers
	for i := 0; i < 3; i++ {
		n := testutil.NewTestNode(mn, t)
		n.SetupDataTransfer(bgCtx, t)
		t.Cleanup(func() {
			err := n.Dt.Stop(ctx)
			require.NoError(t, err)
		})

		// Create a supply for each node
		s := New(n.Host, n.Dt, n.Ds, n.Ms, africa)
		require.NoError(t, s.Start(ctx))

		africaNodes[n.Host.ID()] = n
		africaSupplies = append(africaSupplies, s)
	}

	err = mn.LinkAll()
	require.NoError(t, err)

	err = mn.ConnectAllButSelf()
	require.NoError(t, err)

	require.NoError(t, supply.Register(rootCid, storeID))

	// Wait for all peers to be received in the peer manager
	for i := 0; i < 5; i++ {
		select {
		case <-sub.Out():
		case <-ctx.Done():
			t.Fatal("all peers didn't get in the peermgr")
		}
	}
	// get 5 requests and give up after 4 attemps
	options := DispatchOptions{
		BackoffMin:     100 * time.Millisecond,
		BackoffAttemps: 4,
		RF:             7,
	}
	res := supply.Dispatch(Request{rootCid, uint64(len(origBytes))}, options)

	var recipients []PRecord
	for rec := range res {
		recipients = append(recipients, rec)
	}
	for _, p := range recipients {
		store, err := asiaSupplies[p.Provider].GetStore(rootCid)
		require.NoError(t, err)

		asiaNodes[p.Provider].VerifyFileTransferred(ctx, t, store.DAG, rootCid, origBytes)
	}
	require.Equal(t, 5, len(recipients))
}
