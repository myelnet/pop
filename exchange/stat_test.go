package exchange

import (
	"bytes"
	"context"
	"io"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/filecoin-project/go-multistore"
	"github.com/ipfs/go-datastore"
	dss "github.com/ipfs/go-datastore/sync"
	chunk "github.com/ipfs/go-ipfs-chunker"
	files "github.com/ipfs/go-ipfs-files"
	ipldformat "github.com/ipfs/go-ipld-format"
	"github.com/ipfs/go-unixfs/importer/balanced"
	"github.com/ipfs/go-unixfs/importer/helpers"
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
