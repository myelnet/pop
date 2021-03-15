package node

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"path/filepath"
	"testing"

	"github.com/filecoin-project/go-multistore"
	"github.com/ipfs/go-datastore"
	dss "github.com/ipfs/go-datastore/sync"
	files "github.com/ipfs/go-ipfs-files"
	"github.com/ipfs/go-path"
	"github.com/stretchr/testify/require"
)

func genTestFiles(t *testing.T) (map[string]string, []string) {
	dir := t.TempDir()

	testInputs := map[string]string{
		"line1.txt": "Two roads diverged in a yellow wood,\n",
		"line2.txt": "And sorry I could not travel both\n",
		"line3.txt": "And be one traveler, long I stood\n",
		"line4.txt": "And looked down one as far as I could\n",
		"line5.txt": "To where it bent in the undergrowth;\n",
		"line6.txt": "Then took the other, as just as fair,\n",
		"line7.txt": "And having perhaps the better claim,\n",
		"line8.txt": "Because it was grassy and wanted wear;\n",
	}

	paths := make([]string, 0, len(testInputs))

	for p, c := range testInputs {
		path := filepath.Join(dir, p)

		if err := ioutil.WriteFile(path, []byte(c), 0666); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	return testInputs, paths
}

func TestWorkdag(t *testing.T) {
	ctx := context.Background()

	ds := dss.MutexWrap(datastore.NewMapDatastore())
	ms, err := multistore.NewMultiDstore(ds)
	require.NoError(t, err)

	filevals, filepaths := genTestFiles(t)

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

	_, err = wd.Commit(ctx, CommitOptions{})
	require.NoError(t, err)

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

	// Now check if we can unpack it back into a list of files
	com := idx.Commits[0]
	fileNds, err := wd.Unpack(ctx, com.PayloadCID, com.StoreID)
	require.NoError(t, err)
	require.Equal(t, len(filepaths), len(fileNds))

	for k, nd := range fileNds {
		// Ignore the first one we read later
		if k == "line1.txt" {
			continue
		}
		f := nd.(files.File)
		bytes, err := io.ReadAll(f)
		require.NoError(t, err)

		require.NotEqual(t, "", filevals[k])
		require.Equal(t, bytes, []byte(filevals[k]))
	}
	// Generate a path to look for
	p := fmt.Sprintf("/%s/line1.txt", com.PayloadCID.String())
	pp := path.FromString(p)
	root, segs, err := path.SplitAbsPath(pp)
	require.NoError(t, err)
	require.Equal(t, root, com.PayloadCID)
	require.Equal(t, segs, []string{"line1.txt"})

	// Verify one last time we have the right key and all
	file := fileNds[segs[0]]
	f := file.(files.File)
	bytes, err := io.ReadAll(f)
	require.NoError(t, err)
	require.Equal(t, bytes, []byte(filevals["line1.txt"]))
}
