package utils

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-multistore"
	"github.com/ipfs/go-datastore"
	dss "github.com/ipfs/go-datastore/sync"
	chunk "github.com/ipfs/go-ipfs-chunker"
	files "github.com/ipfs/go-ipfs-files"
	ipldformat "github.com/ipfs/go-ipld-format"
	"github.com/ipfs/go-unixfs"
	"github.com/ipfs/go-unixfs/importer/balanced"
	"github.com/ipfs/go-unixfs/importer/helpers"
	"github.com/ipld/go-ipld-prime"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/myelnet/pop/internal/testutil"
	sel "github.com/myelnet/pop/selectors"
	"github.com/stretchr/testify/require"
)

func TestStat(t *testing.T) {
	ctx := context.Background()

	ds := dss.MutexWrap(datastore.NewMapDatastore())
	ms, err := multistore.NewMultiDstore(ds)
	require.NoError(t, err)
	store, err := ms.Get(ms.Next())
	require.NoError(t, err)

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

			file, err := os.CreateTemp("", "data")
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
			bufferedDS := ipldformat.NewBufferedDAG(ctx, store.DAG)

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

			stats, err := Stat(ctx, store, nd.Cid(), sel.All())
			require.NoError(t, err)

			require.Equal(t, testCase.numBlocks, stats.NumBlocks)
			require.Equal(t, testCase.totalSize, stats.Size)

		})
	}
}

type StoreConfigurableTransport interface {
	UseStore(datatransfer.ChannelID, ipld.Loader, ipld.Storer) error
}

func TestCompareStatWithGraphSync(t *testing.T) {
	sizes := []int{1024, 128000, 256000, 512000}
	for _, size := range sizes {
		t.Run(fmt.Sprintf("Size %d", size), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()

			mn := mocknet.New(ctx)
			n1 := testutil.NewTestNode(mn, t)
			n2 := testutil.NewTestNode(mn, t)

			mn.LinkAll()
			mn.ConnectAllButSelf()

			n1.SetupDataTransfer(ctx, t)
			n2.SetupDataTransfer(ctx, t)

			n1.Dt.RegisterVoucherType(&testutil.FakeDTType{}, &testutil.FakeDTValidator{})
			n2.Dt.RegisterVoucherType(&testutil.FakeDTType{}, &testutil.FakeDTValidator{})

			fname := n1.CreateRandomFile(t, size)
			link, sID, _ := n1.LoadFileToNewStore(ctx, t, fname)
			store, _ := n1.Ms.Get(sID)

			n1.Dt.RegisterTransportConfigurer(&testutil.FakeDTType{}, func(chID datatransfer.ChannelID, voucher datatransfer.Voucher, tp datatransfer.Transport) {
				tp.(StoreConfigurableTransport).UseStore(chID, store.Loader, store.Storer)
			})

			done := make(chan uint64, 1)
			n2.Dt.SubscribeToEvents(func(event datatransfer.Event, chState datatransfer.ChannelState) {
				if chState.Status() == datatransfer.Completed {
					done <- chState.Received()
				}
			})
			root := link.(cidlink.Link).Cid
			_, err := n2.Dt.OpenPullDataChannel(ctx, n1.Host.ID(), &testutil.FakeDTType{}, root, sel.All())
			require.NoError(t, err)

			select {
			case size := <-done:
				stats, err := Stat(ctx, store, root, sel.All())
				require.NoError(t, err)
				require.Equal(t, size, uint64(stats.Size))
			case <-ctx.Done():
				t.Fatal("Could not finish transfer")
			}
		})
	}
}

func TestMapLoadableKeys(t *testing.T) {
	mn := mocknet.New(context.Background())
	tn := testutil.NewTestNode(mn, t)

	file1 := tn.CreateRandomFile(t, 128000)
	lnk1, storeID, _ := tn.LoadFileToNewStore(context.TODO(), t, file1)
	store, err := tn.Ms.Get(storeID)
	require.NoError(t, err)

	file2 := tn.CreateRandomFile(t, 128000)
	lnk2, _ := tn.LoadFileToStore(context.TODO(), t, store, file2)

	_, key1 := filepath.Split(file1)
	cid1 := lnk1.(cidlink.Link).Cid
	_, key2 := filepath.Split(file2)
	cid2 := lnk2.(cidlink.Link).Cid

	dir := unixfs.EmptyDirNode()
	dir.AddRawLink(key1, &ipldformat.Link{
		Size: 128000,
		Cid:  cid1,
	})
	dir.AddRawLink(key2, &ipldformat.Link{
		Size: 128000,
		Cid:  cid2,
	})
	store.Bstore.Put(dir)

	keys, err := MapLoadableKeys(context.TODO(), dir.Cid(), store.Loader)
	require.NoError(t, err)
	require.Equal(t, KeyList([]string{key1, key2}).Sorted(), keys.Sorted())
}
