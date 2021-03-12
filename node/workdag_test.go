package node

import (
	"context"
	"io/ioutil"
	"path/filepath"
	"testing"

	"github.com/filecoin-project/go-multistore"
	"github.com/ipfs/go-datastore"
	dss "github.com/ipfs/go-datastore/sync"
	"github.com/stretchr/testify/require"
)

func genTestFiles(t *testing.T) []string {
	dir := t.TempDir()

	testInputs := map[string]string{
		"1": "Two roads diverged in a yellow wood,\n",
		"2": "And sorry I could not travel both\n",
		"3": "And be one traveler, long I stood\n",
		"4": "And looked down one as far as I could\n",
		"5": "To where it bent in the undergrowth;\n",
		"6": "Then took the other, as just as fair,\n",
		"7": "And having perhaps the better claim,\n",
		"8": "Because it was grassy and wanted wear;\n",
	}

	paths := make([]string, 0, len(testInputs))

	for p, c := range testInputs {
		path := filepath.Join(dir, p)

		if err := ioutil.WriteFile(path, []byte(c), 0666); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	return paths
}

func TestWorkdag(t *testing.T) {
	ctx := context.Background()

	ds := dss.MutexWrap(datastore.NewMapDatastore())
	ms, err := multistore.NewMultiDstore(ds)
	require.NoError(t, err)

	filepaths := genTestFiles(t)

	wd, err := NewWorkdag(ms, ds)
	require.NoError(t, err)

	for i := 0; i < 6; i++ {
		_, err := wd.Add(ctx, AddOptions{Path: filepaths[i], ChunkSize: int64(1 << 10)})
		require.NoError(t, err)

	}

	status, err := wd.Status()
	require.NoError(t, err)
	require.Equal(t, 6, len(status))

	// Should handle new instance
	wd, err = NewWorkdag(ms, ds)
	require.NoError(t, err)

	for i := 6; i < 8; i++ {
		_, err := wd.Add(ctx, AddOptions{Path: filepaths[i], ChunkSize: int64(1 << 10)})
		require.NoError(t, err)
	}

	// save the previous store ID
	sID := wd.StoreID()

	dataCID := "bafy2bzacedppq7brujaqwwve5mxp5mp2i2zsvh2hlxpucaxrvql6bwukgxstw"

	c, err := wd.Commit(ctx, CommitOptions{})
	require.NoError(t, err)
	require.Equal(t, dataCID, c.PayloadCID.String())

	_, err = wd.Add(ctx, AddOptions{Path: filepaths[0], ChunkSize: int64(1 << 10)})
	require.NoError(t, err)

	// We should have a new store with a single entry
	status, err = wd.Status()
	require.NoError(t, err)
	require.Equal(t, 1, len(status))

	require.NotEqual(t, sID, wd.StoreID())

	// We can load a new workdag from store as well
	wd, err = NewWorkdag(ms, ds)
	require.NoError(t, err)

	// Our index remembers the last commit
	idx, err := wd.Index()
	require.NoError(t, err)

	require.Equal(t, 1, len(idx.Commits))
	require.Equal(t, idx.Commits[0].PayloadCID.String(), dataCID)
}
