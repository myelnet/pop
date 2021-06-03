package node

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-fil-markets/storagemarket"
	blocksutil "github.com/ipfs/go-ipfs-blocksutil"
	keystore "github.com/ipfs/go-ipfs-keystore"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/myelnet/pop/exchange"
	"github.com/myelnet/pop/filecoin"
	"github.com/myelnet/pop/filecoin/storage"
	"github.com/myelnet/pop/internal/testutil"
	"github.com/stretchr/testify/require"
)

type mockStorer struct {
	receipt *storage.Receipt
	quote   *storage.Quote
	pInfo   *peer.AddrInfo
}

func (ms *mockStorer) Start(context.Context) error {
	return nil
}

func (ms *mockStorer) Store(ctx context.Context, params storage.Params) (*storage.Receipt, error) {
	return ms.receipt, nil
}

func (ms *mockStorer) GetMarketQuote(ctx context.Context, params storage.QuoteParams) (*storage.Quote, error) {
	return ms.quote, nil
}

func (ms *mockStorer) PeerInfo(ctx context.Context, addr address.Address) (*peer.AddrInfo, error) {
	return ms.pInfo, nil
}

func newTestNode(ctx context.Context, mn mocknet.Mocknet, t *testing.T) *node {
	var err error

	tn := testutil.NewTestNode(mn, t)

	nd := &node{}
	nd.ds = tn.Ds
	nd.bs = tn.Bs
	nd.ms = tn.Ms
	nd.dag = tn.DAG
	nd.host = tn.Host
	opts := exchange.Options{
		Blockstore:  nd.bs,
		MultiStore:  nd.ms,
		RepoPath:    t.TempDir(),
		Keystore:    keystore.NewMemKeystore(),
		FilecoinAPI: filecoin.NewMockLotusAPI(),
	}
	nd.exch, err = exchange.New(ctx, nd.host, nd.ds, opts)
	require.NoError(t, err)

	return nd
}

func TestPing(t *testing.T) {
	ctx := context.Background()
	mn := mocknet.New(ctx)

	nd := newTestNode(ctx, mn, t)

	nd.notify = func(n Notify) {
		require.Equal(t, n.PingResult.ID, nd.host.ID().String())
	}
	nd.Ping(ctx, "")
}

func TestPut(t *testing.T) {
	ctx := context.Background()
	mn := mocknet.New(ctx)

	cn := newTestNode(ctx, mn, t)

	require.NoError(t, mn.LinkAll())
	require.NoError(t, mn.ConnectAllButSelf())

	data := make([]byte, 256000)
	rand.New(rand.NewSource(time.Now().UnixNano())).Read(data)

	file, err := ioutil.TempFile("/tmp", "data")
	require.NoError(t, err)
	defer os.Remove(file.Name())

	_, err = file.Write(data)
	require.NoError(t, err)

	added := make(chan string, 1)
	cn.notify = func(n Notify) {
		require.Equal(t, n.PutResult.Err, "")

		added <- n.PutResult.Cid
	}
	cn.Put(ctx, &PutArgs{
		Path:      file.Name(),
		ChunkSize: 1024,
	})
	<-added
}

// Put shouldn't race as it's protected with a mutex
func TestPutRace(t *testing.T) {
	ctx := context.Background()
	mn := mocknet.New(ctx)

	cn := newTestNode(ctx, mn, t)

	require.NoError(t, mn.LinkAll())
	require.NoError(t, mn.ConnectAllButSelf())

	var wg sync.WaitGroup
	cn.notify = func(n Notify) {
		require.Equal(t, n.PutResult.Err, "")

		wg.Done()
	}

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			data := make([]byte, 56000)
			rand.New(rand.NewSource(time.Now().UnixNano())).Read(data)

			file, err := ioutil.TempFile("/tmp", "data")
			require.NoError(t, err)
			defer os.Remove(file.Name())

			_, err = file.Write(data)
			require.NoError(t, err)

			cn.Put(ctx, &PutArgs{
				Path:      file.Name(),
				ChunkSize: 1024,
			})
		}()
	}
	wg.Wait()
}

