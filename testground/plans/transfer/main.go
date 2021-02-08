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

	datatransfer "github.com/filecoin-project/go-data-transfer"
	dtimpl "github.com/filecoin-project/go-data-transfer/impl"
	dtnet "github.com/filecoin-project/go-data-transfer/network"
	dtgstransport "github.com/filecoin-project/go-data-transfer/transport/graphsync"
	"github.com/filecoin-project/go-multistore"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/go-storedcounter"
	"github.com/ipfs/go-blockservice"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
	dssync "github.com/ipfs/go-datastore/sync"
	"github.com/ipfs/go-graphsync"
	gsimpl "github.com/ipfs/go-graphsync/impl"
	gsnet "github.com/ipfs/go-graphsync/network"
	"github.com/ipfs/go-graphsync/storeutil"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	chunk "github.com/ipfs/go-ipfs-chunker"
	offline "github.com/ipfs/go-ipfs-exchange-offline"
	files "github.com/ipfs/go-ipfs-files"
	ipldformat "github.com/ipfs/go-ipld-format"
	dag "github.com/ipfs/go-merkledag"
	"github.com/ipfs/go-unixfs/importer/balanced"
	"github.com/ipfs/go-unixfs/importer/helpers"
	"github.com/ipld/go-ipld-prime"
	lp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/myelnet/go-hop-exchange/retrieval"
	"github.com/myelnet/go-hop-exchange/retrieval/deal"
	"github.com/testground/sdk-go/run"
	"github.com/testground/sdk-go/runtime"
	"github.com/testground/sdk-go/sync"
)

func main() {
	run.InvokeMap(testcases)
}

var testcases = map[string]interface{}{
	"pick_role": run.InitializedTestCaseFn(runPickrole),
	"duo_role":  run.InitializedTestCaseFn(runDuorole),
}

