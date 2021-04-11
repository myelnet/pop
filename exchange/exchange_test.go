package exchange

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/filecoin-project/go-state-types/abi"
	cid "github.com/ipfs/go-cid"
	keystore "github.com/ipfs/go-ipfs-keystore"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	bhost "github.com/libp2p/go-libp2p-blankhost"
	peer "github.com/libp2p/go-libp2p-core/peer"
	swarmt "github.com/libp2p/go-libp2p-swarm/testing"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/myelnet/pop/internal/testutil"
	"github.com/myelnet/pop/retrieval/deal"
	"github.com/stretchr/testify/require"
)

type testExecutor struct {
	done chan deal.Offer
	conf chan deal.Offer
	err  chan error
}

func (te testExecutor) SetError(err error) {
	te.err <- err
}

func (te testExecutor) Execute(o deal.Offer) error {
	err := <-te.err
	if err != nil {
		return err
	}
	te.done <- o
	return nil
}

func (te testExecutor) Confirm(o deal.Offer) bool {
	select {
	case te.conf <- o:
	default:
	}
	return true
}

func TestSelectionStrategies(t *testing.T) {

	testCases := []struct {
		name     string
		strategy SelectionStrategy
		offers   int
		failures int
	}{
		{
			name:     "SelectFirst",
			strategy: SelectFirst,
			offers:   11,
			failures: 0,
		},
		{
			name:     "SelectFirst failing",
			strategy: SelectFirst,
			offers:   11,
			failures: 3,
		},
		{
			name:     "SelectCheapest count threshold",
			strategy: SelectCheapest(5, 5*time.Second),
			offers:   11,
			failures: 0,
		},
		{
			name:     "SelectCheapest count threshold failing",
			strategy: SelectCheapest(5, 5*time.Second),
			offers:   11,
			failures: 2,
		},
		{
			name:     "SelectCheapest time threshold",
			strategy: SelectCheapest(20, 1*time.Second),
			offers:   11,
			failures: 0,
		},
		{
			name:     "SelectCheapest time threshold failing",
			strategy: SelectCheapest(20, 1*time.Second),
			offers:   11,
			failures: 4,
		},
		{
			name:     "SelectFirstLowerThan",
			strategy: SelectFirstLowerThan(abi.NewTokenAmount(5)),
			offers:   11,
			failures: 0,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			exec := testExecutor{
				done: make(chan deal.Offer, 1),
				err:  make(chan error, 1),
			}
			wq := testCase.strategy(exec)

			for i := 0; i < testCase.offers; i++ {
				i := i
				go func() {
					wq.ReceiveOffer(peer.AddrInfo{ID: peer.ID(fmt.Sprintf("%d", i))}, deal.QueryResponse{
						MinPricePerByte: abi.NewTokenAmount(int64(rand.Intn(10))),
					})
				}()
			}

			wq.Start()

			for i := 0; i < testCase.failures; i++ {
				exec.SetError(errors.New("failing"))
			}
			exec.SetError(nil)

			select {
			case <-exec.done:
			case <-ctx.Done():
				require.NoError(t, ctx.Err())
			}

			_ = wq.Close()
		})
	}
}

// Stress test strategies to make sure they scale well to handle hundreds of offers
func BenchmarkStrategies(b *testing.B) {
	testCases := []struct {
		name     string
		strategy SelectionStrategy
	}{
		{
			name:     "Bench SelectFirst",
			strategy: SelectFirst,
		},
		{
			name:     "Bench SelectCheapest count threshold",
			strategy: SelectCheapest(5, 5*time.Second),
		},
		{
			name:     "Bench SelectCheapest time threshold",
			strategy: SelectCheapest(20, 1*time.Second),
		},
		{
			name:     "Bench SelectFirstLowerThan",
			strategy: SelectFirstLowerThan(abi.NewTokenAmount(5)),
		},
	}
	for _, testCase := range testCases {
		b.Run(testCase.name, func(b *testing.B) {
			b.ReportAllocs()
			exec := testExecutor{
				done: make(chan deal.Offer, 1),
				err:  make(chan error, 1),
			}
			wq := testCase.strategy(exec)

			for i := 0; i < 30+b.N; i++ {
				i := i
				go func() {
					wq.ReceiveOffer(peer.AddrInfo{ID: peer.ID(fmt.Sprintf("%d", i))}, deal.QueryResponse{
						MinPricePerByte: abi.NewTokenAmount(int64(rand.Intn(10))),
					})
				}()
			}

			wq.Start()

			exec.SetError(nil)

			<-exec.done

			_ = wq.Close()
		})
	}

}

