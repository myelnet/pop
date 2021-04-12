package exchange

import (
	"context"
	"fmt"
	"testing"
	"time"

	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	"github.com/libp2p/go-eventbus"
	connmgr "github.com/libp2p/go-libp2p-connmgr"
	peer "github.com/libp2p/go-libp2p-core/peer"
	swarmt "github.com/libp2p/go-libp2p-swarm/testing"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/myelnet/pop/internal/testutil"
	"github.com/stretchr/testify/require"
	bhost "github.com/tchardin/go-libp2p-blankhost"
)

func TestReplication(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	mn := mocknet.New(ctx)

	withSwarmT := func(tn *testutil.TestNode) {
		netw := swarmt.GenSwarm(t, context.Background())
		h := bhost.NewBlankHost(netw, bhost.WithConnectionManager(
			connmgr.NewConnManager(10, 11, time.Second),
		))
		tn.Host = h
	}
	names := make(map[string]peer.ID)
	setupNode := func(name string) (*testutil.TestNode, *Replication) {
		n := testutil.NewTestNode(mn, t, withSwarmT)
		names[name] = n.Host.ID()
		n.SetupDataTransfer(ctx, t)
		repl := NewReplication(
			n.Host,
			NewMetadataStore(n.Ds, n.Ms),
			n.Dt,
			[]Region{global},
		)
		require.NoError(t, repl.Start(ctx))
		return n, repl
	}

	// Topology:
	/*
	   A -- B -- C -- D
	   | \/ | \/ | \/ |
	   | /\	| /\ | /\ |
	   H -- G -- F -- E
	*/

	nA, _ := setupNode("A")

	nB, rB := setupNode("B")

	testutil.Connect(nA, nB)

	nC, _ := setupNode("C")

	testutil.Connect(nB, nC)

	nD, rD := setupNode("D")

	testutil.Connect(nC, nD)

	nE, _ := setupNode("E")

	testutil.Connect(nD, nE)
	testutil.Connect(nC, nE)

	nF, rF := setupNode("F")

	testutil.Connect(nD, nF)
	testutil.Connect(nE, nF)
	testutil.Connect(nC, nF)
	testutil.Connect(nB, nF)

	nG, _ := setupNode("G")

	testutil.Connect(nC, nG)
	testutil.Connect(nF, nG)
	testutil.Connect(nB, nG)
	testutil.Connect(nA, nG)

	nH, rH := setupNode("H")

	testutil.Connect(nB, nH)
	testutil.Connect(nG, nH)
	testutil.Connect(nA, nH)

	time.Sleep(time.Second)

	// 1) D write
	{
		fname := nD.CreateRandomFile(t, 256000)
		link, storeID, _ := nD.LoadFileToNewStore(ctx, t, fname)
		rootCid := link.(cidlink.Link).Cid
		require.NoError(t, rD.store.Register(rootCid, storeID))
		opts := DefaultDispatchOptions
		opts.RF = 3
		res := rD.Dispatch(Request{rootCid, uint64(256000)}, opts)
		for r := range res {
			switch r.Provider {
			case names["C"], names["E"], names["F"]:
			default:
				t.Fatal("sent to wrong peer")
			}
		}
	}

	// 2) F write
	{
		fname := nF.CreateRandomFile(t, 256000)
		link, storeID, _ := nF.LoadFileToNewStore(ctx, t, fname)
		rootCid := link.(cidlink.Link).Cid
		require.NoError(t, rF.store.Register(rootCid, storeID))
		opts := DefaultDispatchOptions
		opts.RF = 5
		res := rF.Dispatch(Request{rootCid, uint64(256000)}, opts)
		for r := range res {
			switch r.Provider {
			case names["E"], names["D"], names["C"], names["B"], names["G"]:
			default:
				t.Fatal("wrong peer")
			}
		}
	}

	// 3) B write
	{
		fname := nB.CreateRandomFile(t, 256000)
		link, storeID, _ := nB.LoadFileToNewStore(ctx, t, fname)
		rootCid := link.(cidlink.Link).Cid
		require.NoError(t, rB.store.Register(rootCid, storeID))
		opts := DefaultDispatchOptions
		opts.RF = 5
		res := rB.Dispatch(Request{rootCid, uint64(256000)}, opts)
		for r := range res {
			switch r.Provider {
			case names["C"], names["F"], names["G"], names["H"], names["A"]:
			default:
				t.Fatal("wrong peer")
			}
		}
	}

	// 4) H write
	{
		fname := nH.CreateRandomFile(t, 256000)
		link, storeID, _ := nH.LoadFileToNewStore(ctx, t, fname)
		rootCid := link.(cidlink.Link).Cid
		require.NoError(t, rH.store.Register(rootCid, storeID))
		opts := DefaultDispatchOptions
		opts.RF = 3
		res := rH.Dispatch(Request{rootCid, uint64(256000)}, opts)
		for r := range res {
			switch r.Provider {
			case names["A"], names["B"], names["G"]:
			default:
				t.Fatal("wrong peer")
			}
		}
	}
}

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

			mds := &MetadataStore{n1.Ds, n1.Ms}
			hn := NewReplication(n1.Host, mds, n1.Dt, regions)
			mds.Register(rootCid, storeID)
			sub, err := hn.h.EventBus().Subscribe(new(PeerRegionEvt), eventbus.BufSize(16))
			require.NoError(t, err)
			require.NoError(t, hn.Start(ctx))

			tnds := make(map[peer.ID]*testutil.TestNode)
			receivers := make(map[peer.ID]*Replication)

			for i := 0; i < 7; i++ {
				tnode := testutil.NewTestNode(mn, t)
				tnode.SetupDataTransfer(ctx, t)
				t.Cleanup(func() {
					err := tnode.Dt.Stop(ctx)
					require.NoError(t, err)
				})

				mds := &MetadataStore{tnode.Ds, tnode.Ms}
				hn1 := NewReplication(tnode.Host, mds, tnode.Dt, regions)
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
				store, err := receivers[r.Provider].store.GetStore(rootCid)
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

	mds := &MetadataStore{n1.Ds, n1.Ms}
	supply := NewReplication(n1.Host, mds, n1.Dt, regions)
	mds.Register(rootCid, storeID)
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

	mds := &MetadataStore{n1.Ds, n1.Ms}
	supply := NewReplication(n1.Host, mds, n1.Dt, asia)
	sub, err := n1.Host.EventBus().Subscribe(new(PeerRegionEvt), eventbus.BufSize(16))
	require.NoError(t, err)
	require.NoError(t, supply.Start(ctx))

	asiaNodes := make(map[peer.ID]*testutil.TestNode)
	asiaSupplies := make(map[peer.ID]*Replication)
	// We add a bunch of asian retrieval providers
	for i := 0; i < 5; i++ {
		n := testutil.NewTestNode(mn, t)
		n.SetupDataTransfer(bgCtx, t)
		t.Cleanup(func() {
			err := n.Dt.Stop(ctx)
			require.NoError(t, err)
		})

		// Create a supply for each node
		mds := &MetadataStore{n.Ds, n.Ms}
		s := NewReplication(n.Host, mds, n.Dt, asia)
		require.NoError(t, s.Start(ctx))

		asiaNodes[n.Host.ID()] = n
		asiaSupplies[n.Host.ID()] = s
	}

	africa := []Region{
		Regions["Africa"],
	}

	africaNodes := make(map[peer.ID]*testutil.TestNode)
	var africaSupplies []*Replication
	// Add african providers
	for i := 0; i < 3; i++ {
		n := testutil.NewTestNode(mn, t)
		n.SetupDataTransfer(bgCtx, t)
		t.Cleanup(func() {
			err := n.Dt.Stop(ctx)
			require.NoError(t, err)
		})

		// Create a supply for each node
		mds := &MetadataStore{n.Ds, n.Ms}
		s := NewReplication(n.Host, mds, n.Dt, africa)
		require.NoError(t, s.Start(ctx))

		africaNodes[n.Host.ID()] = n
		africaSupplies = append(africaSupplies, s)
	}

	err = mn.LinkAll()
	require.NoError(t, err)

	err = mn.ConnectAllButSelf()
	require.NoError(t, err)

	require.NoError(t, mds.Register(rootCid, storeID))

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
		store, err := asiaSupplies[p.Provider].store.GetStore(rootCid)
		require.NoError(t, err)

		asiaNodes[p.Provider].VerifyFileTransferred(ctx, t, store.DAG, rootCid, origBytes)
	}
	require.Equal(t, 5, len(recipients))
}
