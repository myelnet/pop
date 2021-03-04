package node

import (
	"context"
	"io/ioutil"
	"math/rand"
	"os"
	"testing"
	"time"

	keystore "github.com/ipfs/go-ipfs-keystore"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/myelnet/go-hop-exchange"
	"github.com/myelnet/go-hop-exchange/supply"
	"github.com/myelnet/go-hop-exchange/testutil"
	"github.com/stretchr/testify/require"
)

func newTestNode(ctx context.Context, mn mocknet.Mocknet, t *testing.T) *node {
	var err error

	tn := testutil.NewTestNode(mn, t)
	tn.SetupGraphSync(ctx)

	nd := &node{}
	nd.ds = tn.Ds
	nd.bs = tn.Bs
	nd.dag = tn.DAG
	nd.host = tn.Host
	nd.gs = tn.Gs
	nd.ps, err = pubsub.NewGossipSub(ctx, nd.host)
	require.NoError(t, err)

	settings := hop.Settings{
		Datastore:  nd.ds,
		Blockstore: nd.bs,
		Host:       nd.host,
		PubSub:     nd.ps,
		GraphSync:  nd.gs,
		RepoPath:   t.TempDir(),
		Keystore:   keystore.NewMemKeystore(),
		Regions:    []supply.Region{supply.Regions["Global"]},
	}

	nd.exch, err = hop.NewExchange(ctx, settings)
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
