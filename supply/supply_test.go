package supply

import (
	"context"
	"testing"
	"time"

	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	peer "github.com/libp2p/go-libp2p-core/peer"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/myelnet/go-hop-exchange/testutil"
	"github.com/stretchr/testify/require"
)

func TestSendRequest(t *testing.T) {
	bgCtx := context.Background()

	ctx, cancel := context.WithTimeout(bgCtx, 10*time.Second)
	defer cancel()

	mn := mocknet.New(bgCtx)

	n1 := testutil.NewTestNode(mn, t)
	n1.SetupDataTransfer(bgCtx, t)

	// n1 is our client is adding a file to the store
	link, storeID, origBytes := n1.LoadFileToNewStore(bgCtx, t, "/supply/readme.md")
	rootCid := link.(cidlink.Link).Cid

	regions := []Region{
		{
			Name: "TestRegion",
			Code: CustomRegion,
		},
	}

	supply := New(n1.Host, n1.Dt, n1.Ds, n1.Ms, regions)

	providers := make(map[peer.ID]*testutil.TestNode)
	supplies := make(map[peer.ID]*Supply)
	// We add a bunch of retrieval providers
	for i := 0; i < 10; i++ {
		n := testutil.NewTestNode(mn, t)
		n.SetupDataTransfer(bgCtx, t)

		// Create a supply for each node
		s := New(n.Host, n.Dt, n.Ds, n.Ms, regions)

		providers[n.Host.ID()] = n
		supplies[n.Host.ID()] = s
	}

	err := mn.LinkAll()
	require.NoError(t, err)

	err = mn.ConnectAllButSelf()
	require.NoError(t, err)

	res, err := supply.Dispatch(Request{rootCid, uint64(len(origBytes))}, DispatchOptions{
		StoreID: storeID,
	})
	defer res.Close()
	require.NoError(t, err)

	var records []PRecord
	for len(records) < 6 {
		rec, err := res.Next(ctx)
		require.NoError(t, err)
		records = append(records, rec)
	}

	for _, c := range records {
		store, err := supplies[c.Provider].GetStore(rootCid)
		require.NoError(t, err)

		providers[c.Provider].VerifyFileTransferred(ctx, t, store.DAG, rootCid, origBytes)
	}
}

// In some rare cases where our node isn't connected to any peer we should still
// be able to fail gracefully
func TestSendRequestNoPeers(t *testing.T) {
	bgCtx := context.Background()

	mn := mocknet.New(bgCtx)

	n1 := testutil.NewTestNode(mn, t)
	n1.SetupDataTransfer(bgCtx, t)

	link, storeID, origBytes := n1.LoadFileToNewStore(bgCtx, t, "/supply/readme.md")
	rootCid := link.(cidlink.Link).Cid

	regions := []Region{
		{
			Name: "TestRegion",
			Code: CustomRegion,
		},
	}

	supply := New(n1.Host, n1.Dt, n1.Ds, n1.Ms, regions)

	res, err := supply.Dispatch(Request{rootCid, uint64(len(origBytes))}, DispatchOptions{
		StoreID: storeID,
	})
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

	// n1 is our client is adding a file to the store
	link, storeID, origBytes := n1.LoadFileToNewStore(bgCtx, t, "/supply/readme.md")
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

		// Create a supply for each node
		s := New(n.Host, n.Dt, n.Ds, n.Ms, africa)

		africaNodes[n.Host.ID()] = n
		africaSupplies = append(africaSupplies, s)
	}

	err := mn.LinkAll()
	require.NoError(t, err)

	err = mn.ConnectAllButSelf()
	require.NoError(t, err)

	res, err := supply.Dispatch(Request{rootCid, uint64(len(origBytes))}, DispatchOptions{
		StoreID: storeID,
	})
	defer res.Close()
	require.NoError(t, err)

	var recipients []PRecord
	for len(recipients) < 5 {
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
