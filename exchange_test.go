package hop

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/ipfs/go-blockservice"
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
	peer "github.com/libp2p/go-libp2p-core/peer"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/myelnet/go-hop-exchange/supply"
	"github.com/myelnet/go-hop-exchange/testutil"
	"github.com/stretchr/testify/require"
)

func TestExchangeDirect(t *testing.T) {
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
				n.SetupTempRepo(t)
				ps, err := pubsub.NewGossipSub(ctx, n.Host)
				require.NoError(t, err)

				settings := Settings{
					Datastore:  n.Ds,
					Blockstore: n.Bs,
					MultiStore: n.Ms,
					Host:       n.Host,
					PubSub:     ps,
					GraphSync:  n.Gs,
					RepoPath:   n.DTTmpDir,
					Keystore:   keystore.NewMemKeystore(),
					Regions:    []supply.Region{supply.Regions["Global"]},
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

			link, storeID, origBytes := cnode.LoadFileToNewStore(ctx, t, "/README.md")
			rootCid := link.(cidlink.Link).Cid

			// In this test we expect the maximum of providers to receive the content
			// that may not be the case in the real world
			res, err := client.Supply().Dispatch(supply.Request{
				PayloadCID: rootCid,
				Size:       uint64(len(origBytes)),
			},
				supply.DispatchOptions{
					StoreID: storeID,
				})
			require.NoError(t, err)

			var records []supply.PRecord
			for len(records) < 6 {
				rec, err := res.Next(ctx)
				require.NoError(t, err)
				records = append(records, rec)
			}
			res.Close()

			// Gather and check all the recipients have a proper copy of the file
			for _, r := range records {
				store, err := providers[r.Provider].Supply().GetStore(rootCid)
				require.NoError(t, err)
				pnodes[r.Provider].VerifyFileTransferred(ctx, t, store.DAG, rootCid, origBytes)
			}

			err = client.Supply().RemoveContent(rootCid)
			require.NoError(t, err)

			// Sanity check to make sure our client does not have a copy of our blocks
			_, err = client.Supply().GetStore(rootCid)
			require.Error(t, err)

			// Now we fetch it again from our providers
			session, err := client.NewSession(ctx, rootCid)
			require.NoError(t, err)
			defer session.Close()

			offer, err := session.QueryGossip(ctx)
			require.NoError(t, err)

			err = session.SyncBlocks(ctx, offer)
			require.NoError(t, err)

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
