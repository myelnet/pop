package exchange

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/ipfs/go-cid"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	"github.com/libp2p/go-eventbus"
	connmgr "github.com/libp2p/go-libp2p-connmgr"
	peer "github.com/libp2p/go-libp2p-core/peer"
	swarmt "github.com/libp2p/go-libp2p-swarm/testing"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/myelnet/pop/internal/testutil"
	"github.com/myelnet/pop/internal/utils"
	sel "github.com/myelnet/pop/selectors"
	"github.com/stretchr/testify/require"
	bhost "github.com/tchardin/go-libp2p-blankhost"
)

type mockRetriever struct {
	dt      datatransfer.Manager
	idx     *Index
	mu      sync.Mutex
	routing map[cid.Cid]peer.ID
}

// The NewMockRetriever doesn't use multi stores, it loads and retrieves directly from the global blockstore
func NewMockRetriever(dt datatransfer.Manager, idx *Index) *mockRetriever {
	dt.RegisterVoucherType(&testutil.FakeDTType{}, &testutil.FakeDTValidator{})
	return &mockRetriever{
		dt:      dt,
		idx:     idx,
		routing: make(map[cid.Cid]peer.ID),
	}
}

func (mr *mockRetriever) SetRoute(k cid.Cid, p peer.ID) {
	mr.mu.Lock()
	mr.routing[k] = p
	mr.mu.Unlock()
}

func (mr *mockRetriever) SetTable(t map[cid.Cid]peer.ID) {
	mr.mu.Lock()
	mr.routing = t
	mr.mu.Unlock()
}

func (mr *mockRetriever) FindAndRetrieve(ctx context.Context, l cid.Cid) error {
	mr.mu.Lock()
	peer, ok := mr.routing[l]
	mr.mu.Unlock()
	if !ok {
		panic("fail to find provider in mock routing")
	}
	chid, err := mr.dt.OpenPullDataChannel(ctx, peer, &testutil.FakeDTType{Data: l.String()}, l, sel.All())
	if err != nil {
		return err
	}
	for {
		chState, err := mr.dt.ChannelState(ctx, chid)
		if err != nil {
			return err
		}

		switch chState.Status() {
		case datatransfer.Completed:
			return mr.idx.SetRef(&DataRef{
				PayloadCID:  l,
				PayloadSize: int64(256000),
			})
		case datatransfer.Failed, datatransfer.Cancelled:
			return fmt.Errorf(chState.Message())
		}
	}
}

