package pop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/filecoin-project/go-state-types/abi"
	"github.com/ipfs/go-blockservice"
	cid "github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	dss "github.com/ipfs/go-datastore/sync"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	chunk "github.com/ipfs/go-ipfs-chunker"
	offline "github.com/ipfs/go-ipfs-exchange-offline"
	files "github.com/ipfs/go-ipfs-files"
	keystore "github.com/ipfs/go-ipfs-keystore"
	ipldformat "github.com/ipfs/go-ipld-format"
	"github.com/ipfs/go-merkledag"
	"github.com/ipfs/go-unixfs/importer/balanced"
	"github.com/ipfs/go-unixfs/importer/helpers"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	bhost "github.com/libp2p/go-libp2p-blankhost"
	peer "github.com/libp2p/go-libp2p-core/peer"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	swarmt "github.com/libp2p/go-libp2p-swarm/testing"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/myelnet/pop/internal/testutil"
	"github.com/myelnet/pop/retrieval/deal"
	"github.com/myelnet/pop/supply"
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
					wq.Receive(peer.AddrInfo{ID: peer.ID(fmt.Sprintf("%d", i))}, deal.QueryResponse{
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
					wq.Receive(peer.AddrInfo{ID: peer.ID(fmt.Sprintf("%d", i))}, deal.QueryResponse{
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
				n.SetupGraphSync(ctx)
				tracer := NewGossipTracer()
				ps, err := pubsub.NewGossipSub(ctx, n.Host, pubsub.WithEventTracer(tracer))
				require.NoError(t, err)

				settings := Settings{
					Datastore:    n.Ds,
					Blockstore:   n.Bs,
					MultiStore:   n.Ms,
					Host:         n.Host,
					PubSub:       ps,
					GraphSync:    n.Gs,
					RepoPath:     n.DTTmpDir,
					Keystore:     keystore.NewMemKeystore(),
					GossipTracer: tracer,
					Regions:      []supply.Region{supply.Regions["Global"]},
				}

				exch, err := NewExchange(bgCtx, settings)
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
						require.NoError(t, exch.Supply().Register(rootCid, storeID))
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
	for i := 0; i < 11; i++ {
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
				n.SetupGraphSync(ctx)
				tracer := NewGossipTracer()
				ps, err := pubsub.NewGossipSub(ctx, n.Host, pubsub.WithEventTracer(tracer))
				require.NoError(t, err)

				settings := Settings{
					Datastore:    n.Ds,
					Blockstore:   n.Bs,
					MultiStore:   n.Ms,
					Host:         n.Host,
					PubSub:       ps,
					GraphSync:    n.Gs,
					RepoPath:     n.DTTmpDir,
					GossipTracer: tracer,
					Keystore:     keystore.NewMemKeystore(),
					Regions:      []supply.Region{supply.Regions["Global"]},
				}

				exch, err := NewExchange(bgCtx, settings)
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
			require.NoError(t, client.Supply().Register(rootCid, storeID))

			// In this test we expect the maximum of providers to receive the content
			// that may not be the case in the real world
			res := client.Supply().Dispatch(supply.Request{
				PayloadCID: rootCid,
				Size:       uint64(len(origBytes)),
			}, supply.DefaultDispatchOptions)

			var records []supply.PRecord
			for rec := range res {
				records = append(records, rec)
			}
			require.Equal(t, 7, len(records))

			// Gather and check all the recipients have a proper copy of the file
			for _, r := range records {
				store, err := providers[r.Provider].Supply().GetStore(rootCid)
				require.NoError(t, err)
				pnodes[r.Provider].VerifyFileTransferred(ctx, t, store.DAG, rootCid, origBytes)
			}

			err := client.Supply().RemoveContent(rootCid)
			require.NoError(t, err)

			// Sanity check to make sure our client does not have a copy of our blocks
			_, err = client.Supply().GetStore(rootCid)
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
			require.Equal(t, ref.ID, deal.ID(0))

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

func TestDAGStat(t *testing.T) {
	ctx := context.Background()

	ds := dss.MutexWrap(datastore.NewMapDatastore())

	bs := blockstore.NewBlockstore(ds)

	dag := merkledag.NewDAGService(blockservice.New(bs, offline.Exchange(bs)))

	testCases := []struct {
		name      string
		dataSize  int
		chunkSize int
		numBlocks int
		totalSize int
	}{
		{
			name:      "Single block",
			dataSize:  1000,
			chunkSize: 1024,
			numBlocks: 1,
			totalSize: 1000,
		},
		{
			name:      "Couple blocks",
			dataSize:  2000,
			chunkSize: 1024,
			numBlocks: 3,
			totalSize: 2103,
		},
		{
			name:      "Many blocks",
			dataSize:  256000,
			chunkSize: 1024,
			numBlocks: 251,
			totalSize: 268009,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {

			data := make([]byte, testCase.dataSize)
			rand.New(rand.NewSource(time.Now().UnixNano())).Read(data)

			file, err := ioutil.TempFile("/tmp", "data")
			require.NoError(t, err)
			defer os.Remove(file.Name())

			_, err = file.Write(data)
			require.NoError(t, err)

			of, err := os.Open(file.Name())
			require.NoError(t, err)

			var buf bytes.Buffer
			tr := io.TeeReader(of, &buf)
			f := files.NewReaderFile(tr)

			// import to UnixFS
			bufferedDS := ipldformat.NewBufferedDAG(ctx, dag)

			params := helpers.DagBuilderParams{
				Maxlinks:   1024,
				RawLeaves:  true,
				CidBuilder: nil,
				Dagserv:    bufferedDS,
			}

			db, err := params.New(chunk.NewSizeSplitter(f, int64(testCase.chunkSize)))
			require.NoError(t, err)

			nd, err := balanced.Layout(db)
			require.NoError(t, err)

			err = bufferedDS.Commit()
			require.NoError(t, err)

			stats, err := DAGStat(ctx, bs, nd.Cid(), AllSelector())
			require.NoError(t, err)

			require.Equal(t, testCase.numBlocks, stats.NumBlocks)
			require.Equal(t, testCase.totalSize, stats.Size)

		})
	}
}
