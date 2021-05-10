package exchange

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/filecoin-project/go-state-types/abi"
	"github.com/ipfs/go-cid"
	keystore "github.com/ipfs/go-ipfs-keystore"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	"github.com/libp2p/go-eventbus"
	peer "github.com/libp2p/go-libp2p-core/peer"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/myelnet/pop/internal/testutil"
	"github.com/myelnet/pop/retrieval/deal"
	sel "github.com/myelnet/pop/selectors"
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
					wq.ReceiveResponse(peer.AddrInfo{ID: peer.ID(fmt.Sprintf("%d", i))}, deal.QueryResponse{
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
					wq.ReceiveResponse(peer.AddrInfo{ID: peer.ID(fmt.Sprintf("%d", i))}, deal.QueryResponse{
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

			time.Sleep(time.Second)

			// The peer manager has time to fill up while we load this file
			fname := cnode.CreateRandomFile(t, 256000)
			link, storeID, origBytes := cnode.LoadFileToNewStore(ctx, t, fname)
			rootCid := link.(cidlink.Link).Cid
			require.NoError(t, client.Index().SetRef(&DataRef{
				PayloadCID:  rootCid,
				StoreID:     storeID,
				PayloadSize: int64(len(origBytes)),
			}))

			// In this test we expect the maximum of providers to receive the content
			// that may not be the case in the real world
			res := client.R().Dispatch(rootCid, uint64(len(origBytes)), DefaultDispatchOptions)

			var records []PRecord
			for rec := range res {
				records = append(records, rec)
			}
			require.Equal(t, 6, len(records))

			// Must had a delay here because go-data-transfer is racy af
			time.Sleep(time.Second)

			// Gather and check all the recipients have a proper copy of the file
			for _, r := range records {
				store, err := providers[r.Provider].Index().GetStore(rootCid)
				require.NoError(t, err)
				pnodes[r.Provider].VerifyFileTransferred(ctx, t, store.DAG, rootCid, origBytes)
			}

			err := client.Index().DropRef(rootCid)
			require.NoError(t, err)

			// Sanity check to make sure our client does not have a copy of our blocks
			_, err = client.Index().GetStore(rootCid)
			require.Error(t, err)

			// Now we fetch it again from our providers
			tx := client.Tx(ctx, WithRoot(rootCid), WithStrategy(SelectFirst), WithTriage())
			defer tx.Close()

			err = tx.Query(sel.All())
			require.NoError(t, err)

			selected, err := tx.Triage()
			require.NoError(t, err)

			selected.Incline()

			ref := <-tx.Ongoing()
			require.NoError(t, err)
			require.NotEqual(t, ref.ID, deal.ID(0))

			select {
			case res := <-tx.Done():
				require.NoError(t, res.Err)
			case <-ctx.Done():
				t.Fatal("failed to finish sync")
			}

			store, err := cnode.Ms.Get(tx.StoreID())
			require.NoError(t, err)
			// And we verify we got the file back
			cnode.VerifyFileTransferred(ctx, t, store.DAG, rootCid, origBytes)
		})
	}
}

// The goal of this test is to simulate the process of a brand new node joining
// 2 other existing nodes on the network. It demonstrates the ability of the new nodes
// to automatically fill the index with existing content.
func TestExchangeJoiningNetwork(t *testing.T) {
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
			p2:   2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// FIXME: something is broken in concurrent deal handling
			if tc.name == "Many p2" {
				t.Skip()
			}
			bgCtx := context.Background()

			ctx, cancel := context.WithTimeout(bgCtx, 10*time.Second)
			defer cancel()

			mn := mocknet.New(bgCtx)

			newNode := func() (*Exchange, *testutil.TestNode) {
				n := testutil.NewTestNode(mn, t)
				opts := Options{
					Blockstore:  n.Bs,
					MultiStore:  n.Ms,
					RepoPath:    n.DTTmpDir,
					Keystore:    keystore.NewMemKeystore(),
					RepInterval: time.Second,
				}
				exch, err := New(bgCtx, n.Host, n.Ds, opts)
				require.NoError(t, err)
				return exch, n
			}

			nodes := make([]*testutil.TestNode, tc.p1)
			exchs := make([]*Exchange, tc.p1)
			for i := 0; i < tc.p1; i++ {
				exchs[i], nodes[i] = newNode()
			}

			require.NoError(t, mn.LinkAll())
			require.NoError(t, mn.ConnectAllButSelf())

			content := make(map[string]cid.Cid)

			for i := 0; i < tc.p1; i++ {

				// Create a given number of transactions
				for j := 0; j < tc.tx; j++ {
					// The peer manager has time to fill up while we load this file
					fname := nodes[i].CreateRandomFile(t, 128000)

					ptx := exchs[i].Tx(ctx)
					require.NoError(t, ptx.PutFile(fname))
					ptx.SetCacheRF(1)
					require.NoError(t, ptx.Commit())
					ptx.WatchDispatch(func(rec PRecord) {
						// No need to check
					})
					content[KeyFromPath(fname)] = ptx.Root()
					ptx.Close()
				}
			}

			var wg sync.WaitGroup
			for i := 0; i < tc.p2; i++ {
				t := t
				wg.Add(1)
				go func() {
					defer wg.Done()
					// new node joins the network
					ex, node := newNode()
					isub, err := node.Host.EventBus().Subscribe(new(IndexEvt), eventbus.BufSize(16))
					require.NoError(t, err)

					// Randomize when peers connect
					time.Sleep(time.Duration(float64(time.Second) * rand.Float64()))

					require.NoError(t, mn.LinkAll())
					require.NoError(t, mn.ConnectAllButSelf())

					time.Sleep(2 * time.Second)

					for i := 0; i < 5; i++ {
						select {
						case <-isub.Out():
						case <-ctx.Done():
							t.Fatal("did not receive all the content from replication")
						}
					}

					time.Sleep(time.Second)

					for k, r := range content {
						// Now we fetch it again from our providers
						tx := ex.Tx(ctx, WithRoot(r))
						_, err := tx.GetFile(k)
						require.NoError(t, err)
						tx.Close()
					}
				}()
			}
			wg.Wait()
		})
	}
}