func TestReplication(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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
	setupNode := func(name string) (*testutil.TestNode, *Replication, *mockRetriever) {
		n := testutil.NewTestNode(mn, t, withSwarmT)
		names[name] = n.Host.ID()
		n.SetupDataTransfer(ctx, t)
		idx, err := NewIndex(n.Ds, WithBounds(2000000, 1800000))
		require.NoError(t, err)
		rtv := NewMockRetriever(n.Dt, idx)
		repl := NewReplication(
			n.Host,
			idx,
			n.Dt,
			rtv,
			Options{
				Regions:      []Region{global},
				ReplInterval: 2 * time.Second,
				MultiStore:   n.Ms,
				Blockstore:   n.Bs,
			},
		)
		require.NoError(t, repl.Start(ctx))
		return n, repl, rtv
	}

	// Topology:
	/*
	   A -- B -- C -- D
	   | \/ | \/ | \/ |
	   | /\	| /\ | /\ |
	   H -- G -- F -- E
	*/

	nA, _, _ := setupNode("A")

	nB, rB, _ := setupNode("B")

	testutil.Connect(nA, nB)

	nC, _, _ := setupNode("C")

	testutil.Connect(nB, nC)

	nD, rD, _ := setupNode("D")

	testutil.Connect(nC, nD)

	nE, _, _ := setupNode("E")

	testutil.Connect(nD, nE)
	testutil.Connect(nC, nE)

	nF, rF, _ := setupNode("F")

	testutil.Connect(nD, nF)
	testutil.Connect(nE, nF)
	testutil.Connect(nC, nF)
	testutil.Connect(nB, nF)

	time.Sleep(time.Second)

	// 1) D write
	fnameD := nD.CreateRandomFile(t, 256000)
	linkD, storeIDD, _ := nD.LoadFileToNewStore(ctx, t, fnameD)
	rootCidD := linkD.(cidlink.Link).Cid
	require.NoError(t, rD.idx.SetRef(&DataRef{
		PayloadCID:  rootCidD,
		PayloadSize: int64(256000),
	}))
	optsD := DefaultDispatchOptions
	optsD.RF = 3
	optsD.StoreID = storeIDD
	resD, err := rD.Dispatch(rootCidD, uint64(256000), optsD)
	require.NoError(t, err)
	for r := range resD {
		switch r.Provider {
		case names["C"], names["E"], names["F"]:
		default:
			t.Fatal("sent to wrong peer")
		}
	}
	// Must migrate dispatched content to global store afterwards
	store, err := nD.Ms.Get(storeIDD)
	require.NoError(t, err)
	require.NoError(t, utils.MigrateBlocks(ctx, store.Bstore, nD.Bs))
	require.NoError(t, nD.Ms.Delete(storeIDD))

	// 2) F write
	fnameF := nF.CreateRandomFile(t, 256000)
	linkF, storeIDF, _ := nF.LoadFileToNewStore(ctx, t, fnameF)
	rootCidF := linkF.(cidlink.Link).Cid
	require.NoError(t, rF.idx.SetRef(&DataRef{
		PayloadCID:  rootCidF,
		PayloadSize: int64(256000),
	}))
	optsF := DefaultDispatchOptions
	optsF.RF = 4
	optsF.StoreID = storeIDF
	resF, err := rF.Dispatch(rootCidF, uint64(256000), optsF)
	require.NoError(t, err)
	for r := range resF {
		switch r.Provider {
		case names["E"], names["D"], names["C"], names["B"]:
		default:
			t.Fatal("wrong peer")
		}
	}
	// Must migrate dispatched content to global store afterwards
	store, err = nF.Ms.Get(storeIDF)
	require.NoError(t, err)
	require.NoError(t, utils.MigrateBlocks(ctx, store.Bstore, nF.Bs))
	require.NoError(t, nF.Ms.Delete(storeIDF))

	// New node G joins the network
	nG, _, rtvG := setupNode("G")
	isubG, err := nG.Host.EventBus().Subscribe(new(IndexEvt), eventbus.BufSize(16))
	require.NoError(t, err)

	// Set the content routing
	rtvG.SetRoute(rootCidD, nC.Host.ID())
	rtvG.SetRoute(rootCidF, nF.Host.ID())

	testutil.Connect(nC, nG)
	testutil.Connect(nF, nG)
	testutil.Connect(nB, nG)
	testutil.Connect(nA, nG)

	time.Sleep(time.Second)

	// We should be loading 2 CIDs
	for i := 0; i < 2; i++ {
		select {
		case <-isubG.Out():
		case <-ctx.Done():
			t.Fatal("G could not receive all indexes")
		}
	}
	isubG.Close()

	// 3) B write
	fnameB := nB.CreateRandomFile(t, 256000)
	linkB, storeIDB, _ := nB.LoadFileToNewStore(ctx, t, fnameB)
	rootCidB := linkB.(cidlink.Link).Cid
	require.NoError(t, rB.idx.SetRef(&DataRef{
		PayloadCID:  rootCidB,
		PayloadSize: int64(256000),
	}))
	optsB := DefaultDispatchOptions
	optsB.RF = 4
	optsB.StoreID = storeIDB
	resB, err := rB.Dispatch(rootCidB, uint64(256000), optsB)
	require.NoError(t, err)
	for r := range resB {
		switch r.Provider {
		case names["C"], names["F"], names["G"], names["A"]:
		default:
			t.Fatal("wrong peer")
		}
	}

	// New node H joins the network
	nH, rH, rtvH := setupNode("H")
	isubH, err := nH.Host.EventBus().Subscribe(new(IndexEvt), eventbus.BufSize(16))
	require.NoError(t, err)

	// Set the content routing
	rtvH.SetRoute(rootCidD, nG.Host.ID())
	rtvH.SetRoute(rootCidF, nB.Host.ID())
	rtvH.SetRoute(rootCidB, nA.Host.ID())

	testutil.Connect(nB, nH)
	testutil.Connect(nG, nH)
	testutil.Connect(nA, nH)

	time.Sleep(time.Second)

	// We should be receiving 3 CIDs
	for i := 0; i < 3; i++ {
		select {
		case <-isubH.Out():
		case <-ctx.Done():
			t.Fatal("H could not receive all indexes")
		}
	}
	isubH.Close()

	// 4) H write
	fnameH := nH.CreateRandomFile(t, 256000)
	linkH, storeIDH, _ := nH.LoadFileToNewStore(ctx, t, fnameH)
	rootCidH := linkH.(cidlink.Link).Cid
	require.NoError(t, rH.idx.SetRef(&DataRef{
		PayloadCID:  rootCidH,
		PayloadSize: int64(256000),
	}))
	optsH := DefaultDispatchOptions
	optsH.RF = 3
	optsH.StoreID = storeIDH
	resH, err := rH.Dispatch(rootCidH, uint64(256000), optsH)
	require.NoError(t, err)
	for r := range resH {
		switch r.Provider {
		case names["A"], names["B"], names["G"]:
		default:
			t.Fatal("wrong peer")
		}
	}
}

