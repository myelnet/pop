package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/filecoin-project/go-multistore"
	badgerds "github.com/ipfs/go-ds-badger"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	keystore "github.com/ipfs/go-ipfs-keystore"
	"github.com/libp2p/go-libp2p"
	connmgr "github.com/libp2p/go-libp2p-connmgr"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/routing"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/p2p/net/conngater"
	"github.com/myelnet/pop/exchange"
	"github.com/myelnet/pop/filecoin"
	"github.com/myelnet/pop/internal/utils"
	"github.com/myelnet/pop/wallet"
	"github.com/rs/zerolog/log"
)

// pop provider is a lightweight daemon only focused on serving content for retrievals
// settings are only provided via startup flags and it is optimized to run on every platform
// with lowest memory footprint.
// All behavior should be automated so it doesn't require any control system appart from upgrade and restart.

// TODO: move this in shared package

var args struct {
	tempRepo       bool
	bootstrapPeers []string
	filEndpoint    string
	filToken       string
	filTokenType   string
	regions        []string
}

func main() {
	flag.BoolVar(&args.tempRepo, "temp-repo", false, "create a temporary repo instead of regular path")
	flag.Var(utils.ListValue(&args.bootstrapPeers, []string{
		"/ip4/3.14.73.230/tcp/4001/ipfs/12D3KooWQtnktGLsDc3fgHW4vrsCVR15oC1Vn6Wy6Moi65pL6q2a",
	}),
		"boostrap", "peers to connect to in order to discover other peers")
	flag.StringVar(&args.filEndpoint, "fil-endpoint", os.Getenv("FIL_ENDPOINT"), "filecoin blockchain node endpoint")
	flag.StringVar(&args.filToken, "fil-token", os.Getenv("FIL_TOKEN"), "filecoin blockchain node auth token")
	flag.StringVar(&args.filTokenType, "fil-token-type", "Bearer", "filecoin blockchain authorization type")
	flag.Var(utils.ListValue(&args.regions, []string{"Global"}), "regions", "provider regions for finding relevant content")

	flag.Parse()

	if err := run(); err != nil {
		fmt.Printf("shutting down for reason: %v", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var err error

	rpath, err := setupRepo()
	if err != nil {
		return err
	}

	filToken := utils.FormatToken(args.filToken, args.filTokenType)

	dsopts := badgerds.DefaultOptions
	dsopts.SyncWrites = false
	dsopts.Truncate = true

	ds, err := badgerds.NewDatastore(filepath.Join(rpath, "datastore"), &dsopts)
	if err != nil {
		return err
	}

	bs := blockstore.NewBlockstore(ds)

	ms, err := multistore.NewMultiDstore(ds)
	if err != nil {
		return err
	}

	ks, err := keystore.NewFSKeystore(filepath.Join(rpath, "keystore"))
	if err != nil {
		return err
	}

	priv, err := utils.Libp2pKey(ks)
	if err != nil {
		return err
	}

	gater, err := conngater.NewBasicConnectionGater(ds)
	if err != nil {
		return err
	}

	host, err := libp2p.New(
		ctx,
		libp2p.Identity(priv),
		libp2p.ConnectionManager(connmgr.NewConnManager(
			50,             // Lowwater
			100,            // HighWater,
			20*time.Second, // GracePeriod
		)),
		libp2p.ConnectionGater(gater),
		libp2p.DisableRelay(),
		// Attempt to open ports using uPNP for NATed hosts.
		libp2p.NATPortMap(),
		libp2p.EnableNATService(),
		// Let this host use the DHT to find other hosts
		libp2p.Routing(func(h host.Host) (routing.PeerRouting, error) {
			return dht.New(ctx, h)
		}),
	)
	if err != nil {
		return err
	}

	// Convert region names to region structs
	regions := exchange.ParseRegions(args.regions)

	opts := exchange.Options{
		Blockstore:          bs,
		MultiStore:          ms,
		RepoPath:            rpath,
		FilecoinRPCEndpoint: args.filEndpoint,
		FilecoinRPCHeader: http.Header{
			"Authorization": []string{filToken},
		},
		Regions: regions,
	}

	opts.FilecoinAPI, err = filecoin.NewLotusRPC(ctx, opts.FilecoinRPCEndpoint, opts.FilecoinRPCHeader)
	if err != nil {
		log.Error().Err(err).Msg("failed to connect with Lotus RPC")
	}
	opts.Wallet = wallet.NewFromKeystore(
		ks,
		wallet.WithFilAPI(opts.FilecoinAPI),
	)

	exch, err := exchange.New(ctx, host, ds, opts)
	if err != nil {
		return err
	}
	fmt.Printf("==> Started pop exchange\n")

	// remove unwanted blocks that might be in the blockstore but are removed from the index
	err = exch.Index().CleanBlockStore(ctx)
	if err != nil {
		return err
	}

	go utils.Bootstrap(ctx, host, args.bootstrapPeers)

	fmt.Printf("==> Joined %s regions\n", args.regions)
	if exch.IsFilecoinOnline() {
		fmt.Printf("==> Connected to Filecoin RPC at %s\n", args.filEndpoint)
	}

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-interrupt:
	case <-ctx.Done():
	}

	return nil
}

// setupRepo creates a new repo if none, or simply returns the path
func setupRepo() (string, error) {
	path, err := utils.FullPath(utils.RepoPath())
	if err != nil {
		return path, err
	}
	exists, err := utils.RepoExists(path)
	if err != nil {
		return path, err
	}
	if args.tempRepo {
		path, err = os.MkdirTemp("", ".pop")
		if err != nil {
			return path, err
		}
		fmt.Printf("==> Created temporary repo\n")
		return path, nil
	}

	if exists {
		return path, nil
	}

	// Make our root repo dir and datastore dir
	err = os.MkdirAll(filepath.Join(path, "datastore"), 0755)
	if err != nil {
		return path, err
	}
	fmt.Printf("==> Initialized pop repo in %s\n", path)

	return path, nil
}
