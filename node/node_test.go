package node

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	keystore "github.com/ipfs/go-ipfs-keystore"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/myelnet/pop/exchange"
	"github.com/myelnet/pop/internal/testutil"
	"github.com/stretchr/testify/require"
)

func newTestNode(ctx context.Context, mn mocknet.Mocknet, t *testing.T) *node {
	var err error

	tn := testutil.NewTestNode(mn, t)
	tn.SetupGraphSync(ctx)

	nd := &node{}
	nd.ds = tn.Ds
	nd.bs = tn.Bs
	nd.ms = tn.Ms
	nd.dag = tn.DAG
	nd.host = tn.Host

	opts := exchange.Options{
		Blockstore: nd.bs,
		MultiStore: nd.ms,
		RepoPath:   t.TempDir(),
		Keystore:   keystore.NewMemKeystore(),
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

func TestAdd(t *testing.T) {
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
		require.Equal(t, n.AddResult.Err, "")

		added <- n.AddResult.Cid
	}
	cn.Add(ctx, &AddArgs{
		Path:      file.Name(),
		ChunkSize: 1024,
	})
	<-added
}

func TestStatusAndPack(t *testing.T) {
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
		require.Equal(t, n.AddResult.Err, "")

		added <- n.AddResult.Cid
	}
	cn.Add(ctx, &AddArgs{
		Path:      p1,
		ChunkSize: 1024,
	})
	<-added

	cn.Add(ctx, &AddArgs{
		Path:      p2,
		ChunkSize: 1024,
	})
	<-added

	stat := make(chan string, 1)
	cn.notify = func(n Notify) {
		require.Equal(t, n.StatusResult.Err, "")

		stat <- n.StatusResult.Output
	}
	cn.Status(ctx, &StatusArgs{})
	<-stat

	pac := make(chan string, 1)
	cn.notify = func(n Notify) {
		require.Equal(t, n.PackResult.Err, "")

		pac <- n.PackResult.DataCID
	}
	cn.Pack(ctx, &PackArgs{})
	out := <-pac
	require.NotEqual(t, out, "")

	// Export back out
	path := fmt.Sprintf("/%s/data2", out)
	loc := make(chan bool, 1)
	cn.notify = func(n Notify) {
		require.Equal(t, n.GetResult.Err, "")

		loc <- n.GetResult.Local
	}
	newp := filepath.Join(dir, "newdata2")
	cn.Get(ctx, &GetArgs{
		Cid: path,
		Out: newp,
	})
	<-loc

	f, err := os.Open(newp)
	require.NoError(t, err)

	newb, err := io.ReadAll(f)
	require.NoError(t, err)
	require.Equal(t, data2, newb)
}