type Topology func(*testing.T, mocknet.Mocknet, []*testutil.TestNode, []*testutil.TestNode)

func All(t *testing.T, mn mocknet.Mocknet, rn []*testutil.TestNode, prs []*testutil.TestNode) {
	require.NoError(t, mn.LinkAll())
	require.NoError(t, mn.ConnectAllButSelf())
}

func OneToOne(t *testing.T, mn mocknet.Mocknet, rn []*testutil.TestNode, prs []*testutil.TestNode) {
	require.NoError(t, testutil.Connect(rn[0], prs[0]))
	time.Sleep(time.Second)
}

func Markov(t *testing.T, mn mocknet.Mocknet, rn []*testutil.TestNode, prs []*testutil.TestNode) {
	prevPeer := rn[0]
	var peers []*testutil.TestNode
	peers = append(peers, rn[1:]...)
	peers = append(peers, prs...)
	for _, tn := range peers {
		require.NoError(t, testutil.Connect(prevPeer, tn))
		prevPeer = tn
	}
	time.Sleep(time.Second)
}

func noop(*testutil.TestNode) {}

func TestGossipQuery(t *testing.T) {
	withSwarmT := func(tn *testutil.TestNode) {
		netw := swarmt.GenSwarm(t, context.Background())
		h := bhost.NewBlankHost(netw)
		tn.Host = h
	}
	testCases := []struct {
		name     string
		topology Topology
		peers    int
		clients  int
		files    int
		netOpts  func(*testutil.TestNode)
	}{
		{
			name:     "Connect all",
			topology: All,
			peers:    11,
			clients:  1,
			netOpts:  noop,
			files:    1,
		},
		{
			name:     "Connect all with 2 clients",
			topology: All,
			peers:    11,
			clients:  2,
			netOpts:  noop,
			files:    1,
		},

		{
			name:     "One to one",
			topology: OneToOne,
			peers:    2,
			clients:  1,
			netOpts:  withSwarmT,
			files:    1,
		},
		{
			name:     "Markov",
			topology: Markov,
			peers:    6,
			clients:  1,
			netOpts:  withSwarmT,
			files:    3,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			bgCtx := context.Background()

			ctx, cancel := context.WithTimeout(bgCtx, 5*time.Second)
			defer cancel()

			mn := mocknet.New(bgCtx)

			clients := make(map[peer.ID]*Exchange)
			var cnodes []*testutil.TestNode

			providers := make(map[peer.ID]*Exchange)
			var pnodes []*testutil.TestNode

			fnames := make([]string, testCase.files)
			for i := range fnames {
				// This just creates the file without adding it
				fnames[i] = (&testutil.TestNode{}).CreateRandomFile(t, 256000)
			}
			roots := make([]cid.Cid, testCase.files)

			var rootCid cid.Cid

			for i := 0; i < testCase.peers; i++ {
				n := testutil.NewTestNode(mn, t, testCase.netOpts)
				opts := Options{
					Blockstore: n.Bs,
					MultiStore: n.Ms,
					RepoPath:   n.DTTmpDir,
					Keystore:   keystore.NewMemKeystore(),
				}

				exch, err := New(bgCtx, n.Host, n.Ds, opts)
				require.NoError(t, err)

				if i < testCase.clients {
					clients[n.Host.ID()] = exch
					cnodes = append(cnodes, n)
				} else {
					providers[n.Host.ID()] = exch
					pnodes = append(pnodes, n)

					for i, name := range fnames {
						link, storeID, _ := n.LoadFileToNewStore(ctx, t, name)
						rootCid = link.(cidlink.Link).Cid
						require.NoError(t, exch.Put(ctx, rootCid, PutOptions{
							StoreID: storeID,
							Local:   true,
						}))
						roots[i] = rootCid
					}
				}
			}

			testCase.topology(t, mn, cnodes, pnodes)

			for _, client := range clients {
				for _, root := range roots {
					// Now we fetch it again from our providers
					session := client.NewSession(ctx, root, SelectFirst)

					err := session.QueryGossip(ctx)
					require.NoError(t, err)

					// execute a job for each offer
					for i := 0; i < testCase.peers-testCase.clients; i++ {
						selected, err := session.Checkout()
						require.NoError(t, err)
						require.Equal(t, selected.Offer.Response.Size, uint64(268009))

						// Deny the offer so we don't trigger a retrieval
						selected.Decline()
					}
					session.Close()
				}
			}

		})
	}

}

