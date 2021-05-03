package main

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"
	"github.com/libp2p/go-libp2p"
	connmgr "github.com/libp2p/go-libp2p-connmgr"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/routing"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	ex "github.com/myelnet/pop/exchange"
	"github.com/testground/sdk-go/run"
	"github.com/testground/sdk-go/runtime"
)

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

	peers, err := WaitForPeers(ctx, runenv, initCtx.SyncClient, h.ID())
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
		RepoPath: rpath,
		Regions:  []ex.Region{ex.Regions["Global"]},
	}
	e, err := ex.New(ctx, h, ds, opts)
	if err != nil {
		return nil, nil, err
	}
	return e, h, nil
}
