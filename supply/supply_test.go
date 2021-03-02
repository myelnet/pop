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

func TestSendAddRequest(t *testing.T) {
	bgCtx := context.Background()

	ctx, cancel := context.WithTimeout(bgCtx, 10*time.Second)
	defer cancel()

	mn := mocknet.New(bgCtx)

	n1 := testutil.NewTestNode(mn, t)
	n1.SetupDataTransfer(bgCtx, t)
	require.NoError(t, n1.Dt.RegisterVoucherType(&AddRequest{}, &testutil.FakeDTValidator{}))

	// n1 is our client is adding a file to the store
	link, origBytes := n1.LoadUnixFSFileToStore(bgCtx, t, "/supply/readme.md")
	rootCid := link.(cidlink.Link).Cid

	regions := []Region{
		{
			Name: "TestRegion",
			Code: CustomRegion,
		},
	}

	supply := New(ctx, n1.Host, n1.Dt, regions)

	providers := make(map[peer.ID]*testutil.TestNode)
	var supplies []*Supply
	// We add a bunch of retrieval providers
	for i := 0; i < 10; i++ {
		n := testutil.NewTestNode(mn, t)
		n.SetupDataTransfer(bgCtx, t)

		// Create a supply for each node
		s := New(ctx, n.Host, n.Dt, regions)

		providers[n.Host.ID()] = n
		supplies = append(supplies, s)
	}

	err := mn.LinkAll()
	require.NoError(t, err)

	err = mn.ConnectAllButSelf()
	require.NoError(t, err)

	receivers := make(chan peer.ID, 6)
	done := make(chan error)
	unsubscribe := supply.SubscribeToEvents(func(event Event) {
		require.Equal(t, rootCid, event.PayloadCID)
		receivers <- event.Provider
		if len(receivers) == cap(receivers) {
			done <- nil
		}

	})
	defer unsubscribe()

	err = supply.SendAddRequest(rootCid, uint64(len(origBytes)))
	require.NoError(t, err)

	select {
	case <-ctx.Done():
		t.Error("requests incomplete")
	case <-done:
		pp := supply.providerPeers[rootCid].Peers()
		for i := 0; i < len(pp); i++ {
			providers[pp[i]].VerifyFileTransferred(ctx, t, rootCid, origBytes)
		}

	}
}

// In some rare cases where our node isn't connected to any peer we should still
// be able to fail gracefully
func TestSendAddRequestNoPeers(t *testing.T) {
	bgCtx := context.Background()

	ctx, cancel := context.WithTimeout(bgCtx, 10*time.Second)
	defer cancel()

	mn := mocknet.New(bgCtx)

	n1 := testutil.NewTestNode(mn, t)
	n1.SetupDataTransfer(bgCtx, t)
	require.NoError(t, n1.Dt.RegisterVoucherType(&AddRequest{}, &testutil.FakeDTValidator{}))

	link, origBytes := n1.LoadUnixFSFileToStore(bgCtx, t, "/supply/readme.md")
	rootCid := link.(cidlink.Link).Cid

	regions := []Region{
		{
			Name: "TestRegion",
			Code: CustomRegion,
		},
	}

	supply := New(ctx, n1.Host, n1.Dt, regions)

	err := supply.SendAddRequest(rootCid, uint64(len(origBytes)))
	require.EqualError(t, err, ErrNoPeers.Error())
}

// The role of this test is to make sure we never dispatch content to unwanted regions
func TestSendAddRequestDiffRegions(t *testing.T) {
	bgCtx := context.Background()

	ctx, cancel := context.WithTimeout(bgCtx, 10*time.Second)
	defer cancel()

	mn := mocknet.New(bgCtx)

	n1 := testutil.NewTestNode(mn, t)
	n1.SetupDataTransfer(bgCtx, t)
	require.NoError(t, n1.Dt.RegisterVoucherType(&AddRequest{}, &testutil.FakeDTValidator{}))

	// n1 is our client is adding a file to the store
	link, origBytes := n1.LoadUnixFSFileToStore(bgCtx, t, "/supply/readme.md")
	rootCid := link.(cidlink.Link).Cid

	asia := []Region{
		Regions["Asia"],
	}

	supply := New(ctx, n1.Host, n1.Dt, asia)

	asiaNodes := make(map[peer.ID]*testutil.TestNode)
	var asiaSupplies []*Supply
	// We add a bunch of asian retrieval providers
	for i := 0; i < 5; i++ {
		n := testutil.NewTestNode(mn, t)
		n.SetupDataTransfer(bgCtx, t)

		// Create a supply for each node
		s := New(ctx, n.Host, n.Dt, asia)

		asiaNodes[n.Host.ID()] = n
		asiaSupplies = append(asiaSupplies, s)
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
		s := New(ctx, n.Host, n.Dt, africa)

		africaNodes[n.Host.ID()] = n
		africaSupplies = append(africaSupplies, s)
	}

	err := mn.LinkAll()
	require.NoError(t, err)

	err = mn.ConnectAllButSelf()
	require.NoError(t, err)

	receivers := make(chan peer.ID, 4)
	done := make(chan error)
	unsubscribe := supply.SubscribeToEvents(func(event Event) {
		require.Equal(t, rootCid, event.PayloadCID)
		receivers <- event.Provider
		if len(receivers) == cap(receivers) {
			done <- nil
		}
	})
	defer unsubscribe()

	err = supply.SendAddRequest(rootCid, uint64(len(origBytes)))
	require.NoError(t, err)

	select {
	case <-ctx.Done():
		t.Error("requests incomplete")
	case <-done:
		pp := supply.providerPeers[rootCid].Peers()
		require.Equal(t, len(pp), 4)
		for i := 0; i < len(pp); i++ {
			asiaNodes[pp[i]].VerifyFileTransferred(ctx, t, rootCid, origBytes)
		}

	}
}
