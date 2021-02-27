package hop

import (
	"context"
	"net/http"
	"os"
	"path/filepath"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	dtfimpl "github.com/filecoin-project/go-data-transfer/impl"
	dtnet "github.com/filecoin-project/go-data-transfer/network"
	gstransport "github.com/filecoin-project/go-data-transfer/transport/graphsync"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-storedcounter"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
	"github.com/ipfs/go-graphsync"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	"github.com/libp2p/go-libp2p-core/host"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/myelnet/go-hop-exchange/wallet"
)

// TODO: We should be able to customize these in the options

// DefaultPricePerByte is the charge per byte retrieved if the miner does
// not specifically set it
var DefaultPricePerByte = abi.NewTokenAmount(2)

// DefaultPaymentInterval is the baseline interval, set to 1Mb
// if the miner does not explicitly set it otherwise
var DefaultPaymentInterval = uint64(1 << 20)

// DefaultPaymentIntervalIncrease is the amount interval increases on each payment,
// set to to 1Mb if the miner does not explicitly set it otherwise
var DefaultPaymentIntervalIncrease = uint64(1 << 20)

// Settings are all the elements required to power the exchange
type Settings struct {
	Datastore           datastore.Batching
	Blockstore          blockstore.Blockstore
	Host                host.Host
	PubSub              *pubsub.PubSub
	GraphSync           graphsync.GraphExchange
	Keystore            wallet.Keystore
	RepoPath            string
	FilecoinRPCEndpoint string
	FilecoinRPCHeader   http.Header
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
	tp := gstransport.NewTransport(h.ID(), gs)
	// Make a special key for stored counter
	key := datastore.NewKey(dsprefix + "-counter")
	// persist ids for new transfers
	storedCounter := storedcounter.New(ds, key)
	// Build the manager
	dt, err := dtfimpl.NewDataTransfer(dtDs, cidDir, dtNet, tp, storedCounter)
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