func TestConcurrentReplication(t *testing.T) {
	// @BUG: this is very racy as well
	t.Skip()
	testCases := []struct {
		name string
		tx   int
		p1   int
		p2   int
	}{
		{
			name: "Single p2",
			tx:   5,
			p1:   2,
			p2:   1,
		},
		{
			name: "Many p2",
			tx:   5,
			p1:   2,
			p2:   5,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			bgCtx := context.Background()

			ctx, cancel := context.WithTimeout(bgCtx, 10*time.Second)
			defer cancel()

			mn := mocknet.New(bgCtx)

			newNode := func() (*testutil.TestNode, *Replication, *mockRetriever) {
				n := testutil.NewTestNode(mn, t)
				n.SetupDataTransfer(ctx, t)
				idx, err := NewIndex(n.Ds, WithBounds(8000000, 7800000))
				require.NoError(t, err)
				rtv := NewMockRetriever(n.Dt, idx)
				repl := NewReplication(
					n.Host,
					idx,
					n.Dt,
					rtv,
					Options{
						Regions:      []Region{global},
						ReplInterval: 3 * time.Second,
						MultiStore:   n.Ms,
						Blockstore:   n.Bs,
					},
				)
				require.NoError(t, repl.Start(ctx))
				return n, repl, rtv
			}

			nodes := make([]*testutil.TestNode, tc.p1)
			repls := make([]*Replication, tc.p1)
			for i := 0; i < tc.p1; i++ {
				nodes[i], repls[i], _ = newNode()
			}

			require.NoError(t, mn.LinkAll())
			require.NoError(t, mn.ConnectAllButSelf())

			content := make(map[cid.Cid][]byte)
			// manual routing table
			routing := make(map[cid.Cid]peer.ID)

			for i := 0; i < tc.p1; i++ {
				// Create 5 new transactions
				for j := 0; j < tc.tx; j++ {
					// The peer manager has time to fill up while we load this file
					fname := nodes[i].CreateRandomFile(t, 128000)
					link, storeID, bytes := nodes[i].LoadFileToNewStore(ctx, t, fname)
					rootCid := link.(cidlink.Link).Cid
					require.NoError(t, repls[i].idx.SetRef(&DataRef{
						PayloadCID:  rootCid,
						PayloadSize: int64(128000),
					}))
					opts := DefaultDispatchOptions
					opts.RF = tc.p1 - 1
					opts.StoreID = storeID
					res, err := repls[i].Dispatch(rootCid, uint64(128000), opts)
					require.NoError(t, err)
					for range res {
					}
					// Must migrate dispatched content to global store afterwards
					store, err := nodes[i].Ms.Get(storeID)
					require.NoError(t, err)
					require.NoError(t, utils.MigrateBlocks(ctx, store.Bstore, nodes[i].Bs))
					require.NoError(t, nodes[i].Ms.Delete(storeID))

					content[rootCid] = bytes
					routing[rootCid] = nodes[i].Host.ID()
				}
			}

			var wg sync.WaitGroup
			for i := 0; i < tc.p2; i++ {
				t := t
				wg.Add(1)
				go func() {
					defer wg.Done()
					// new node joins the network
					node, repl, rtv := newNode()
					rtv.SetTable(routing)
					isub, err := node.Host.EventBus().Subscribe(new(IndexEvt), eventbus.BufSize(16))
					require.NoError(t, err)

					// Randomize when peers connect
					time.Sleep(time.Duration(float64(time.Second) * rand.Float64()))

					require.NoError(t, mn.LinkAll())
					require.NoError(t, mn.ConnectAllButSelf())

					for i := 0; i < tc.tx*tc.p1; i++ {
						select {
						case <-isub.Out():
						case <-ctx.Done():
							t.Fatal("did not receive all the content from replication")
						}
					}

					time.Sleep(time.Second)

					for k, b := range content {
						// Now we fetch it again from our providers
						_, err := repl.idx.GetRef(k)
						require.NoError(t, err)
						node.VerifyFileTransferred(ctx, t, node.DAG, k, b)
					}
				}()
			}
			wg.Wait()
		})
	}

}

