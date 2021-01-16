package supply

import (
	"context"
	"testing"
	"time"

	"github.com/ipfs/go-cid"
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

	supply := New(ctx, n1.Host, n1.Dt)

	providers := make(map[peer.ID]*testutil.TestNode)
	var supplies []*Supply
	// We add a bunch of retrieval providers
	for i := 0; i < 10; i++ {
		n := testutil.NewTestNode(mn, t)
		n.SetupDataTransfer(bgCtx, t)

		// Create a supply for each node
		s := New(ctx, n.Host, n.Dt)

		providers[n.Host.ID()] = n
		supplies = append(supplies, s)
	}

	err := mn.LinkAll()
	require.NoError(t, err)

	err = mn.ConnectAllButSelf()
	require.NoError(t, err)

	done := make(chan bool, 1)
	go func(ctx context.Context, c cid.Cid, b []byte) {
		err := supply.SendAddRequest(ctx, rootCid, uint64(len(b)))
		require.NoError(t, err)

		done <- true

	}(ctx, rootCid, origBytes)

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
