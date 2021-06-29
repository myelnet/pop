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
	"time"

	"github.com/filecoin-project/go-multistore"
	files "github.com/ipfs/go-ipfs-files"
	"github.com/ipfs/go-path"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	"github.com/libp2p/go-libp2p-core/host"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/myelnet/pop/internal/testutil"
	"github.com/myelnet/pop/internal/utils"
	sel "github.com/myelnet/pop/selectors"
	"github.com/stretchr/testify/require"
)

func TestTx(t *testing.T) {
	newNode := func(ctx context.Context, mn mocknet.Mocknet) (*Exchange, *testutil.TestNode) {
		n := testutil.NewTestNode(mn, t)
		opts := Options{
			RepoPath:     n.DTTmpDir,
			ReplInterval: -1,
		}
		exch, err := New(ctx, n.Host, n.Ds, opts)
		require.NoError(t, err)
		return exch, n
	}
	bgCtx := context.Background()

	ctx, cancel := context.WithTimeout(bgCtx, 10*time.Second)
	defer cancel()

	mn := mocknet.New(bgCtx)

	var providers []*Exchange
	var pnodes []*testutil.TestNode

	for i := 0; i < 11; i++ {
		exch, n := newNode(ctx, mn)
		providers = append(providers, exch)
		pnodes = append(pnodes, n)
	}
	require.NoError(t, mn.LinkAll())
	require.NoError(t, mn.ConnectAllButSelf())

	// The peer manager has time to fill up while we load this file
	fname := pnodes[0].CreateRandomFile(t, 56000)
	tx := providers[0].Tx(ctx)
	link, bytes := pnodes[0].LoadFileToStore(ctx, t, tx.Store(), fname)
	rootCid := link.(cidlink.Link).Cid
	require.NoError(t, tx.Put(KeyFromPath(fname), rootCid, int64(len(bytes))))

	file, err := tx.GetFile(KeyFromPath(fname))
	require.NoError(t, err)
	size, err := file.Size()
	require.NoError(t, err)
	require.Equal(t, size, int64(56000))

	// Commit the transaction will dipatch the content to the network
	require.NoError(t, tx.Commit())

	var records []PRecord
	tx.WatchDispatch(func(rec PRecord) {
		records = append(records, rec)
	})
	require.Equal(t, 6, len(records))
	root := tx.Root()
	require.NoError(t, tx.Close())

	// Create a new client
	client, _ := newNode(ctx, mn)

	require.NoError(t, mn.LinkAll())
	require.NoError(t, mn.ConnectAllButSelf())

	tx = client.Tx(ctx, WithRoot(root), WithStrategy(SelectFirst))
	require.NoError(t, tx.Query(sel.Key(KeyFromPath(fname))))

loop:
	for {
		select {
		case <-ctx.Done():
			t.Fatal("tx timeout")
		case <-tx.Ongoing():
		case <-tx.Done():
			break loop
		}
	}
	file, err = tx.GetFile(KeyFromPath(fname))
	require.NoError(t, err)
	size, err = file.Size()
	require.NoError(t, err)
	require.Equal(t, size, int64(56000))

	require.NoError(t, tx.Close())
	// Check the global blockstore now has the blocks
	_, err = utils.Stat(ctx, &multistore.Store{Bstore: client.opts.Blockstore}, root, sel.All())
	require.NoError(t, err)

	// Get will by default create a new store if it's been deleted
	store, err := tx.ms.Get(tx.StoreID())
	require.NoError(t, err)
	// That new store should not have our blocks
	has, err := store.Bstore.Has(root)
	require.NoError(t, err)
	require.False(t, has)
}

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