func TestPutGet(t *testing.T) {
	ctx := context.Background()
	mn := mocknet.New(ctx)

	cn := newTestNode(ctx, mn, t)
	require.NoError(t, mn.LinkAll())
	require.NoError(t, mn.ConnectAllButSelf())

	dir := t.TempDir()

	data := make([]byte, 256000)
	rand.New(rand.NewSource(time.Now().UnixNano())).Read(data)
	p1 := filepath.Join(dir, "data1")
	err := os.WriteFile(p1, data, 0666)
	require.NoError(t, err)

	data2 := make([]byte, 512000)
	rand.New(rand.NewSource(time.Now().UnixNano())).Read(data2)
	p2 := filepath.Join(dir, "data2")
	err = os.WriteFile(p2, data2, 0666)
	require.NoError(t, err)

	added := make(chan string, 2)
	cn.notify = func(n Notify) {
		require.Equal(t, n.PutResult.Err, "")

		added <- n.PutResult.Cid
	}
	cn.Put(ctx, &PutArgs{
		Path:      p1,
		ChunkSize: 1024,
	})
	<-added

	cn.Put(ctx, &PutArgs{
		Path:      p2,
		ChunkSize: 1024,
	})
	<-added

	stat := make(chan string, 1)
	cn.notify = func(n Notify) {
		require.Equal(t, n.StatusResult.Err, "")

		stat <- n.StatusResult.RootCid
	}
	cn.Status(ctx, &StatusArgs{})
	out := <-stat

	// Export back out
	path := fmt.Sprintf("/%s/data2", out)
	loc := make(chan bool, 1)
	cn.notify = func(n Notify) {
		require.Equal(t, n.GetResult.Err, "")

		loc <- n.GetResult.Local
	}
	newp := filepath.Join(dir, "newdata2")
	cn.Get(ctx, &GetArgs{
		Cid:      path,
		Out:      newp,
		Strategy: "SelectFirst",
		Timeout:  1,
	})
	<-loc

	f, err := os.Open(newp)
	require.NoError(t, err)

	newb, err := io.ReadAll(f)
	require.NoError(t, err)
	require.Equal(t, data2, newb)
}

var getAddr = address.NewForTestGetter()

func TestQuote(t *testing.T) {
	ctx := context.Background()
	mn := mocknet.New(ctx)

	cn := newTestNode(ctx, mn, t)
	m1 := getAddr()
	p1, _ := filecoin.ParseFIL("0.001")
	m2 := getAddr()
	p2, _ := filecoin.ParseFIL("0.002")
	rs := &mockStorer{
		receipt: &storage.Receipt{},
		quote: &storage.Quote{
			Miners: []storage.Miner{
				{
					Info: &storagemarket.StorageProviderInfo{
						Address: m1,
					},
				},
				{
					Info: &storagemarket.StorageProviderInfo{
						Address: m2,
					},
				},
			},
			Prices: map[address.Address]filecoin.FIL{
				m1: p1,
				m2: p2,
			},
		},
		pInfo: host.InfoFromHost(cn.host),
	}
	cn.rs = rs

	require.NoError(t, mn.LinkAll())
	require.NoError(t, mn.ConnectAllButSelf())

	dir := t.TempDir()

	data := make([]byte, 256000)
	rand.New(rand.NewSource(time.Now().UnixNano())).Read(data)
	p := filepath.Join(dir, "data1")
	err := os.WriteFile(p, data, 0666)
	require.NoError(t, err)

	added := make(chan string, 1)
	cn.notify = func(n Notify) {
		require.Equal(t, n.PutResult.Err, "")

		added <- n.PutResult.Cid
	}
	cn.Put(ctx, &PutArgs{
		Path:      p,
		ChunkSize: 1024,
	})
	<-added

	quoted := make(chan struct{}, 1)
	cn.notify = func(n Notify) {
		require.Equal(t, "", n.QuoteResult.Err)
		require.Equal(t, 2, len(n.QuoteResult.Quotes))
		quoted <- struct{}{}
	}
	cn.Quote(ctx, &QuoteArgs{
		Duration:  24 * time.Hour * time.Duration(180),
		StorageRF: 6,
		MaxPrice:  uint64(20000000000),
	})
	<-quoted
	require.Equal(t, rs.quote, cn.sQuote)
}

