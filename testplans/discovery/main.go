package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
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
	keystore "github.com/ipfs/go-ipfs-keystore"
	ipldformat "github.com/ipfs/go-ipld-format"
	"github.com/ipfs/go-unixfs/importer/balanced"
	"github.com/ipfs/go-unixfs/importer/helpers"
	"github.com/libp2p/go-libp2p"
	connmgr "github.com/libp2p/go-libp2p-connmgr"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/routing"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
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
	"gossip": run.InitializedTestCaseFn(runGossip),
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
	// We need to listen on (and advertise) our data network IP address, so we
	// obtain it from the NetClient.
	ip := initCtx.NetClient.MustGetDataNetworkIP()

	settings, err := defaultSettings(ctx, rpath, ip)
	if err != nil {
		return err
	}
	h := settings.Host
	ms := settings.MultiStore

	settings.Regions = supply.ParseRegions(runenv.StringArrayParam("regions"))

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

	peers = RandomTopology{runenv.IntParam("conn_per_peer")}.SelectPeers(peers)

	if err := connectTopology(ctx, runenv, peers, h); err != nil {
		return err
	}

	initCtx.SyncClient.MustSignalAndWait(ctx, "connected", runenv.TestInstanceCount)

	runenv.RecordMessage("connected to %d peers", len(h.Network().Peers()))

	// The content topic lets other peers know when content was imported
	contentTopic := sync.NewTopic("content", new(supply.PRecord))

	// Get a random  provider to provide a random file
	if group == "providers" && int(initCtx.GroupSeq) <= runenv.IntParam("replication") {
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

		initCtx.SyncClient.MustPublish(ctx, contentTopic, &supply.PRecord{
			PayloadCID: fid,
			Provider:   h.ID(),
		})
		runenv.RecordMessage("published content")
	}

	if group == "clients" {
		// We expect a single CID
		contentCh := make(chan *supply.PRecord, 1)
		sctx, scancel := context.WithCancel(ctx)
		defer scancel()
		_ = initCtx.SyncClient.MustSubscribe(sctx, contentTopic, contentCh)

		select {
		case c := <-contentCh:
			// need to wait a sec otherwise pubsub message might be sent too early
			time.Sleep(1 * time.Second)
			conns := h.Network().ConnsToPeer(c.Provider)
			if len(conns) == 0 {
				runenv.RecordMessage("peer not directly connected to provider")
			}
			for _, conn := range conns {
				runenv.RecordMessage("connected to provider. local addr: %s remote addr: %s\n",
					conn.LocalMultiaddr(), conn.RemoteMultiaddr())
			}

			session, err := exch.NewSession(ctx, c.PayloadCID)
			if err != nil {
				return err
			}
			runenv.RecordMessage("querying gossip for content %s", c.PayloadCID)

			t := time.Now()

			offer, err := session.QueryGossip(ctx)
			if err != nil {
				return err
			}

			runenv.RecordMessage("got an offer from %s in %d ns", offer.PeerID, time.Since(t).Nanoseconds())
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	_, err = initCtx.SyncClient.SignalAndWait(ctx, "completed", runenv.TestInstanceCount)
	if err != nil {
		return err
	}
	runenv.RecordSuccess()
	return nil
}

func defaultSettings(ctx context.Context, rpath string, ip net.IP) (pop.Settings, error) {
	ds := dssync.MutexWrap(datastore.NewMapDatastore())

	bs := blockstore.NewBlockstore(ds)

	ms, err := multistore.NewMultiDstore(ds)
	if err != nil {
		return pop.Settings{}, err
	}

	ks := keystore.NewMemKeystore()

	// create a new libp2p Host that listens on a random TCP port
	h, err := libp2p.New(ctx,
		libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/%s/tcp/0", ip)),
		// Control the maximum number of simultaneous connections a node can have
		libp2p.ConnectionManager(connmgr.NewConnManager(
			20,            // Lowwater
			60,            // HighWater,
			1*time.Second, // GracePeriod
		)),
		libp2p.DisableRelay(),
		// All peer discovery happens via the dht and a single bootstrap peer
		libp2p.Routing(func(h host.Host) (routing.PeerRouting, error) {
			return dht.New(ctx, h)
		}),
		// Running without security because of a bug
		// see https://github.com/libp2p/go-libp2p-noise/issues/70
		libp2p.NoSecurity,
	)
	if err != nil {
		return pop.Settings{}, err
	}

	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		return pop.Settings{}, err
	}

	gs := gsimpl.New(ctx,
		gsnet.NewFromLibp2pHost(h),
		storeutil.LoaderForBlockstore(bs),
		storeutil.StorerForBlockstore(bs),
	)

	return pop.Settings{
		Datastore:  ds,
		Blockstore: bs,
		MultiStore: ms,
		Host:       h,
		PubSub:     ps,
		GraphSync:  gs,
		RepoPath:   rpath,
		Keystore:   ks,
		Regions:    []supply.Region{supply.Regions["Global"]},
	}, nil

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
