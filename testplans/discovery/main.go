package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/filecoin-project/go-multistore"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"
	gsimpl "github.com/ipfs/go-graphsync/impl"
	gsnet "github.com/ipfs/go-graphsync/network"
	"github.com/ipfs/go-graphsync/storeutil"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	chunk "github.com/ipfs/go-ipfs-chunker"
	files "github.com/ipfs/go-ipfs-files"
	ipldformat "github.com/ipfs/go-ipld-format"
	"github.com/ipfs/go-unixfs/importer/balanced"
	"github.com/ipfs/go-unixfs/importer/helpers"
	"github.com/libp2p/go-libp2p"
	connmgr "github.com/libp2p/go-libp2p-connmgr"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/routing"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	tptu "github.com/libp2p/go-libp2p-transport-upgrader"
	"github.com/libp2p/go-tcp-transport"
	"github.com/myelnet/go-hop-exchange/wallet"
	"github.com/myelnet/pop"
	"github.com/myelnet/pop/supply"
	"github.com/testground/sdk-go/run"
	"github.com/testground/sdk-go/runtime"
	"github.com/testground/sdk-go/sync"
)

func main() {
	run.InvokeMap(testcases)
}

var testcases = map[string]interface{}{
	"gossip-single-region": run.InitializedTestCaseFn(runGossip),
}

func runGossip(runenv *runtime.RunEnv, initCtx *run.InitContext) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	group := runenv.TestGroupID

	// Wait until all instances in this test run have signalled.
	initCtx.MustWaitAllInstancesInitialized(ctx)

	rpath, err := runenv.CreateRandomDirectory("", 0)
	if err != nil {
		return err
	}

	ds := dssync.MutexWrap(datastore.NewMapDatastore())

	bs := blockstore.NewBlockstore(ds)

	ms, err := multistore.NewMultiDstore(ds)
	if err != nil {
		return err
	}

	ks := wallet.NewMemKeystore()

	// We need to listen on (and advertise) our data network IP address, so we
	// obtain it from the NetClient.
	ip := initCtx.NetClient.MustGetDataNetworkIP()

	var idht *dht.IpfsDHT

	// create a new libp2p Host that listens on a random TCP port
	h, err := libp2p.New(ctx,
		libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/%s/tcp/0", ip)),
		// Use only the TCP transport without reuseport.
		libp2p.Transport(func(u *tptu.Upgrader) *tcp.TcpTransport {
			tpt := tcp.NewTCPTransport(u)
			tpt.DisableReuseport = true
			return tpt
		}),
		// Control the maximum number of simultaneous connections a node can have
		libp2p.ConnectionManager(connmgr.NewConnManager(
			20,            // Lowwater
			60,            // HighWater,
			1*time.Second, // GracePeriod
		)),
		libp2p.DisableRelay(),
		// All peer discovery happens via the dht and a single bootstrap peer
		libp2p.Routing(func(h host.Host) (routing.PeerRouting, error) {
			idht, err = dht.New(ctx, h)
			return idht, err
		}),
	)
	if err != nil {
		return err
	}

	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		return err
	}

	gs := gsimpl.New(ctx,
		gsnet.NewFromLibp2pHost(h),
		storeutil.LoaderForBlockstore(bs),
		storeutil.StorerForBlockstore(bs),
	)

	settings := pop.Settings{
		Datastore:  ds,
		Blockstore: bs,
		MultiStore: ms,
		Host:       h,
		PubSub:     ps,
		GraphSync:  gs,
		RepoPath:   rpath,
		Keystore:   ks,
		Regions:    []supply.Region{supply.Regions["Global"]},
	}

	exch, err := pop.NewExchange(ctx, settings)
	if err != nil {
		return err
	}

	runenv.RecordMessage("started exchange")

	info := host.InfoFromHost(h)

	initCtx.SyncClient.MustPublish(ctx, peersTopic, info)

	peers, err := waitForPeers(ctx, runenv, initCtx.SyncClient, h.ID())
	if err != nil {
		return err
	}

	peers = RandomTopology{2}.SelectPeers(peers)

	if err := connectTopology(ctx, peers, h); err != nil {
		return err
	}

	runenv.RecordMessage("topology %d", len(peers))

	initCtx.SyncClient.MustSignalAndWait(ctx, "connected", runenv.TestInstanceCount)

	runenv.RecordMessage("connected to %d peers", len(h.Network().Peers()))

	// The content topic lets other peers know when content was imported
	contentTopic := sync.NewTopic("content", new(cid.Cid))

	seqNum, err := getGroupSeqNum(ctx, initCtx.SyncClient, info, group)
	if err != nil {
		return err
	}

	// Get a random  provider to provide we store a random file
	if group == "providers" && seqNum == 1 {
		fpath, err := runenv.CreateRandomFile("", 256000)
		if err != nil {
			return err
		}
		storeID := ms.Next()
		store, err := ms.Get(storeID)

		fid, err := importFile(ctx, fpath, store.DAG)
		if err != nil {
			return err
		}
		if err := exch.Supply().Register(fid, storeID); err != nil {
			return err
		}

		initCtx.SyncClient.MustPublish(ctx, contentTopic, &fid)
	}

	if group == "clients" {
		// We expect a single CID
		contentCh := make(chan *cid.Cid, 1)
		sctx, scancel := context.WithCancel(ctx)
		defer scancel()
		sub := initCtx.SyncClient.MustSubscribe(sctx, contentTopic, contentCh)

		select {
		case c := <-contentCh:
			session, err := exch.NewSession(ctx, *c)
			if err != nil {
				return err
			}
			runenv.RecordMessage("querying gossip for content %s", c)

			offer, err := session.QueryGossip(ctx)
			if err != nil {
				return err
			}
			runenv.RecordMessage("got an offer %v", offer)
		case err := <-sub.Done():
			scancel()
			return err
		}
	}

	// Wait for clients to complete any transfer
	_, err = initCtx.SyncClient.SignalAndWait(ctx, "completed", runenv.TestInstanceCount)
	return err
}

