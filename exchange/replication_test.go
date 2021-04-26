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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
		idx, err := NewIndex(n.Ds, n.Ms)
		require.NoError(t, err)
		repl := NewReplication(
			n.Host,
			idx,
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

	time.Sleep(time.Second)

	// 1) D write
	{
		fname := nD.CreateRandomFile(t, 256000)
		link, storeID, _ := nD.LoadFileToNewStore(ctx, t, fname)
		rootCid := link.(cidlink.Link).Cid
		require.NoError(t, rD.idx.SetRef(&DataRef{
			PayloadCID: rootCid,
			StoreID:    storeID,
		}))
		opts := DefaultDispatchOptions
		opts.RF = 3
		res := rD.Dispatch(rootCid, uint64(256000), opts)
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
		require.NoError(t, rF.idx.SetRef(&DataRef{
			PayloadCID: rootCid,
			StoreID:    storeID,
		}))
		opts := DefaultDispatchOptions
		opts.RF = 4
		res := rF.Dispatch(rootCid, uint64(256000), opts)
		for r := range res {
			switch r.Provider {
			case names["E"], names["D"], names["C"], names["B"]:
			default:
				t.Fatal("wrong peer")
			}
		}
	}

	// New node G joins the network
	nG, _ := setupNode("G")
	isubG, err := nG.Host.EventBus().Subscribe(new(IndexEvt), eventbus.BufSize(16))
	require.NoError(t, err)

	testutil.Connect(nC, nG)
	testutil.Connect(nF, nG)
	testutil.Connect(nB, nG)
	testutil.Connect(nA, nG)

	time.Sleep(time.Second)

	// We should be receiving 3 indexes
	for i := 0; i < 3; i++ {
		select {
		case <-isubG.Out():
		case <-ctx.Done():
			t.Fatal("G could not receive all indexes")
		}
	}
	isubG.Close()

	// 3) B write
	{
		fname := nB.CreateRandomFile(t, 256000)
		link, storeID, _ := nB.LoadFileToNewStore(ctx, t, fname)
		rootCid := link.(cidlink.Link).Cid
		require.NoError(t, rB.idx.SetRef(&DataRef{
			PayloadCID: rootCid,
			StoreID:    storeID,
		}))
		opts := DefaultDispatchOptions
		opts.RF = 4
		res := rB.Dispatch(rootCid, uint64(256000), opts)
		for r := range res {
			switch r.Provider {
			case names["C"], names["F"], names["G"], names["A"]:
			default:
				t.Fatal("wrong peer")
			}
		}
	}

	// New node H joins the network
	nH, rH := setupNode("H")
	isubH, err := nH.Host.EventBus().Subscribe(new(IndexEvt), eventbus.BufSize(16))
	require.NoError(t, err)

	testutil.Connect(nB, nH)
	testutil.Connect(nG, nH)
	testutil.Connect(nA, nH)

	time.Sleep(time.Second)

	// We should be receiving 3 indexes
	for i := 0; i < 3; i++ {
		select {
		case <-isubH.Out():
		case <-ctx.Done():
			t.Fatal("H could not receive all indexes")
		}
	}
	isubH.Close()

	// 4) H write
	{
		fname := nH.CreateRandomFile(t, 256000)
		link, storeID, _ := nH.LoadFileToNewStore(ctx, t, fname)
		rootCid := link.(cidlink.Link).Cid
		require.NoError(t, rH.idx.SetRef(&DataRef{
			PayloadCID: rootCid,
			StoreID:    storeID,
		}))
		opts := DefaultDispatchOptions
		opts.RF = 3
		res := rH.Dispatch(rootCid, uint64(256000), opts)
		for r := range res {
			switch r.Provider {
			case names["A"], names["B"], names["G"]:
			default:
				t.Fatal("wrong peer")
			}
		}
	}
}

func TestMultiDispatchStreams(t *testing.T) {
	// Loop is useful for detecting any flakiness
	for i := 0; i < 1; i++ {
		t.Run(fmt.Sprintf("Run %d", i), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

			idx, err := NewIndex(n1.Ds, n1.Ms)
			require.NoError(t, err)
			hn := NewReplication(n1.Host, idx, n1.Dt, regions)
			require.NoError(t, idx.SetRef(&DataRef{
				PayloadCID: rootCid,
				StoreID:    storeID,
			}))
			sub, err := hn.h.EventBus().Subscribe(new(HeyEvt), eventbus.BufSize(16))
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
				idx, err := NewIndex(tnode.Ds, tnode.Ms)
				require.NoError(t, err)
				hn1 := NewReplication(tnode.Host, idx, tnode.Dt, regions)
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

			res := hn.Dispatch(rootCid, uint64(len(origBytes)), DefaultDispatchOptions)

			var recs []PRecord
			for rec := range res {
				recs = append(recs, rec)
			}
			require.Equal(t, len(recs), 7)

			time.Sleep(time.Second)
			for _, r := range recs {
				store, err := receivers[r.Provider].idx.GetStore(rootCid)
				require.NoError(t, err)
				tnds[r.Provider].VerifyFileTransferred(ctx, t, store.DAG, rootCid, origBytes)
			}

		})
	}
}

// In some rare cases where our node isn't connected to any peer we should still
// be able to fail gracefully
func TestSendDispatchNoPeers(t *testing.T) {
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

	idx, err := NewIndex(n1.Ds, n1.Ms)
	require.NoError(t, err)
	supply := NewReplication(n1.Host, idx, n1.Dt, regions)
	require.NoError(t, idx.SetRef(&DataRef{
		PayloadCID: rootCid,
		StoreID:    storeID,
	}))
	require.NoError(t, supply.Start(bgCtx))

	options := DispatchOptions{
		BackoffMin:     10 * time.Millisecond,
		BackoffAttemps: 4,
		RF:             5,
	}
	res := supply.Dispatch(rootCid, uint64(len(origBytes)), options)
	for range res {
	}
}

// The role of this test is to make sure we never dispatch content to unwanted regions
func TestSendDispatchDiffRegions(t *testing.T) {
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

	idx, err := NewIndex(n1.Ds, n1.Ms)
	require.NoError(t, err)
	supply := NewReplication(n1.Host, idx, n1.Dt, asia)
	sub, err := n1.Host.EventBus().Subscribe(new(HeyEvt), eventbus.BufSize(16))
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

		idx, err := NewIndex(n.Ds, n.Ms)
		require.NoError(t, err)
		s := NewReplication(n.Host, idx, n.Dt, asia)
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

		idx, err := NewIndex(n.Ds, n.Ms)
		require.NoError(t, err)

		s := NewReplication(n.Host, idx, n.Dt, africa)
		require.NoError(t, s.Start(ctx))

		africaNodes[n.Host.ID()] = n
		africaSupplies = append(africaSupplies, s)
	}

	err = mn.LinkAll()
	require.NoError(t, err)

	err = mn.ConnectAllButSelf()
	require.NoError(t, err)

	require.NoError(t, idx.SetRef(&DataRef{
		PayloadCID: rootCid,
		StoreID:    storeID,
	}))

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
	res := supply.Dispatch(rootCid, uint64(len(origBytes)), options)

	var recipients []PRecord
	for rec := range res {
		recipients = append(recipients, rec)
	}
	for _, p := range recipients {
		store, err := asiaSupplies[p.Provider].idx.GetStore(rootCid)
		require.NoError(t, err)

		asiaNodes[p.Provider].VerifyFileTransferred(ctx, t, store.DAG, rootCid, origBytes)
	}
	require.Equal(t, 5, len(recipients))
}