func TestMultiDispatchStreams(t *testing.T) {
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
	opts := Options{Regions: regions, MultiStore: n1.Ms, Blockstore: n1.Bs}

	idx, err := NewIndex(n1.Ds)
	require.NoError(t, err)
	hn := NewReplication(n1.Host, idx, n1.Dt, NewMockRetriever(n1.Dt, idx), opts)
	require.NoError(t, idx.SetRef(&DataRef{
		PayloadCID:  rootCid,
		PayloadSize: int64(256000),
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
		idx, err := NewIndex(tnode.Ds)
		require.NoError(t, err)
		opts := Options{Regions: regions, MultiStore: tnode.Ms, Blockstore: tnode.Bs}
		hn1 := NewReplication(tnode.Host, idx, tnode.Dt, NewMockRetriever(tnode.Dt, idx), opts)
		require.NoError(t, hn1.Start(ctx))
		receivers[tnode.Host.ID()] = hn1
		tnds[tnode.Host.ID()] = tnode
	}

	err = mn.LinkAll()
	require.NoError(t, err)

	err = mn.ConnectAllButSelf()
	require.NoError(t, err)

	time.Sleep(time.Second)

	// Wait for all peers to be received in the peer manager
	for i := 0; i < 7; i++ {
		select {
		case <-sub.Out():
		case <-ctx.Done():
			t.Fatal("all peers didn't get in the peermgr")
		}
	}

	dopts := DefaultDispatchOptions
	dopts.StoreID = storeID
	res, err := hn.Dispatch(rootCid, uint64(len(origBytes)), dopts)
	require.NoError(t, err)

	var recs []PRecord
	for rec := range res {
		recs = append(recs, rec)
	}
	require.Equal(t, len(recs), 6)

	time.Sleep(time.Second)
	for _, r := range recs {
		p := tnds[r.Provider]
		p.VerifyFileTransferred(ctx, t, p.DAG, rootCid, origBytes)
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
	opts := Options{Regions: regions, MultiStore: n1.Ms, Blockstore: n1.Bs}

	idx, err := NewIndex(n1.Ds)
	require.NoError(t, err)
	supply := NewReplication(n1.Host, idx, n1.Dt, NewMockRetriever(n1.Dt, idx), opts)
	require.NoError(t, idx.SetRef(&DataRef{
		PayloadCID:  rootCid,
		PayloadSize: int64(256000),
	}))
	require.NoError(t, supply.Start(bgCtx))

	options := DispatchOptions{
		BackoffMin:     10 * time.Millisecond,
		BackoffAttemps: 4,
		RF:             5,
		StoreID:        storeID,
	}
	res, err := supply.Dispatch(rootCid, uint64(len(origBytes)), options)
	require.NoError(t, err)
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

	idx, err := NewIndex(n1.Ds)
	require.NoError(t, err)
	supply := NewReplication(
		n1.Host,
		idx,
		n1.Dt,
		NewMockRetriever(n1.Dt, idx),
		Options{Regions: asia, MultiStore: n1.Ms, Blockstore: n1.Bs},
	)
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

		idx, err := NewIndex(n.Ds)
		require.NoError(t, err)
		s := NewReplication(
			n.Host,
			idx,
			n.Dt,
			NewMockRetriever(n1.Dt, idx),
			Options{Regions: asia, MultiStore: n.Ms, Blockstore: n.Bs},
		)
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

		idx, err := NewIndex(n.Ds)
		require.NoError(t, err)

		s := NewReplication(
			n.Host,
			idx,
			n.Dt,
			NewMockRetriever(n.Dt, idx),
			Options{Regions: africa, MultiStore: n.Ms, Blockstore: n.Bs},
		)
		require.NoError(t, s.Start(ctx))

		africaNodes[n.Host.ID()] = n
		africaSupplies = append(africaSupplies, s)
	}

	err = mn.LinkAll()
	require.NoError(t, err)

	err = mn.ConnectAllButSelf()
	require.NoError(t, err)

	time.Sleep(time.Second)

	require.NoError(t, idx.SetRef(&DataRef{
		PayloadCID:  rootCid,
		PayloadSize: int64(512000),
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
		BackoffMin:     5 * time.Second,
		BackoffAttemps: 4,
		RF:             7,
		StoreID:        storeID,
	}
	res, err := supply.Dispatch(rootCid, uint64(len(origBytes)), options)
	require.NoError(t, err)

	var recipients []PRecord
	for rec := range res {
		recipients = append(recipients, rec)
	}
	for _, p := range recipients {
		node := asiaNodes[p.Provider]
		node.VerifyFileTransferred(ctx, t, node.DAG, rootCid, origBytes)
	}
	require.Equal(t, 5, len(recipients))
}

func TestPeerMgr(t *testing.T) {

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	mn := mocknet.New(ctx)

	regions := []Region{
		{
			Name: "TestRegion",
			Code: CustomRegion,
		},
	}

	tnds := make(map[peer.ID]*testutil.TestNode)
	receivers := make([]*Replication, 11)

	for i := 0; i < 11; i++ {
		tnode := testutil.NewTestNode(mn, t)
		tnode.SetupDataTransfer(ctx, t)
		t.Cleanup(func() {
			err := tnode.Dt.Stop(ctx)
			require.NoError(t, err)
		})
		idx, err := NewIndex(tnode.Ds)
		require.NoError(t, err)
		opts := Options{Regions: regions, MultiStore: tnode.Ms, Blockstore: tnode.Bs}
		hn1 := NewReplication(tnode.Host, idx, tnode.Dt, NewMockRetriever(tnode.Dt, idx), opts)
		require.NoError(t, hn1.Start(ctx))
		receivers[i] = hn1
		tnds[tnode.Host.ID()] = tnode
	}

	require.NoError(t, mn.LinkAll())

	require.NoError(t, mn.ConnectAllButSelf())

	time.Sleep(time.Second)

	repl := receivers[0]
	ignore := make(map[peer.ID]bool)

	peers := repl.pm.Peers(6, regions, ignore)
	require.Equal(t, 6, len(peers))

	// Let's ignore a couple
	ignore[peers[0]] = true
	ignore[peers[1]] = true

	peers2 := repl.pm.Peers(6, regions, ignore)
	require.Equal(t, 6, len(peers2))
	for _, pid := range peers2 {
		if pid == peers[0] || pid == peers[1] {
			t.Fatal("Peers did not ignore the right peers")
		}
	}

	// Try to get more than available
	peers3 := repl.pm.Peers(10, regions, ignore)
	require.Equal(t, 8, len(peers3))

	// 0 peers should return 0 peers
	peers4 := repl.pm.Peers(0, regions, ignore)
	require.Equal(t, 0, len(peers4))
}
