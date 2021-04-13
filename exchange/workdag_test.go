package exchange

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/filecoin-project/go-multistore"
	"github.com/ipfs/go-datastore"
	dss "github.com/ipfs/go-datastore/sync"
	files "github.com/ipfs/go-ipfs-files"
	"github.com/ipfs/go-path"
	"github.com/myelnet/pop/internal/testutil"
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

	ref, err := wd.Commit(ctx, CommitOptions{})
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

	require.Equal(t, 1, len(idx.Refs))

	// Now check if we can unpack it back into a list of files
	com := idx.Refs[ref.PayloadCID.String()]
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

func BenchmarkAdd(b *testing.B) {

	ctx := context.Background()

	ds := dss.MutexWrap(datastore.NewMapDatastore())
	ms, err := multistore.NewMultiDstore(ds)
	require.NoError(b, err)

	wd, err := NewWorkdag(ms, ds)
	require.NoError(b, err)

	var filepaths []string
	for i := 0; i < b.N; i++ {
		filepaths = append(filepaths, (&testutil.TestNode{}).CreateRandomFile(b, 256000))
	}

	b.ReportAllocs()
	b.ResetTimer()
	runtime.GC()

	for i := 0; i < b.N; i++ {
		_, err := wd.Add(ctx, AddOptions{Path: filepaths[i], ChunkSize: int64(1 << 10)})
		require.NoError(b, err)
	}
}

func TestWorkdagLFU(t *testing.T) {
	ctx := context.Background()
	ds := dss.MutexWrap(datastore.NewMapDatastore())
	ms, err := multistore.NewMultiDstore(ds)
	require.NoError(t, err)

	wd, err := NewWorkdag(ms, ds, WithBounds(512000, 500000))
	require.NoError(t, err)

	harness := &testutil.TestNode{}

	fname1 := harness.CreateRandomFile(t, 100000)
	_, err = wd.Add(ctx, AddOptions{Path: fname1, ChunkSize: int64(1 << 10)})
	require.NoError(t, err)

	fname2 := harness.CreateRandomFile(t, 156000)
	_, err = wd.Add(ctx, AddOptions{Path: fname2, ChunkSize: int64(1 << 10)})

	ref1, err := wd.Commit(ctx, CommitOptions{})
	require.NoError(t, err)

	fname3 := harness.CreateRandomFile(t, 44000)
	_, err = wd.Add(ctx, AddOptions{Path: fname3, ChunkSize: int64(1 << 10)})
	require.NoError(t, err)

	fname4 := harness.CreateRandomFile(t, 66000)
	_, err = wd.Add(ctx, AddOptions{Path: fname4, ChunkSize: int64(1 << 10)})

	ref2, err := wd.Commit(ctx, CommitOptions{})
	require.NoError(t, err)

	// Adding some reads
	_, err = wd.GetRef(ref2.PayloadCID.String())
	_, err = wd.GetRef(ref2.PayloadCID.String())

	// Now add a very large piece
	fname5 := harness.CreateRandomFile(t, 256000)
	_, err = wd.Add(ctx, AddOptions{Path: fname5, ChunkSize: int64(1 << 10)})
	require.NoError(t, err)

	fname6 := harness.CreateRandomFile(t, 100000)
	_, err = wd.Add(ctx, AddOptions{Path: fname6, ChunkSize: int64(1 << 10)})
	require.NoError(t, err)

	ref3, err := wd.Commit(ctx, CommitOptions{})
	require.NoError(t, err)

	// Now our first ref should be evicted
	_, err = wd.GetRef(ref1.PayloadCID.String())
	require.Error(t, err)

	// But our second ref should still be around
	_, err = wd.GetRef(ref2.PayloadCID.String())
	require.NoError(t, err)

	// Test reinitializing the list from the stored frequencies
	wd, err = NewWorkdag(ms, ds, WithBounds(512000, 500000))
	require.NoError(t, err)

	// Add more stuff in there
	fname7 := harness.CreateRandomFile(t, 20000)
	_, err = wd.Add(ctx, AddOptions{Path: fname7, ChunkSize: int64(1 << 10)})
	require.NoError(t, err)

	_, err = wd.Commit(ctx, CommitOptions{})
	require.NoError(t, err)

	fname8 := harness.CreateRandomFile(t, 60000)
	_, err = wd.Add(ctx, AddOptions{Path: fname8, ChunkSize: int64(1 << 10)})
	require.NoError(t, err)

	_, err = wd.Commit(ctx, CommitOptions{})
	require.NoError(t, err)

	// ref2 should still be around
	_, err = wd.GetRef(ref2.PayloadCID.String())
	require.NoError(t, err)

	// ref3 is gone
	_, err = wd.GetRef(ref3.PayloadCID.String())
	require.Error(t, err)
}
