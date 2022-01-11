package exchange

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"time"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	dtfimpl "github.com/filecoin-project/go-data-transfer/impl"
	dtnet "github.com/filecoin-project/go-data-transfer/network"
	gstransport "github.com/filecoin-project/go-data-transfer/transport/graphsync"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
	"github.com/ipfs/go-graphsync"
	gsimpl "github.com/ipfs/go-graphsync/impl"
	gsnet "github.com/ipfs/go-graphsync/network"
	"github.com/ipfs/go-graphsync/storeutil"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	keystore "github.com/ipfs/go-ipfs-keystore"
	"github.com/libp2p/go-libp2p-core/host"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/myelnet/go-multistore"
	"github.com/myelnet/pop/filecoin"
	"github.com/myelnet/pop/retrieval/deal"
	"github.com/myelnet/pop/wallet"
	"github.com/rs/zerolog/log"
)

// RequestTopic listens for peers looking for content blocks
const RequestTopic = "/myel/pop/request/"

// Options are optional modules for the exchange. We fill each field with a default
// instance when not provided
type Options struct {
	// Blockstore is used by default for graphsync and metadata storage
	// content should be stored on a multistore for proper isolation.
	Blockstore blockstore.Blockstore
	// MultiStore should be used to interface with content like importing files to store with the exchange
	// or exporting files to disk etc.
	MultiStore *multistore.MultiStore
	// PubSub allows passing a different pubsub instance with alternative routing algorithms. Default is Gossip.
	PubSub *pubsub.PubSub
	// GraphSync is used as Transport for DataTransfer, if you're providing a DataTransfer manager instance
	//  you don't need to set it.
	GraphSync graphsync.GraphExchange
	// DataTransfer is a single manager instance used across every retrieval operation.
	DataTransfer datatransfer.Manager
	// Wallet is a minimal interface for signing things
	Wallet wallet.Driver
	// RepoPath is where to persist any file to disk. It's actually only used for the DataTransfer CID list
	// recommend passing the same path as the datastore.
	RepoPath string
	// FilecoinRPCEndpoint is the websocket url to connect to a remote Lotus node.
	FilecoinRPCEndpoint string
	// FilecoinRPCHeader provides any required header depending on the Lotus server policy.
	FilecoinRPCHeader http.Header
	// FilecoinAPI can be passed directly instead of providing an endpoint. This can be useful in case you are.
	// in an environment which already may have the API instance.
	FilecoinAPI filecoin.API
	// GossipTracer is provided if you are using an external PubSub instance.
	GossipTracer *GossipTracer
	// Regions is the geographic region this exchange should serve. Defaults to Global only.
	Regions []Region
	// Capacity is the maximum storage capacity in bytes this exchange can handle. Once we capacity is reached,
	// least frequently used content is evicted to make more room for new content.
	// Default is 10GB.
	Capacity uint64
	// ReplInterval is the replication interval after which a worker will try to retrieve fresh new content
	// on the network
	ReplInterval time.Duration
	// WatchQueriesFunc is an optional function that will return any queries received as a provider
	WatchQueriesFunc func(deal.Query)
	// WatchEvictionFunc is an optional function that will yield the root cid for any evicted content.
	WatchEvictionFunc func(DataRef)
	// WatchAdditionFunc is an optional callback notifying when content is added to the index.
	WatchAdditionFunc func(DataRef)
	// PPB is the price per byte the exchange sets when offering a deal.
	PPB big.Int
}

// Everything isn't thoroughly validated so we trust users who provide options know what they're doing
func (opts Options) fillDefaults(ctx context.Context, h host.Host, ds datastore.Batching) (Options, error) {
	var err error
	if opts.Blockstore == nil {
		opts.Blockstore = blockstore.NewGCBlockstore(blockstore.NewBlockstore(ds), blockstore.NewGCLocker())
	} else if _, ok := opts.Blockstore.(blockstore.GCBlockstore); !ok {
		opts.Blockstore = blockstore.NewGCBlockstore(opts.Blockstore, blockstore.NewGCLocker())
	}
	if opts.MultiStore == nil {
		opts.MultiStore, err = multistore.NewMultiDstore(ds)
		if err != nil {
			return opts, err
		}
	}
	if opts.PubSub == nil {
		opts.GossipTracer = NewGossipTracer()
		opts.PubSub, err = pubsub.NewGossipSub(ctx, h, pubsub.WithEventTracer(opts.GossipTracer))
		if err != nil {
			return opts, err
		}
	}
	if opts.GraphSync == nil {
		opts.GraphSync = gsimpl.New(ctx,
			gsnet.NewFromLibp2pHost(h),
			storeutil.LinkSystemForBlockstore(opts.Blockstore),
		)
	}
	if opts.DataTransfer == nil {
		opts.DataTransfer, err = NewDataTransfer(ctx, h, opts.GraphSync, ds, "pop/retrieval", opts.RepoPath)
		if err != nil {
			return opts, err
		}
	}
	if opts.Regions == nil {
		opts.Regions = []Region{global}
	}
	if opts.FilecoinRPCEndpoint != "" && opts.FilecoinAPI == nil {
		opts.FilecoinAPI, err = filecoin.NewLotusRPC(ctx, opts.FilecoinRPCEndpoint, opts.FilecoinRPCHeader)
		if err != nil {
			// We don't fail the initialization and continue without it
			log.Error().Err(err).Msg("failed to connect with lotus RPC")
			opts.FilecoinAPI = nil
		}
	}

	// Default wallet only supports secp256k1
	if opts.Wallet == nil {
		ks, err := keystore.NewFSKeystore(filepath.Join(opts.RepoPath, "keystore"))
		if err != nil {
			return opts, err
		}
		opts.Wallet = wallet.NewFromKeystore(ks, wallet.WithFilAPI(opts.FilecoinAPI))
	}

	if opts.Capacity == 0 {
		// Default is 10GB
		opts.Capacity = 10737418240
	}

	if opts.PPB.Nil() {
		// Default is 0 PPB
		opts.PPB = big.NewInt(0)
	}


	return opts, nil
}

// NewDataTransfer packages together all the things needed for a new manager to work
func NewDataTransfer(ctx context.Context, h host.Host, gs graphsync.GraphExchange, ds datastore.Batching, dsprefix string, dir string) (datatransfer.Manager, error) {
	cidDir, err := mkCidListDir(dir)
	if err != nil {
		return nil, err
	}
	// Create a special key for persisting the datatransfer manager state
	dtDs := namespace.Wrap(ds, datastore.NewKey(dsprefix+"-datatransfer"))
	// Setup datatransfer network
	dtNet := dtnet.NewFromLibp2pHost(h)
	// Setup graphsync transport
	tp := gstransport.NewTransport(h.ID(), gs, dtNet)
	// Build the manager
	dt, err := dtfimpl.NewDataTransfer(dtDs, cidDir, dtNet, tp)
	if err != nil {
		return nil, err
	}
	ready := make(chan error, 1)
	dt.OnReady(func(err error) {
		ready <- err
	})
	dt.Start(ctx)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-ready:
		return dt, err
	}
}

// Make a special directory for the data transfer manager
func mkCidListDir(rpath string) (string, error) {
	p := filepath.Join(rpath, "data-transfer")
	err := os.MkdirAll(p, 0755)
	if err != nil && !os.IsExist(err) {
		return "", err
	}
	return p, nil
}
