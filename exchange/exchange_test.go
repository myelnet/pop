package exchange

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/filecoin-project/go-state-types/abi"
	keystore "github.com/ipfs/go-ipfs-keystore"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	peer "github.com/libp2p/go-libp2p-core/peer"
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

			// The peer manager has time to fill up while we load this file
			fname := cnode.CreateRandomFile(t, 256000)
			link, storeID, origBytes := cnode.LoadFileToNewStore(ctx, t, fname)
			rootCid := link.(cidlink.Link).Cid
			require.NoError(t, client.meta.Register(rootCid, storeID))

			// In this test we expect the maximum of providers to receive the content
			// that may not be the case in the real world
			res := client.R().Dispatch(Request{
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

			err = session.Query(ctx)
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