func TestCommit(t *testing.T) {
	ctx := context.Background()
	mn := mocknet.New(ctx)

	cn := newTestNode(ctx, mn, t)

	var nds []*node
	nds = append(nds, newTestNode(ctx, mn, t))
	nds = append(nds, newTestNode(ctx, mn, t))

	require.NoError(t, mn.LinkAll())
	require.NoError(t, mn.ConnectAllButSelf())

	dir := t.TempDir()

	data := make([]byte, 256000)
	rand.New(rand.NewSource(time.Now().UnixNano())).Read(data)
	p := filepath.Join(dir, "data1")
	err := os.WriteFile(p, data, 0666)
	require.NoError(t, err)

	added := make(chan string, 1)
	cn.notify = func(n Notify) {
		require.Equal(t, n.PutResult.Err, "")

		added <- n.PutResult.Cid
	}
	cn.Put(ctx, &PutArgs{
		Path:      p,
		ChunkSize: 1024,
	})
	<-added

	committed := make(chan []string, 2)
	cn.notify = func(n Notify) {
		require.Equal(t, n.CommResult.Err, "")
		committed <- n.CommResult.Caches
	}
	cn.Commit(ctx, &CommArgs{
		CacheOnly: true,
		CacheRF:   2,
	})
	close(committed)
	for range committed {
	}
}

func TestGet(t *testing.T) {
	bgCtx := context.Background()

	ctx, cancel := context.WithTimeout(bgCtx, 4*time.Second)
	defer cancel()
	mn := mocknet.New(bgCtx)

	pn := newTestNode(bgCtx, mn, t)
	cn := newTestNode(bgCtx, mn, t)

	require.NoError(t, mn.LinkAll())
	require.NoError(t, mn.ConnectAllButSelf())

	// Let the routing propagate to gossip
	time.Sleep(time.Second)

	dir := t.TempDir()

	data := make([]byte, 256000)
	rand.New(rand.NewSource(time.Now().UnixNano())).Read(data)
	p := filepath.Join(dir, "data1")
	err := os.WriteFile(p, data, 0666)
	require.NoError(t, err)

	added := make(chan string, 1)
	pn.notify = func(n Notify) {
		require.Equal(t, n.PutResult.Err, "")

		added <- n.PutResult.Cid
	}
	pn.Put(ctx, &PutArgs{
		Path:      p,
		ChunkSize: 1024,
	})
	<-added

	ref, err := pn.getRef("")
	require.NoError(t, err)
	require.NoError(t, pn.exch.Index().SetRef(ref))

	got := make(chan *GetResult, 2)
	cn.notify = func(n Notify) {
		require.Equal(t, n.GetResult.Err, "")
		got <- n.GetResult
	}
	cn.Get(ctx, &GetArgs{
		Cid:      fmt.Sprintf("/%s/data1", ref.PayloadCID.String()),
		Strategy: "SelectFirst",
		Timeout:  1,
	})
	res := <-got
	require.NotEqual(t, "", res.DealID)

	res = <-got
	require.Greater(t, res.TransLatSeconds, 0.0)

	// We should be able to request again this time from local storage
	got = make(chan *GetResult, 1)
	cn.notify = func(n Notify) {
		require.Equal(t, n.GetResult.Err, "")
		got <- n.GetResult
	}
	out := filepath.Join(dir, "dataout")
	cn.Get(ctx, &GetArgs{
		Cid:      fmt.Sprintf("/%s/data1", ref.PayloadCID.String()),
		Strategy: "SelectFirst",
		Timeout:  1,
		Out:      out,
	})
	<-got
	dataout := make([]byte, len(data))
	file, err := os.Open(out)
	require.NoError(t, err)

	_, err = file.Read(dataout)
	require.NoError(t, err)
	require.EqualValues(t, data, dataout)
}

