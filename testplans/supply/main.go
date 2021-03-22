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
	keystore "github.com/ipfs/go-ipfs-keystore"
	ipldformat "github.com/ipfs/go-ipld-format"
	"github.com/ipfs/go-unixfs/importer/balanced"
	"github.com/ipfs/go-unixfs/importer/helpers"
	lp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
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
	"supply": run.InitializedTestCaseFn(runSupply),
}

func runSupply(runenv *runtime.RunEnv, initCtx *run.InitContext) error {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
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

	ks := keystore.NewMemKeystore()

	// We need to listen on (and advertise) our data network IP address, so we
	// obtain it from the NetClient.
	ip := initCtx.NetClient.MustGetDataNetworkIP()

	// create a new libp2p Host that listens on a random TCP port
	h, err := lp2p.New(ctx,
		lp2p.ListenAddrStrings(fmt.Sprintf("/ip4/%s/tcp/0", ip)),
		lp2p.DisableRelay(),
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

	info := host.InfoFromHost(h)
	// the peers topic where all instances will advertise their AddrInfo.
	peersTopic := sync.NewTopic("peers", new(peer.AddrInfo))
	// initialize a slice to store the AddrInfos of all other peers in the run.
	peers := make([]peer.AddrInfo, 0, runenv.TestInstanceCount-1)

	// Publish our own.
	initCtx.SyncClient.MustPublish(ctx, peersTopic, info)

	// Now subscribe to the peers topic and consume all addresses, storing them
	// in the peers slice.
	peersCh := make(chan *peer.AddrInfo)
	sctx, scancel := context.WithCancel(ctx)
	sub := initCtx.SyncClient.MustSubscribe(sctx, peersTopic, peersCh)

	// Receive the expected number of AddrInfos.
	for len(peers) < cap(peers) {
		select {
		case ai := <-peersCh:
			if ai.ID == h.ID() {
				continue // skip over ourselves.
			}
			peers = append(peers, *ai)
		case err := <-sub.Done():
			scancel()
			return err
		}
	}
	scancel() // cancels the Subscription.

	// Note: we sidestep simultaneous connect issues by ONLY connecting to peers
	// who published their addresses before us (this is enough to dedup and avoid
	// two peers dialling each other at the same time).
	//
	// We can do this because sync service pubsub is ordered.
	for _, ai := range peers {
		if ai.ID == h.ID() {
			break
		}
		if err := h.Connect(ctx, ai); err != nil {
			return err
		}
	}

	runenv.RecordMessage("done dialling my peers")

	// Wait for all peers to signal that they're done with the connection phase.
	initCtx.SyncClient.MustSignalAndWait(ctx, "connected", runenv.TestInstanceCount)

	if group == "clients" {

		fpath, err := runenv.CreateRandomFile("", 16000)
		if err != nil {
			return err
		}
		storeID := ms.Next()
		store, err := ms.Get(storeID)
		if err != nil {
			return err
		}

		fid, err := importFile(ctx, fpath, store.DAG)
		if err != nil {
			return err
		}

		if err := exch.Supply().Register(fid, storeID); err != nil {
			return err
		}

		runenv.RecordMessage("dispatching to providers")

		res, err := exch.Supply().Dispatch(supply.Request{
			PayloadCID: fid,
			Size:       uint64(16000),
		})
		if err != nil {
			return err
		}

		for i := 0; i < res.Count; i++ {
			rec, err := res.Next(ctx)
			if err != nil {
				return err
			}
			runenv.RecordMessage("sent to peer %s", rec.Provider)
		}
	}

	_, err = initCtx.SyncClient.SignalAndWait(ctx, "completed", runenv.TestInstanceCount)
	if err != nil {
		return err
	}
	return nil
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