func TestTxPutGet(t *testing.T) {
	ctx := context.Background()
	mn := mocknet.New(ctx)

	n := testutil.NewTestNode(mn, t)
	opts := Options{
		RepoPath: n.DTTmpDir,
	}
	exch, err := New(ctx, n.Host, n.Ds, opts)
	require.NoError(t, err)

	filevals, filepaths := genTestFiles(t)

	tx := exch.Tx(ctx)
	sID := tx.StoreID()
	for _, p := range filepaths {
		link, bytes := n.LoadFileToStore(ctx, t, tx.Store(), p)
		rootCid := link.(cidlink.Link).Cid
		require.NoError(t, tx.Put(KeyFromPath(p), rootCid, int64(len(bytes))))
	}

	status, err := tx.Status()
	require.NoError(t, err)
	require.Equal(t, len(filepaths), len(status))

	require.NoError(t, tx.Commit())
	r := tx.Root()
	require.NoError(t, exch.Index().SetRef(tx.Ref()))
	require.NoError(t, tx.Close())

	tx = exch.Tx(ctx)

	link, bytes := n.LoadFileToStore(ctx, t, tx.Store(), filepaths[0])
	rootCid := link.(cidlink.Link).Cid
	require.NoError(t, tx.Put(KeyFromPath(filepaths[0]), rootCid, int64(len(bytes))))

	// We should have a new store with a single entry
	status, err = tx.Status()
	require.NoError(t, err)
	require.Equal(t, 1, len(status))

	require.NotEqual(t, sID, tx.StoreID())

	// Test that we can retrieve local content stored by a previous transaction
	tx = exch.Tx(ctx, WithRoot(r))
	for k, v := range filevals {
		nd, err := tx.GetFile(k)
		require.NoError(t, err)
		f := nd.(files.File)
		bytes, err := io.ReadAll(f)
		require.NoError(t, err)

		require.Equal(t, bytes, []byte(v))
	}
	// Generate a path to look for
	p := fmt.Sprintf("/%s/line1.txt", r.String())
	pp := path.FromString(p)
	root, segs, err := path.SplitAbsPath(pp)
	require.NoError(t, err)
	require.Equal(t, root, r)
	require.Equal(t, segs, []string{"line1.txt"})
}

func BenchmarkAdd(b *testing.B) {
	ctx := context.Background()
	mn := mocknet.New(ctx)
	n := testutil.NewTestNode(mn, b)
	opts := Options{
		RepoPath: n.DTTmpDir,
	}
	exch, err := New(ctx, n.Host, n.Ds, opts)
	require.NoError(b, err)

	var filepaths []string
	for i := 0; i < b.N; i++ {
		filepaths = append(filepaths, n.CreateRandomFile(b, 256000))
	}

	b.ReportAllocs()
	b.ResetTimer()
	runtime.GC()

	tx := exch.Tx(ctx)
	for i := 0; i < b.N; i++ {
		link, bytes := n.LoadFileToStore(ctx, b, tx.Store(), filepaths[i])
		rootCid := link.(cidlink.Link).Cid
		require.NoError(b, tx.Put(KeyFromPath(filepaths[i]), rootCid, int64(len(bytes))))
	}
}

// Testing this with race flag to detect any weirdness
func TestTxRace(t *testing.T) {
	ctx := context.Background()
	mn := mocknet.New(ctx)
	n := testutil.NewTestNode(mn, t)
	opts := Options{
		RepoPath: n.DTTmpDir,
	}
	exch, err := New(ctx, n.Host, n.Ds, opts)
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			harness := &testutil.TestNode{}
			tx := exch.Tx(ctx)
			fname1 := harness.CreateRandomFile(t, 100000)
			link, bytes := n.LoadFileToStore(ctx, t, tx.Store(), fname1)
			rootCid := link.(cidlink.Link).Cid
			require.NoError(t, tx.Put(KeyFromPath(fname1), rootCid, int64(len(bytes))))

			fname2 := harness.CreateRandomFile(t, 156000)
			link, bytes = n.LoadFileToStore(ctx, t, tx.Store(), fname2)
			rootCid = link.(cidlink.Link).Cid
			require.NoError(t, tx.Put(KeyFromPath(fname2), rootCid, int64(len(bytes))))

			require.NoError(t, tx.Commit())
			require.NoError(t, tx.Close())
		}()
	}
	wg.Wait()
}

