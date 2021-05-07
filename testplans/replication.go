package main

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"
	"github.com/libp2p/go-eventbus"
	"github.com/libp2p/go-libp2p"
	connmgr "github.com/libp2p/go-libp2p-connmgr"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/routing"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	ex "github.com/myelnet/pop/exchange"
	"github.com/testground/sdk-go/run"
	"github.com/testground/sdk-go/runtime"
)

func runBootstrapSupply(runenv *runtime.RunEnv, initCtx *run.InitContext) error {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	group := runenv.TestGroupID

	// Wait until all instances in this test run have signalled.
	initCtx.MustWaitAllInstancesInitialized(ctx)

	rpath, err := runenv.CreateRandomDirectory("", 0)
	if err != nil {
		return err
	}

	ip := initCtx.NetClient.MustGetDataNetworkIP()
	low := runenv.IntParam("min_conns")
	hiw := runenv.IntParam("max_conns")
	exch, h, err := newNode(ctx, rpath, ip, low, hiw)
	if err != nil {
		return err
	}

	isub, err := h.EventBus().Subscribe(new(ex.IndexEvt), eventbus.BufSize(16))
	if err != nil {
		return err
	}

	runenv.RecordMessage("started exchange")
	txCount := runenv.IntParam("tx_per_provider")

	if group == "providers_1" {

		info := host.InfoFromHost(h)

		// Publish our peer info to the network.
		initCtx.SyncClient.MustPublish(ctx, PeersTopic, info)

		peers, err := WaitForPeers(ctx, runenv, initCtx.SyncClient, h.ID(), runenv.IntParam("providers_1"))
		if err != nil {
			return err
		}

		peers = RandomTopology{Count: runenv.IntParam("bootstrap")}.SelectPeers(peers)

		if err := ConnectTopology(ctx, runenv, peers, h); err != nil {
			return err
		}

		initCtx.SyncClient.MustSignalAndWait(ctx, "connected", runenv.IntParam("providers_1"))

		runenv.RecordMessage("providers_1 connected to %d peers", len(h.Network().Peers()))

		roots := make([]cid.Cid, txCount)
		// Add a number of transactions to the local supply
		for i := 0; i < txCount; i++ {
			fpath, err := runenv.CreateRandomFile(rpath, 256000)
			if err != nil {
				return err
			}

			tx := exch.Tx(ctx)
			tx.SetCacheRF(0)
			err = tx.PutFile(fpath)
			if err != nil {
				return err
			}
			err = tx.Commit()
			if err != nil {
				return err
			}
			roots[i] = tx.Root()
		}

		// Add few random reads
		for i := 0; i < txCount; i++ {
			_, _ = exch.Index().GetRef(roots[rand.Intn(txCount)])
		}

		initCtx.SyncClient.MustSignalEntry(ctx, "providers_1_supply_ready")
	}

	if group == "providers_2" {

		<-initCtx.SyncClient.MustBarrier(ctx, "providers_1_supply_ready", runenv.IntParam("providers_1")).C

		info := host.InfoFromHost(h)

		// Publish our peer info to the network.
		initCtx.SyncClient.MustPublish(ctx, PeersTopic, info)

		peers, err := WaitForPeers(ctx, runenv, initCtx.SyncClient, h.ID(), runenv.IntParam("providers_2"))
		if err != nil {
			return err
		}

		peers = RandomTopology{Count: runenv.IntParam("bootstrap")}.SelectPeers(peers)

		if err := ConnectTopology(ctx, runenv, peers, h); err != nil {
			return err
		}

		initCtx.SyncClient.MustSignalAndWait(ctx, "connected", runenv.IntParam("providers_2"))

		runenv.RecordMessage("providers_2 connected to %d peers", len(h.Network().Peers()))

		for i := 0; i < txCount; i++ {
			select {
			case evt := <-isub.Out():
				runenv.RecordMessage("received tx (%d/%d) %s", i, txCount, evt.(IndexEvt).Root)
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	initCtx.SyncClient.MustSignalAndWait(ctx, "done", runenv.TestInstanceCount)

	return nil
}

func runDispatch(runenv *runtime.RunEnv, initCtx *run.InitContext) error {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	group := runenv.TestGroupID

	// Wait until all instances in this test run have signalled.
	initCtx.MustWaitAllInstancesInitialized(ctx)

	rpath, err := runenv.CreateRandomDirectory("", 0)
	if err != nil {
		return err
	}

	ip := initCtx.NetClient.MustGetDataNetworkIP()
	low := runenv.IntParam("min_conns")
	hiw := runenv.IntParam("max_conns")
	exch, h, err := newNode(ctx, rpath, ip, low, hiw)
	if err != nil {
		return err
	}

	runenv.RecordMessage("started exchange")

	info := host.InfoFromHost(h)

	// Publish our peer info to the network.
	initCtx.SyncClient.MustPublish(ctx, PeersTopic, info)

	peers, err := WaitForPeers(ctx, runenv, initCtx.SyncClient, h.ID(), runenv.TestInstanceCount)
	if err != nil {
		return err
	}

	peers = RandomTopology{Count: runenv.IntParam("bootstrap")}.SelectPeers(peers)

	if err := ConnectTopology(ctx, runenv, peers, h); err != nil {
		return err
	}

	initCtx.SyncClient.MustSignalAndWait(ctx, "connected", runenv.TestInstanceCount)

	runenv.RecordMessage("connected to %d peers", len(h.Network().Peers()))

	if group == "clients" {

		fpath, err := runenv.CreateRandomFile("", 256000)
		if err != nil {
			return err
		}

		tx := exch.Tx(ctx)
		err = tx.PutFile(fpath)
		if err != nil {
			return err
		}
		err = tx.Commit()
		if err != nil {
			return err
		}
		runenv.RecordMessage("dispatching to providers")

		tx.WatchDispatch(func(rec ex.PRecord) {
			runenv.RecordMessage("sent to peer %s", rec.Provider)
		})

	}

	_, err = initCtx.SyncClient.SignalAndWait(ctx, "completed", runenv.TestInstanceCount)
	if err != nil {
		return err
	}
	return nil
}

func newNode(ctx context.Context, rpath string, ip net.IP, low, hi int) (*ex.Exchange, host.Host, error) {
	ds := dssync.MutexWrap(datastore.NewMapDatastore())
	// create a new libp2p Host that listens on a random TCP port
	h, err := libp2p.New(ctx,
		libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/%s/tcp/0", ip)),
		// Control the maximum number of simultaneous connections a node can have
		libp2p.ConnectionManager(connmgr.NewConnManager(
			low,           // Lowwater
			hi,            // HighWater,
			1*time.Second, // GracePeriod
		)),
		libp2p.DisableRelay(),
		// All peer discovery happens via the dht and a single bootstrap peer
		libp2p.Routing(func(h host.Host) (routing.PeerRouting, error) {
			return dht.New(ctx, h)
		}),
	)
	if err != nil {
		return nil, h, err
	}

	opts := ex.Options{
		RepoPath:    rpath,
		Regions:     []ex.Region{ex.Regions["Global"]},
		RepInterval: 3 * time.Second, // accelerate replication for testing purposes
	}
	e, err := ex.New(ctx, h, ds, opts)
	if err != nil {
		return nil, nil, err
	}
	return e, h, nil
}