func TestList(t *testing.T) {
	blockGen := blocksutil.NewBlockGenerator()
	ctx := context.Background()
	mn := mocknet.New(ctx)

	cn := newTestNode(ctx, mn, t)

	for i := 0; i < 10; i++ {
		require.NoError(t, cn.exch.Index().SetRef(&exchange.DataRef{
			PayloadCID:  blockGen.Next().Cid(),
			PayloadSize: 100,
		}))
	}
	out := make(chan *ListResult, 10)
	cn.notify = func(n Notify) {
		require.Equal(t, n.ListResult.Err, "")
		out <- n.ListResult
		if n.ListResult.Last {
			close(out)
		}
	}
	cn.List(ctx, &ListArgs{})
	for range out {
	}
}

func TestMultipleGet(t *testing.T) {
	bgCtx := context.Background()

	ctx, cancel := context.WithTimeout(bgCtx, 4*time.Second)
	defer cancel()
	mn := mocknet.New(bgCtx)

	pn := newTestNode(bgCtx, mn, t)
	cn := newTestNode(bgCtx, mn, t)
	// cn2 := newTestNode(bgCtx, mn, t)

	require.NoError(t, mn.LinkAll())
	require.NoError(t, mn.ConnectAllButSelf())

	// Let the routing propagate to gossip
	time.Sleep(time.Second)

	dir := t.TempDir()

	data1 := make([]byte, 256000)
	rand.New(rand.NewSource(time.Now().UnixNano())).Read(data1)
	p1 := filepath.Join(dir, "data1")
	err := os.WriteFile(p1, data1, 0666)
	require.NoError(t, err)

	added1 := make(chan string, 1)
	pn.notify = func(n Notify) {
		require.Equal(t, n.PutResult.Err, "")

		added1 <- n.PutResult.Cid
	}
	pn.Put(ctx, &PutArgs{
		Path:      p1,
		ChunkSize: 1024,
	})
	<-added1

	data2 := make([]byte, 124000)
	rand.New(rand.NewSource(time.Now().UnixNano())).Read(data2)
	p2 := filepath.Join(dir, "data2")
	err = os.WriteFile(p2, data2, 0666)
	require.NoError(t, err)

	added2 := make(chan string, 1)
	pn.notify = func(n Notify) {
		require.Equal(t, n.PutResult.Err, "")

		added2 <- n.PutResult.Cid
	}
	pn.Put(ctx, &PutArgs{
		Path:      p2,
		ChunkSize: 1024,
	})
	<-added2

	ref, err := pn.getRef("")
	require.NoError(t, err)
	require.NoError(t, pn.exch.Index().SetRef(ref))

	got1 := make(chan *GetResult, 2)
	cn.notify = func(n Notify) {
		require.Equal(t, n.GetResult.Err, "")
		got1 <- n.GetResult
	}
	cn.Get(ctx, &GetArgs{
		Cid:      fmt.Sprintf("/%s/data1", ref.PayloadCID.String()),
		Strategy: "SelectFirst",
		Timeout:  1,
	})
	res := <-got1
	require.NotEqual(t, "", res.DealID)

	res = <-got1
	require.Greater(t, res.TransLatSeconds, 0.0)

	//@TODO: register keys in index
	//Now let's try to request the second file
	got2 := make(chan *GetResult, 2)
	cn.notify = func(n Notify) {
		require.Equal(t, n.GetResult.Err, "")
		got2 <- n.GetResult
	}
	cn.Get(ctx, &GetArgs{
		Cid:      fmt.Sprintf("/%s/data2", ref.PayloadCID.String()),
		Strategy: "SelectFirst",
		Timeout:  1,
	})
	res = <-got2
	require.NotEqual(t, "", res.DealID)

	res = <-got2
	require.Greater(t, res.TransLatSeconds, 0.0)

	// @BUG: not entirely sure why this doesn't work if data2 isn't fetched first
	// got3 := make(chan *GetResult, 2)
	// cn2.notify = func(n Notify) {
	// 	require.Equal(t, "", n.GetResult.Err)
	// 	got3 <- n.GetResult
	// }
	// cn2.Get(ctx, &GetArgs{
	// 	Cid:      fmt.Sprintf("/%s/data2", ref.PayloadCID.String()),
	// 	Strategy: "SelectFirst",
	// 	Timeout:  1,
	// })
	// res = <-got3
	// require.NotEqual(t, "", res.DealID)

	// res = <-got3
	// require.Greater(t, res.TransLatSeconds, 0.0)
}