// Test retrieving a specific value for a key in the IPLD map
func TestMapFieldSelector(t *testing.T) {
	ctx := context.Background()
	mn := mocknet.New(ctx)

	n1 := testutil.NewTestNode(mn, t)
	opts := Options{
		RepoPath: n1.DTTmpDir,
	}
	pn, err := New(ctx, n1.Host, n1.Ds, opts)
	require.NoError(t, err)

	n2 := testutil.NewTestNode(mn, t)
	cn, err := New(ctx, n2.Host, n2.Ds, Options{
		RepoPath: n2.DTTmpDir,
	})
	require.NoError(t, err)

	require.NoError(t, mn.LinkAll())
	require.NoError(t, mn.ConnectAllButSelf())

	time.Sleep(time.Second)

	filevals, filepaths := genTestFiles(t)

	tx := pn.Tx(ctx)
	for _, p := range filepaths {
		link, bytes := n1.LoadFileToStore(ctx, t, tx.Store(), p)
		rootCid := link.(cidlink.Link).Cid
		require.NoError(t, tx.Put(KeyFromPath(p), rootCid, int64(len(bytes))))
	}
	tx.SetCacheRF(0)
	require.NoError(t, tx.Commit())
	require.NoError(t, pn.Index().SetRef(tx.Ref()))

	stat, err := utils.Stat(ctx, tx.Store(), tx.Root(), sel.Key("line2.txt"))
	require.NoError(t, err)
	require.Equal(t, 2, stat.NumBlocks)
	require.Equal(t, 683, stat.Size)

	// Close the transaction
	require.NoError(t, tx.Close())

	gtx := cn.Tx(ctx, WithRoot(tx.Root()), WithStrategy(SelectFirst))
	key := KeyFromPath(filepaths[0])

	// We skip discovery and request an offer directly
	info := host.InfoFromHost(n1.Host)
	require.NoError(t, gtx.QueryFrom(*info, sel.Key(key)))

loop:
	for {
		select {
		case <-gtx.Ongoing():
		case <-gtx.Done():
			break loop
		case <-ctx.Done():
			t.Fatal("transaction could not complete")
		}
	}
	fnd, err := gtx.GetFile(key)
	require.NoError(t, err)
	f := fnd.(files.File)
	bytes, err := io.ReadAll(f)
	require.NoError(t, err)
	require.Equal(t, bytes, []byte(filevals[key]))

	// Getting any other key should fail
	_, err = gtx.GetFile(KeyFromPath(filepaths[1]))
	require.Error(t, err)
}

