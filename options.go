package hop

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	dtfimpl "github.com/filecoin-project/go-data-transfer/impl"
	dtnet "github.com/filecoin-project/go-data-transfer/network"
	gstransport "github.com/filecoin-project/go-data-transfer/transport/graphsync"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-storedcounter"
	cid "github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
	"github.com/ipfs/go-graphsync"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	keystore "github.com/ipfs/go-ipfs-keystore"
	"github.com/ipld/go-ipld-prime"
	dagpb "github.com/ipld/go-ipld-prime-proto"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	basicnode "github.com/ipld/go-ipld-prime/node/basic"
	"github.com/ipld/go-ipld-prime/traversal"
	"github.com/ipld/go-ipld-prime/traversal/selector"
	"github.com/libp2p/go-libp2p-core/host"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/myelnet/go-hop-exchange/supply"
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
	Keystore            keystore.Keystore
	RepoPath            string
	FilecoinRPCEndpoint string
	FilecoinRPCHeader   http.Header
	// Probably temporary as we want Regions to be more dynamic eventually
	Regions []supply.Region
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

// DAGStatResult compiles stats about a DAG
type DAGStatResult struct {
	Size      int
	NumBlocks int
}

// DAGStat returns stats about a selected part of a DAG given a cid and blockstore
func DAGStat(ctx context.Context, bs blockstore.Blockstore, root cid.Cid, sel ipld.Node) (*DAGStatResult, error) {
	res := &DAGStatResult{}
	link := cidlink.Link{Cid: root}
	chooser := dagpb.AddDagPBSupportToChooser(func(ipld.Link, ipld.LinkContext) (ipld.NodePrototype, error) {
		return basicnode.Prototype.Any, nil
	})
	// The root node could be a raw node so we need to select the builder accordingly
	nodeType, err := chooser(link, ipld.LinkContext{})
	if err != nil {
		return res, err
	}
	builder := nodeType.NewBuilder()
	// We make a custom loader to intercept when each block is read during the traversal
	makeLoader := func(bs blockstore.Blockstore) ipld.Loader {
		return func(lnk ipld.Link, lnkCtx ipld.LinkContext) (io.Reader, error) {
			c, ok := lnk.(cidlink.Link)
			if !ok {
				return nil, fmt.Errorf("incorrect Link Type")
			}
			block, err := bs.Get(c.Cid)
			if err != nil {
				return nil, err
			}
			reader := bytes.NewReader(block.RawData())
			res.Size += reader.Len()
			res.NumBlocks++
			return reader, nil
		}
	}
	// Load the root node
	err = link.Load(ctx, ipld.LinkContext{}, builder, makeLoader(bs))
	if err != nil {
		return res, fmt.Errorf("unable to load link: %v", err)
	}
	nd := builder.Build()

	s, err := selector.ParseSelector(sel)
	if err != nil {
		return res, err
	}
	// Traverse any links from the root node
	err = traversal.Progress{
		Cfg: &traversal.Config{
			LinkLoader:                     makeLoader(bs),
			LinkTargetNodePrototypeChooser: chooser,
		},
	}.WalkMatching(nd, s, func(prog traversal.Progress, n ipld.Node) error {
		return nil
	})

	return res, nil
}