// runDuorole means all nodes are both client and provider so each will
// import some random bytes in the blockstore and publish the cid to all the peers
// then collect any cid it receives to retrieve them all from whomever sent it
func runDuorole(runenv *runtime.RunEnv, initCtx *run.InitContext) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Wait until all instances in this test run have signalled.
	initCtx.MustWaitAllInstancesInitialized(ctx)

	node, err := spawn(ctx, runenv, initCtx)
	if err != nil {
		return err
	}
	dt, err := startDataTransfer(ctx, node.Ds, node.Host, node.Gs, "client")
	if err != nil {
		return err
	}
	err = dt.RegisterVoucherType(&deal.Proposal{}, &DTValidator{})
	if err != nil {
		return err
	}

	// The content topic lets other peers know when content was imported
	contentTopic := sync.NewTopic("content", new(ContentLocation))
	// Store all the content added by providers in a slice
	content := make([]*ContentLocation, 0, runenv.TestInstanceCount-1)

	contentCh := make(chan *ContentLocation)
	sctx, scancel := context.WithCancel(ctx)
	sub := initCtx.SyncClient.MustSubscribe(sctx, contentTopic, contentCh)

	// Wait for all peers to signal that they're done with the connection phase.
	initCtx.SyncClient.MustSignalAndWait(ctx, "connected", runenv.TestInstanceCount)

	// generate 1600 bytes of random data
	data := make([]byte, 1600)
	rand.New(rand.NewSource(time.Now().UnixNano())).Read(data)

	file, err := ioutil.TempFile("/tmp", "data")
	if err != nil {
		scancel()
		return err
	}
	defer os.Remove(file.Name())

	_, err = file.Write(data)
	if err != nil {
		scancel()
		return err
	}
	fid, err := importFile(ctx, file.Name(), node.DAG)
	if err != nil {
		scancel()
		return err
	}

	initCtx.SyncClient.MustPublish(ctx, contentTopic, &ContentLocation{fid, node.Host.ID()})

	for len(content) < cap(content) {
		select {
		case c := <-contentCh:
			content = append(content, c)
		case err := <-sub.Done():
			scancel()
			return err
		}
	}
	scancel()

	done := make(chan datatransfer.Event, len(content))
	unsubscribe := dt.SubscribeToEvents(func(event datatransfer.Event, channelState datatransfer.ChannelState) {
		// runenv.RecordMessage(datatransfer.Events[event.Code])
		if channelState.Status() == datatransfer.Completed || event.Code == datatransfer.Error {
			done <- event
		}
	})
	defer unsubscribe()

	for _, c := range content {

		pricePerByte := abi.NewTokenAmount(1000)
		paymentInterval := uint64(10000)
		paymentIntervalIncrease := uint64(1000)
		unsealPrice := big.Zero()
		params, err := deal.NewParams(pricePerByte, paymentInterval, paymentIntervalIncrease, retrieval.AllSelector(), nil, unsealPrice)

		voucher := &deal.Proposal{
			PayloadCID: c.Cid,
			ID:         deal.ID(0),
			Params:     params,
		}

		_, err = dt.OpenPullDataChannel(ctx, c.Peer, voucher, c.Cid, retrieval.AllSelector())
		if err != nil {
			return err
		}
		runenv.RecordMessage("openned pull data channel")
	}

	for i := 0; i < cap(done); i++ {
		runenv.RecordMessage("waiting for transfer to complete")
		select {
		case event := <-done:
			if event.Code == datatransfer.Error {
				return fmt.Errorf("Failed to retrieve: %v", event.Message)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	initCtx.SyncClient.MustSignalAndWait(ctx, "completed", runenv.TestInstanceCount)

	_ = node.Host.Close()
	return nil

}

// runPickrole forces a node to decide if they will be a client or a provider and only fill that role
func runPickrole(runenv *runtime.RunEnv, initCtx *run.InitContext) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	role := runenv.StringParam("role")

	// Wait until all instances in this test run have signalled.
	initCtx.MustWaitAllInstancesInitialized(ctx)

	node, err := spawn(ctx, runenv, initCtx)
	if err != nil {
		return err
	}

	node.Dt, err = startDataTransfer(ctx, node.Ds, node.Host, node.Gs, role)
	if err != nil {
		return err
	}

	// The content topic lets clients knows about providers that added new content
	contentTopic := sync.NewTopic("content", new(ContentLocation))

	if role == "provider" {
		err = node.Dt.RegisterVoucherType(&deal.Proposal{}, &DTValidator{})
		if err != nil {
			return err
		}

		// Wait for all peers to signal that they're done with the connection phase.
		initCtx.SyncClient.MustSignalAndWait(ctx, "connected", runenv.TestInstanceCount)

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
		fid, err := importFile(ctx, file.Name(), node.DAG)
		if err != nil {
			return err
		}

		initCtx.SyncClient.MustPublish(ctx, contentTopic, &ContentLocation{fid, node.Host.ID()})

		// Wait for clients to complete any transfer
		initCtx.SyncClient.MustSignalAndWait(ctx, "completed", runenv.TestInstanceCount)
		return node.Host.Close()
	}

	if role == "client" {

		// We shouldn't need a validator since we're a client
		err = node.Dt.RegisterVoucherType(&deal.Proposal{}, nil)
		if err != nil {
			return err
		}
		// Wait for all peers to signal that they're done with the connection phase.
		initCtx.SyncClient.MustSignalAndWait(ctx, "connected", runenv.TestInstanceCount)

		// Store all the content added by providers in a slice
		content := make([]*ContentLocation, 0, runenv.IntParam("providers"))

		contentCh := make(chan *ContentLocation)
		sctx, scancel := context.WithCancel(ctx)
		sub := initCtx.SyncClient.MustSubscribe(sctx, contentTopic, contentCh)

		for len(content) < cap(content) {
			select {
			case c := <-contentCh:
				content = append(content, c)
			case err := <-sub.Done():
				scancel()
				return err
			}
		}
		scancel()

		done := make(chan datatransfer.Event, len(content))
		unsubscribe := node.Dt.SubscribeToEvents(func(event datatransfer.Event, channelState datatransfer.ChannelState) {
			// runenv.RecordMessage(datatransfer.Events[event.Code])
			if channelState.Status() == datatransfer.Completed || event.Code == datatransfer.Error {
				done <- event
			}
		})
		defer unsubscribe()

		for _, c := range content {

			pricePerByte := abi.NewTokenAmount(1000)
			paymentInterval := uint64(10000)
			paymentIntervalIncrease := uint64(1000)
			unsealPrice := big.Zero()
			params, err := deal.NewParams(pricePerByte, paymentInterval, paymentIntervalIncrease, retrieval.AllSelector(), nil, unsealPrice)

			voucher := &deal.Proposal{
				PayloadCID: c.Cid,
				ID:         deal.ID(0),
				Params:     params,
			}

			_, err = node.Dt.OpenPullDataChannel(ctx, c.Peer, voucher, c.Cid, retrieval.AllSelector())
			if err != nil {
				return err
			}
			runenv.RecordMessage("openned pull data channel")
		}

		for i := 0; i < cap(done); i++ {
			runenv.RecordMessage("waiting for transfer to complete")
			select {
			case event := <-done:
				if event.Code == datatransfer.Error {
					return fmt.Errorf("Failed to retrieve: %v", event.Message)
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}

	}
	initCtx.SyncClient.MustSignalAndWait(ctx, "completed", runenv.TestInstanceCount)

	_ = node.Host.Close()
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

func startDataTransfer(ctx context.Context, ds datastore.Batching, h host.Host, gs graphsync.GraphExchange, role string) (datatransfer.Manager, error) {
	var err error
	sc := storedcounter.New(ds, datastore.NewKey("nextDTID"))
	dtNet := dtnet.NewFromLibp2pHost(h)
	dtStore := namespace.Wrap(ds, datastore.NewKey(fmt.Sprintf("/datatransfer/%s", role)))
	dtTmpDir, err := ioutil.TempDir("", "dt-tmp")
	if err != nil {
		return nil, err
	}
	dtTransport := dtgstransport.NewTransport(h.ID(), gs)
	dt, err := dtimpl.NewDataTransfer(dtStore, dtTmpDir, dtNet, dtTransport, sc)
	if err != nil {
		return nil, err
	}

	ready := make(chan error, 1)
	dt.OnReady(func(err error) {
		ready <- err
	})
	err = dt.Start(ctx)

	select {
	case <-ctx.Done():
		return nil, err
	case err := <-ready:
		if err != nil {
			return nil, err
		}
		return dt, nil
	}
}

// Node wraps all the ipfs components
type Node struct {
	Ds              datastore.Batching
	Bs              blockstore.Blockstore
	DAG             ipldformat.DAGService
	Host            host.Host
	Gs              graphsync.GraphExchange
	DTNet           dtnet.DataTransferNetwork
	DTStore         datastore.Batching
	DTTmpDir        string
	DTStoredCounter *storedcounter.StoredCounter
	Dt              datatransfer.Manager
	Ms              *multistore.MultiStore
	Counter         *storedcounter.StoredCounter
}

func spawn(ctx context.Context, runenv *runtime.RunEnv, initCtx *run.InitContext) (*Node, error) {
	ds := dssync.MutexWrap(datastore.NewMapDatastore())

	bs := blockstore.NewIdStore(blockstore.NewBlockstore(ds))

	// We need to listen on (and advertise) our data network IP address, so we
	// obtain it from the NetClient.
	ip := initCtx.NetClient.MustGetDataNetworkIP()

	// create a new libp2p Host that listens on a random TCP port
	h, err := lp2p.New(ctx, lp2p.ListenAddrStrings(fmt.Sprintf("/ip4/%s/tcp/0", ip)))
	if err != nil {
		return nil, err
	}

	gsNet := gsnet.NewFromLibp2pHost(h)
	gs := gsimpl.New(ctx,
		gsNet,
		storeutil.LoaderForBlockstore(bs),
		storeutil.StorerForBlockstore(bs),
	)

	DAG := dag.NewDAGService(blockservice.New(bs, offline.Exchange(bs)))

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
			return nil, err
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
			return nil, err
		}
	}

	runenv.RecordMessage("done dialling my peers")

	return &Node{
		Ds:   ds,
		Bs:   bs,
		DAG:  DAG,
		Host: h,
		Gs:   gs,
	}, nil
}

// ContentLocation represents a provider and the content it provides
type ContentLocation struct {
	Cid  cid.Cid
	Peer peer.ID
}

// DTValidator is our custom data transfer voucher validator
type DTValidator struct{}

func (v *DTValidator) ValidatePush(sender peer.ID, voucher datatransfer.Voucher, baseCid cid.Cid, selector ipld.Node) (datatransfer.VoucherResult, error) {
	return nil, nil
}

func (v *DTValidator) ValidatePull(receiver peer.ID, voucher datatransfer.Voucher, baseCid cid.Cid, selector ipld.Node) (datatransfer.VoucherResult, error) {
	return nil, nil
}
