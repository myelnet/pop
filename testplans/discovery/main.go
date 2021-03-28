package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	goruntime "runtime"
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
	imported := sync.State("imported")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	group := runenv.TestGroupID

	// Wait until all instances in this test run have signalled.
	initCtx.MustWaitAllInstancesInitialized(ctx)

	if err := shapeTraffic(ctx, runenv, initCtx.NetClient); err != nil {
		return err
	}

	rpath, err := runenv.CreateRandomDirectory("", 0)
	if err != nil {
		return err
	}
	// We need to listen on (and advertise) our data network IP address, so we
	// obtain it from the NetClient.
	ip := initCtx.NetClient.MustGetDataNetworkIP()

	low := runenv.IntParam("min_conns")
	hiw := runenv.IntParam("max_conns")
	settings, err := defaultSettings(ctx, rpath, ip, low, hiw)
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

	peers = RandomTopology{runenv.IntParam("bootstrap")}.SelectPeers(peers)

	if err := connectTopology(ctx, runenv, peers, h); err != nil {
		return err
	}

	initCtx.SyncClient.MustSignalAndWait(ctx, "connected", runenv.TestInstanceCount)

	runenv.RecordMessage("connected to %d peers", len(h.Network().Peers()))

	// The content topic lets other peers know when content was imported
	contentTopic := sync.NewTopic("content", new(supply.PRecord))

	// Any node part of the provider to provide a random file
	if group == "providers" {
		storeID := ms.Next()
		store, err := ms.Get(storeID)

		fid, err := importFile(ctx, "/fixture.jpeg", store.DAG)
		if err != nil {
			return err
		}
		if err := exch.Supply().Register(fid, storeID); err != nil {
			return err
		}

		// Only the first one in the group needs to publish the CID as it's the same file
		if int(initCtx.GroupSeq) == 0 {
			initCtx.SyncClient.MustPublish(ctx, contentTopic, &supply.PRecord{
				PayloadCID: fid,
				Provider:   h.ID(),
			})
		}
		runenv.RecordMessage("imported content")
		initCtx.SyncClient.MustSignalEntry(ctx, imported)
	}

	if group == "clients" {
		// We expect a CID published by only one of the providers
		contentCh := make(chan *supply.PRecord, 1)
		sctx, scancel := context.WithCancel(ctx)
		defer scancel()
		_ = initCtx.SyncClient.MustSubscribe(sctx, contentTopic, contentCh)

		// Wait for all providers to have imported the file
		<-initCtx.SyncClient.MustBarrier(ctx, imported, runenv.IntParam("providers")).C

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

			goruntime.GC()
			session, err := exch.NewSession(ctx, c.PayloadCID)
			if err != nil {
				return err
			}
			runenv.RecordMessage("querying gossip for content %s", c.PayloadCID)

			t := time.Now()

			err = session.QueryGossip(ctx)
			if err != nil {
				return err
			}

			offer, err := session.OfferQueue().Peek(ctx)
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

func defaultSettings(ctx context.Context, rpath string, ip net.IP, low, hiw int) (pop.Settings, error) {
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
			low,           // Lowwater
			hiw,           // HighWater,
			1*time.Second, // GracePeriod
		)),
		libp2p.DisableRelay(),
		// All peer discovery happens via the dht and a single bootstrap peer
		libp2p.Routing(func(h host.Host) (routing.PeerRouting, error) {
			return dht.New(ctx, h)
		}),
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