func TestMultiTx(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	mn := mocknet.New(ctx)

	n1 := testutil.NewTestNode(mn, t)
	opts := Options{
		RepoPath: n1.DTTmpDir,
	}
	pn, err := New(ctx, n1.Host, n1.Ds, opts)
	require.NoError(t, err)

	n2 := testutil.NewTestNode(mn, t)
	cn1, err := New(ctx, n2.Host, n2.Ds, Options{
		RepoPath: n2.DTTmpDir,
	})
	require.NoError(t, err)

	n3 := testutil.NewTestNode(mn, t)
	_, err = New(ctx, n3.Host, n3.Ds, Options{
		RepoPath: n3.DTTmpDir,
	})
	require.NoError(t, err)

	require.NoError(t, mn.LinkAll())
	require.NoError(t, mn.ConnectAllButSelf())

	time.Sleep(time.Second)

	_, filepaths := genTestFiles(t)

	tx := pn.Tx(ctx)
	for _, p := range filepaths {
		link, bytes := n1.LoadFileToStore(ctx, t, tx.Store(), p)
		rootCid := link.(cidlink.Link).Cid
		require.NoError(t, tx.Put(KeyFromPath(p), rootCid, int64(len(bytes))))
	}
	tx.SetCacheRF(0)
	require.NoError(t, tx.Commit())
	require.NoError(t, pn.Index().SetRef(tx.Ref()))
	require.NoError(t, tx.Close())

	gtx1 := cn1.Tx(ctx, WithRoot(tx.Root()), WithStrategy(SelectFirst))
	key1 := KeyFromPath(filepaths[0])
	require.NoError(t, gtx1.Query(sel.Key(key1)))

loop:
	for {
		select {
		case <-ctx.Done():
			t.Fatal("could not finish gtx1")
		case <-gtx1.Ongoing():
		case <-gtx1.Done():
			break loop
		}
	}

	time.Sleep(10 * time.Millisecond)

	_, err = gtx1.GetFile(key1)
	require.NoError(t, err)

	gtx2 := cn1.Tx(ctx, WithRoot(tx.Root()), WithStrategy(SelectFirst))
	key2 := KeyFromPath(filepaths[1])
	require.NoError(t, gtx2.Query(sel.Key(key2)))

loop2:
	for {
		select {
		case <-ctx.Done():
			t.Fatal("could not finish gtx2")
		case <-gtx2.Ongoing():
		case <-gtx2.Done():
			break loop2
		}
	}
}

func TestTxGetEntries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	mn := mocknet.New(ctx)

	n1 := testutil.NewTestNode(mn, t)
	opts := Options{
		RepoPath: n1.DTTmpDir,
	}
	pn, err := New(ctx, n1.Host, n1.Ds, opts)
	require.NoError(t, err)

	_, filepaths := genTestFiles(t)

	tx := pn.Tx(ctx)
	tx.SetCacheRF(0)
	for _, p := range filepaths {
		link, bytes := n1.LoadFileToStore(ctx, t, tx.Store(), p)
		rootCid := link.(cidlink.Link).Cid
		require.NoError(t, tx.Put(KeyFromPath(p), rootCid, int64(len(bytes))))
	}

	fname := n1.CreateRandomFile(t, 56000)
	link, bytes := n1.LoadFileToStore(ctx, t, tx.Store(), fname)
	rootCid := link.(cidlink.Link).Cid
	require.NoError(t, tx.Put(KeyFromPath(fname), rootCid, int64(len(bytes))))

	require.NoError(t, tx.Commit())
	require.NoError(t, pn.Index().SetRef(tx.Ref()))
	require.NoError(t, tx.Close())

	// Fresh new tx based on the root of the previous one
	ntx := pn.Tx(ctx, WithRoot(tx.Root()))
	keys, err := ntx.Keys()
	require.NoError(t, err)
	require.Equal(t, len(filepaths)+1, len(keys))

	// A client enters the scene
	n2 := testutil.NewTestNode(mn, t)
	opts2 := Options{
		RepoPath: n2.DTTmpDir,
	}
	cn, err := New(ctx, n2.Host, n2.Ds, opts2)
	require.NoError(t, err)

	require.NoError(t, mn.LinkAll())
	require.NoError(t, mn.ConnectAllButSelf())
	time.Sleep(time.Second)

	gtx := cn.Tx(ctx, WithRoot(tx.Root()), WithStrategy(SelectFirst))
	require.NoError(t, gtx.Query(sel.Entries()))

loop:
	for {
		select {
		case <-ctx.Done():
			t.Fatal("could not finish gtx1")
		case <-gtx.Ongoing():
		case <-gtx.Done():
			break loop
		}
	}

	// @NOTE: even when selecting a specific key the operation will retrieve all other the entries
	// without the linked data. We may need to alter this behavior in cases where there is a large
	// number of entries
	keys, err = gtx.Keys()
	require.NoError(t, err)
	require.Equal(t, len(filepaths)+1, len(keys))

	entries, err := gtx.Entries()
	require.NoError(t, err)
	require.Equal(t, len(filepaths)+1, len(entries))
}
