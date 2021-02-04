package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"os"
	"time"

	bserv "github.com/ipfs/go-blockservice"
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
	dag "github.com/ipfs/go-merkledag"
	"github.com/ipfs/go-unixfs/importer/balanced"
	"github.com/ipfs/go-unixfs/importer/helpers"
	lp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/peer"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/myelnet/go-hop-exchange"
	"github.com/myelnet/go-hop-exchange/wallet"
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
	// State notifying when a client has added content
	addedState := sync.State("added")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	role := runenv.StringParam("role")
	runenv.RecordMessage("Started instance with role: %s", role)

	// Wait until all instances in this test run have signalled.
	initCtx.MustWaitAllInstancesInitialized(ctx)

	rpath, err := ioutil.TempDir("", "tmp-repo")
	if err != nil {
		return err
	}

	ds := dssync.MutexWrap(datastore.NewMapDatastore())

	bs := blockstore.NewIdStore(blockstore.NewBlockstore(ds))

	ks := wallet.NewMemKeystore()

	// We need to listen on (and advertise) our data network IP address, so we
	// obtain it from the NetClient.
	ip := initCtx.NetClient.MustGetDataNetworkIP()

	// create a new libp2p Host that listens on a random TCP port
	h, err := lp2p.New(ctx, lp2p.ListenAddrStrings(fmt.Sprintf("/ip4/%s/tcp/0", ip)))
	if err != nil {
		return err
	}

	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		return err
	}

	gsNet := gsnet.NewFromLibp2pHost(h)
	gs := gsimpl.New(ctx,
		gsNet,
		storeutil.LoaderForBlockstore(bs),
		storeutil.StorerForBlockstore(bs),
	)

	exch, err := hop.NewExchange(
		ctx,
		hop.WithBlockstore(bs),
		hop.WithPubSub(ps),
		hop.WithHost(h),
		hop.WithDatastore(ds),
		hop.WithGraphSync(gs),
		hop.WithRepoPath(rpath),
		hop.WithKeystore(ks),
	)
	if err != nil {
		return err
	}

	blocks := bserv.New(bs, exch)
	DAG := dag.NewDAGService(blocks)

	// Record our listen addrs.
	runenv.RecordMessage("my listen addrs: %v", h.Addrs())

	info := &peer.AddrInfo{ID: h.ID(), Addrs: h.Addrs()}
	// the peers topic where all instances will advertise their AddrInfo.
	peersTopic := sync.NewTopic("peers", new(peer.AddrInfo))
	// initialize a slice to store the AddrInfos of all other peers in the run.
	peers := make([]*peer.AddrInfo, 0, runenv.TestInstanceCount-1)

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
			peers = append(peers, ai)
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
		if err := h.Connect(ctx, *ai); err != nil {
			return err
		}
	}

	runenv.RecordMessage("done dialling my peers")

	// Wait for all peers to signal that they're done with the connection phase.
	initCtx.SyncClient.MustSignalAndWait(ctx, "connected", runenv.TestInstanceCount)

	if role == "client" {
		// generate 1600 bytes of random data
		data := make([]byte, 1600)
		rand.New(rand.NewSource(time.Now().UnixNano())).Read(data)

		file, err := ioutil.TempFile("/tmp", "data")
		if err != nil {
			return err
		}
		defer os.Remove(file.Name())

		_, err = file.Write(data)
		if err != nil {
			return err
		}
		_, err = importFile(ctx, file.Name(), DAG)
		if err != nil {
			return err
		}
		initCtx.SyncClient.MustSignalEntry(ctx, addedState)
		return nil
	}

	err = <-initCtx.SyncClient.MustBarrier(ctx, addedState, runenv.IntParam("clients")).C
	if err != nil {
		return err
	}

	_ = h.Close()
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

	if err != nil {
		return cid.Undef, err
	}
	return nd.Cid(), nil
}
