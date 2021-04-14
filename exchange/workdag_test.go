package exchange

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"path/filepath"
	"runtime"
	"sync"
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

	idx, err := NewIndex(ds, ms)
	require.NoError(t, err)
	wd := NewWorkdag(ms)

	tx := wd.Tx(ctx)
	sID := tx.StoreID()
	for _, p := range filepaths {
		key := KeyFromPath(p)
		_, err := tx.Put(key, PutOptions{Path: p, ChunkSize: int64(1 << 10)})
		require.NoError(t, err)
	}

	status, err := tx.Status()
	require.NoError(t, err)
	require.Equal(t, len(filepaths), len(status))

	ref, err := tx.Commit()
	require.NoError(t, err)
	require.NoError(t, idx.SetRef(ref))

	tx = wd.Tx(ctx)
	_, err = tx.Put(KeyFromPath(filepaths[0]), PutOptions{Path: filepaths[0], ChunkSize: int64(1 << 10)})
	require.NoError(t, err)

	// We should have a new store with a single entry
	status, err = tx.Status()
	require.NoError(t, err)
	require.Equal(t, 1, len(status))

	require.NotEqual(t, sID, tx.StoreID())

	// We can load a new index from store as well
	idx, err = NewIndex(ds, ms)
	require.NoError(t, err)

	// it remembers the last commit
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

	wd := NewWorkdag(ms)
	require.NoError(b, err)

	var filepaths []string
	for i := 0; i < b.N; i++ {
		filepaths = append(filepaths, (&testutil.TestNode{}).CreateRandomFile(b, 256000))
	}

	b.ReportAllocs()
	b.ResetTimer()
	runtime.GC()

	tx := wd.Tx(ctx)
	for i := 0; i < b.N; i++ {
		_, err := tx.Put(KeyFromPath(filepaths[i]), PutOptions{Path: filepaths[i], ChunkSize: int64(1 << 10)})
		require.NoError(b, err)
	}
}

func TestWorkdagLFU(t *testing.T) {
	ctx := context.Background()
	ds := dss.MutexWrap(datastore.NewMapDatastore())
	ms, err := multistore.NewMultiDstore(ds)
	require.NoError(t, err)

	idx, err := NewIndex(ds, ms, WithBounds(512000, 500000))
	wd := NewWorkdag(ms)
	require.NoError(t, err)

	harness := &testutil.TestNode{}

	tx := wd.Tx(ctx)
	fname1 := harness.CreateRandomFile(t, 100000)
	_, err = tx.Put(KeyFromPath(fname1), PutOptions{Path: fname1, ChunkSize: int64(1 << 10)})
	require.NoError(t, err)

	fname2 := harness.CreateRandomFile(t, 156000)
	_, err = tx.Put(KeyFromPath(fname2), PutOptions{Path: fname2, ChunkSize: int64(1 << 10)})

	ref1, err := tx.Commit()
	require.NoError(t, err)
	require.NoError(t, idx.SetRef(ref1))

	tx = wd.Tx(ctx)
	fname3 := harness.CreateRandomFile(t, 44000)
	_, err = tx.Put(KeyFromPath(fname3), PutOptions{Path: fname3, ChunkSize: int64(1 << 10)})
	require.NoError(t, err)

	fname4 := harness.CreateRandomFile(t, 66000)
	_, err = tx.Put(KeyFromPath(fname4), PutOptions{Path: fname4, ChunkSize: int64(1 << 10)})

	ref2, err := tx.Commit()
	require.NoError(t, err)
	require.NoError(t, idx.SetRef(ref2))

	// Adding some reads
	_, err = idx.GetRef(ref2.PayloadCID)
	_, err = idx.GetRef(ref2.PayloadCID)

	tx = wd.Tx(ctx)
	// Now add a very large piece
	fname5 := harness.CreateRandomFile(t, 256000)
	_, err = tx.Put(KeyFromPath(fname5), PutOptions{Path: fname5, ChunkSize: int64(1 << 10)})
	require.NoError(t, err)

	fname6 := harness.CreateRandomFile(t, 100000)
	_, err = tx.Put(KeyFromPath(fname6), PutOptions{Path: fname6, ChunkSize: int64(1 << 10)})
	require.NoError(t, err)

	ref3, err := tx.Commit()
	require.NoError(t, err)
	require.NoError(t, idx.SetRef(ref3))

	// Now our first ref should be evicted
	_, err = idx.GetRef(ref1.PayloadCID)
	require.Error(t, err)

	// But our second ref should still be around
	_, err = idx.GetRef(ref2.PayloadCID)
	require.NoError(t, err)

	// Test reinitializing the list from the stored frequencies
	idx, err = NewIndex(ds, ms, WithBounds(512000, 500000))
	require.NoError(t, err)

	tx = wd.Tx(ctx)
	// Add more stuff in there
	fname7 := harness.CreateRandomFile(t, 20000)
	_, err = tx.Put(KeyFromPath(fname7), PutOptions{Path: fname7, ChunkSize: int64(1 << 10)})
	require.NoError(t, err)

	ref4, err := tx.Commit()
	require.NoError(t, err)
	require.NoError(t, idx.SetRef(ref4))

	tx = wd.Tx(ctx)
	fname8 := harness.CreateRandomFile(t, 60000)
	_, err = tx.Put(KeyFromPath(fname8), PutOptions{Path: fname8, ChunkSize: int64(1 << 10)})
	require.NoError(t, err)

	ref5, err := tx.Commit()
	require.NoError(t, err)
	require.NoError(t, idx.SetRef(ref5))

	// ref2 should still be around
	_, err = idx.GetRef(ref2.PayloadCID)
	require.NoError(t, err)

	// ref3 is gone
	_, err = idx.GetRef(ref3.PayloadCID)
	require.Error(t, err)
}