func importFile(ctx context.Context, fpath string, dg ipldformat.DAGService) (cid.Cid, error) {
	f, err := os.Open(fpath)
	if err != nil {
		return cid.Undef, err
	}

	var buf bytes.Buffer
	tr := io.TeeReader(f, &buf)
	file := files.NewReaderFile(tr)

	// import to UnixFS
	bufferedDS := ipldformat.NewBufferedDAG(ctx, dg)

	params := helpers.DagBuilderParams{
		Maxlinks:   1024,
		RawLeaves:  true,
		CidBuilder: nil,
		Dagserv:    bufferedDS,
	}

	db, err := params.New(chunk.NewSizeSplitter(file, int64(1<<10)))
	if err != nil {
		return cid.Undef, err
	}

	nd, err := balanced.Layout(db)
	if err != nil {
		return cid.Undef, err
	}

	err = bufferedDS.Commit()

	return nd.Cid(), err
}

// WaitRoutingTable waits until the routing table is not empty.
func WaitRoutingTable(ctx context.Context, runenv *runtime.RunEnv, d *dht.IpfsDHT) error {
	//ctxt, cancel := context.WithTimeout(ctx, time.Second*10)
	//defer cancel()
	for {
		if d.RoutingTable().Size() > 0 {
			return nil
		}

		t := time.NewTimer(time.Second * 10)

		select {
		case <-time.After(200 * time.Millisecond):
		case <-t.C:
			runenv.RecordMessage("waiting on routing table")
		case <-ctx.Done():
			peers := d.Host().Network().Peers()
			errStr := fmt.Sprintf("empty rt. %d peer conns. they are %v", len(peers), peers)
			runenv.RecordMessage(errStr)
			return fmt.Errorf(errStr)
		}
	}
}

func getGroupSeqNum(ctx context.Context, client sync.Client, info *peer.AddrInfo, group string) (int64, error) {
	topic := sync.NewTopic("group-"+string(group), &peer.AddrInfo{})
	return client.Publish(ctx, topic, info)
}