func TestExchangeE2E(t *testing.T) {
	// Iterating a ton helps weed out false positives
	for i := 0; i < 1; i++ {
		t.Run(fmt.Sprintf("Try %v", i), func(t *testing.T) {
			bgCtx := context.Background()

			ctx, cancel := context.WithTimeout(bgCtx, 10*time.Second)
			defer cancel()

			mn := mocknet.New(bgCtx)

			var client *Exchange
			var cnode *testutil.TestNode
			providers := make(map[peer.ID]*Exchange)
			pnodes := make(map[peer.ID]*testutil.TestNode)

			for i := 0; i < 11; i++ {
				n := testutil.NewTestNode(mn, t)
				opts := Options{
					Blockstore: n.Bs,
					MultiStore: n.Ms,
					RepoPath:   n.DTTmpDir,
					Keystore:   keystore.NewMemKeystore(),
				}
				exch, err := New(bgCtx, n.Host, n.Ds, opts)
				require.NoError(t, err)

				if i == 0 {
					client = exch
					cnode = n
				} else {
					providers[n.Host.ID()] = exch
					pnodes[n.Host.ID()] = n
				}
			}
			require.NoError(t, mn.LinkAll())

			require.NoError(t, mn.ConnectAllButSelf())

			// The peer manager has time to fill up while we load this file
			fname := cnode.CreateRandomFile(t, 256000)
			link, storeID, origBytes := cnode.LoadFileToNewStore(ctx, t, fname)
			rootCid := link.(cidlink.Link).Cid
			require.NoError(t, client.Put(ctx, rootCid, PutOptions{
				StoreID: storeID,
				Local:   true,
			}))

			// In this test we expect the maximum of providers to receive the content
			// that may not be the case in the real world
			res := client.R().DispatchRequest(Request{
				PayloadCID: rootCid,
				Size:       uint64(len(origBytes)),
			}, DefaultDispatchOptions)

			var records []PRecord
			for rec := range res {
				records = append(records, rec)
			}
			require.Equal(t, 7, len(records))

			// Must had a delay here because go-data-transfer is racy af
			time.Sleep(time.Second)

			// Gather and check all the recipients have a proper copy of the file
			for _, r := range records {
				store, err := providers[r.Provider].meta.GetStore(rootCid)
				require.NoError(t, err)
				pnodes[r.Provider].VerifyFileTransferred(ctx, t, store.DAG, rootCid, origBytes)
			}

			err := client.meta.RemoveContent(rootCid)
			require.NoError(t, err)

			// Sanity check to make sure our client does not have a copy of our blocks
			_, err = client.meta.GetStore(rootCid)
			require.Error(t, err)

			// Now we fetch it again from our providers
			session := client.NewSession(ctx, rootCid, SelectFirst)
			defer session.Close()

			err = session.QueryGossip(ctx)
			require.NoError(t, err)

			selected, err := session.Checkout()
			require.NoError(t, err)

			selected.Incline()

			ref := <-session.Ongoing()
			require.NoError(t, err)
			require.NotEqual(t, ref.ID, deal.ID(0))

			select {
			case err := <-session.Done():
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal("failed to finish sync")
			}

			store, err := cnode.Ms.Get(session.StoreID())
			require.NoError(t, err)
			// And we verify we got the file back
			cnode.VerifyFileTransferred(ctx, t, store.DAG, rootCid, origBytes)
		})
	}
}