// Testing this with race flag to detect any weirdness
func TestWorkdagRace(t *testing.T) {
	ctx := context.Background()
	ds := dss.MutexWrap(datastore.NewMapDatastore())
	ms, err := multistore.NewMultiDstore(ds)
	require.NoError(t, err)

	wd := NewWorkdag(ms)
	idx, err := NewIndex(ds, ms, WithBounds(512000, 500000))
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			harness := &testutil.TestNode{}
			tx := wd.Tx(ctx)
			fname1 := harness.CreateRandomFile(t, 100000)
			_, err := tx.Put(KeyFromPath(fname1), PutOptions{Path: fname1, ChunkSize: int64(1 << 10)})
			require.NoError(t, err)

			fname2 := harness.CreateRandomFile(t, 156000)
			_, err = tx.Put(KeyFromPath(fname2), PutOptions{Path: fname2, ChunkSize: int64(1 << 10)})

			ref, err := tx.Commit()
			require.NoError(t, err)
			require.NoError(t, idx.SetRef(ref))
		}()
	}
	wg.Wait()
}

func TestIndexDropRef(t *testing.T) {
	ctx := context.Background()
	ds := dss.MutexWrap(datastore.NewMapDatastore())
	ms, err := multistore.NewMultiDstore(ds)
	require.NoError(t, err)

	wd := NewWorkdag(ms)
	idx, err := NewIndex(ds, ms)
	require.NoError(t, err)

	harness := &testutil.TestNode{}
	tx := wd.Tx(ctx)
	fname1 := harness.CreateRandomFile(t, 100000)
	_, err = tx.Put(KeyFromPath(fname1), PutOptions{Path: fname1, ChunkSize: int64(1 << 10)})
	require.NoError(t, err)

	fname2 := harness.CreateRandomFile(t, 156000)
	_, err = tx.Put(KeyFromPath(fname2), PutOptions{Path: fname2, ChunkSize: int64(1 << 10)})

	ref, err := tx.Commit()
	require.NoError(t, err)
	require.NoError(t, idx.SetRef(ref))

	err = idx.DropRef(ref.PayloadCID)
	require.NoError(t, err)

	_, err = idx.GetRef(ref.PayloadCID)
	require.Error(t, err)
}

func TestWorkdagStat(t *testing.T) {
	ctx := context.Background()
	ds := dss.MutexWrap(datastore.NewMapDatastore())
	ms, err := multistore.NewMultiDstore(ds)
	require.NoError(t, err)

	wd := NewWorkdag(ms)
	testCases := []struct {
		name      string
		dataSize  int
		chunkSize int
		numBlocks int
	}{
		{
			name:      "Single block",
			dataSize:  1000,
			chunkSize: 1024,
			numBlocks: 2,
		},
		{
			name:      "Couple blocks",
			dataSize:  2000,
			chunkSize: 1024,
			numBlocks: 4,
		},
		{
			name:      "Many blocks",
			dataSize:  256000,
			chunkSize: 1024,
			numBlocks: 252,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {

			harness := &testutil.TestNode{}
			tx := wd.Tx(ctx)
			fname1 := harness.CreateRandomFile(t, testCase.dataSize)
			_, err = tx.Put(KeyFromPath(fname1), PutOptions{Path: fname1, ChunkSize: int64(testCase.chunkSize)})
			require.NoError(t, err)

			ref, err := tx.Commit()
			require.NoError(t, err)

			stats, err := wd.Stat(ctx, tx.Store(), ref.PayloadCID, AllSelector())
			require.NoError(t, err)

			require.Equal(t, testCase.numBlocks, stats.NumBlocks)
			// Size of the blocks isn't deterministic as the CID changes every time
			// but we know it should be greater than the data size
			require.Greater(t, stats.Size, testCase.dataSize)

		})
	}
}
