package supply

import (
	"context"
	"fmt"
	"testing"
	"time"

	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	peer "github.com/libp2p/go-libp2p-core/peer"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/myelnet/pop/testutil"
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

			var testData []*testutil.TestNode
			receivers := make(map[peer.ID]*Supply)

			for i := 0; i < 7; i++ {
				tnode := testutil.NewTestNode(mn, t)
				tnode.SetupDataTransfer(ctx, t)
				t.Cleanup(func() {
					err := tnode.Dt.Stop(ctx)
					require.NoError(t, err)
				})

				hn1 := New(tnode.Host, tnode.Dt, tnode.Ds, tnode.Ms, regions)
				receivers[tnode.Host.ID()] = hn1
				testData = append(testData, tnode)
			}

			err := mn.LinkAll()
			require.NoError(t, err)

			err = mn.ConnectAllButSelf()
			require.NoError(t, err)

			// This delay is required to let the host register all the peers and protocols
			time.Sleep(10 * time.Millisecond)

			res, err := hn.Dispatch(Request{rootCid, uint64(len(origBytes))})
			defer res.Close()
			require.NoError(t, err)

			var recs []PRecord
			for len(recs) < res.Count {
				rec, err := res.Next(ctx)
				require.NoError(t, err)
				recs = append(recs, rec)
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

	res, err := supply.Dispatch(Request{rootCid, uint64(len(origBytes))})
	defer res.Close()
	require.EqualError(t, err, ErrNoPeers.Error())
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

		africaNodes[n.Host.ID()] = n
		africaSupplies = append(africaSupplies, s)
	}

	err := mn.LinkAll()
	require.NoError(t, err)

	err = mn.ConnectAllButSelf()
	require.NoError(t, err)

	require.NoError(t, supply.Register(rootCid, storeID))

	res, err := supply.Dispatch(Request{rootCid, uint64(len(origBytes))})
	defer res.Close()
	require.NoError(t, err)

	var recipients []PRecord
	for len(recipients) < res.Count {
		rec, err := res.Next(ctx)
		require.NoError(t, err)
		recipients = append(recipients, rec)
	}
	for _, p := range recipients {
		store, err := asiaSupplies[p.Provider].GetStore(rootCid)
		require.NoError(t, err)

		asiaNodes[p.Provider].VerifyFileTransferred(ctx, t, store.DAG, rootCid, origBytes)
	}
}
