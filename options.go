package hop

import (
	"os"
	"path/filepath"

	"github.com/filecoin-project/go-address"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-graphsync"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	"github.com/libp2p/go-libp2p-core/host"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
)

// WithBlockstore sets the Exchange blockstore
func WithBlockstore(b blockstore.Blockstore) func(*Exchange) error {
	return func(e *Exchange) error {
		e.Blockstore = b
		return nil
	}
}

// WithDatastore sets the underlying datastore
func WithDatastore(ds datastore.Batching) func(*Exchange) error {
	return func(ex *Exchange) error {
		ex.Datastore = ds
		return nil
	}
}

// WithHost sets the Exchange host
func WithHost(h host.Host) func(*Exchange) error {
	return func(ex *Exchange) error {
		ex.Host = h
		return nil
	}
}

// WithPubSub sets the pubsub interface
func WithPubSub(ps *pubsub.PubSub) func(*Exchange) error {
	return func(ex *Exchange) error {
		ex.PubSub = ps
		return nil
	}
}

// WithFILAddress sets the Filecoin address of the host
func WithFILAddress(a address.Address) func(*Exchange) error {
	return func(ex *Exchange) error {
		ex.SelfAddress = a
		return nil
	}
}

// WithRepoPath provides us with the path where to store our list of datatransfer cids
func WithRepoPath(rpath string) func(*Exchange) error {
	return func(ex *Exchange) error {
		p := filepath.Join(rpath, "data-transfer")
		err := os.MkdirAll(p, 0755)
		if err != nil && !os.IsExist(err) {
			return err
		}

		ex.cidListDir = p
		return nil
	}
}

// WithGraphSync brings the graphsync instance
func WithGraphSync(gs graphsync.GraphExchange) func(*Exchange) error {
	return func(ex *Exchange) error {
		ex.GraphSync = gs
		return nil
	}
}

// WithDefaults basically creates a lightweight IPFS node to enable a lightweight exchange
// TODO:
func WithDefaults() func(*Exchange) error {
	return func(ex *Exchange) error {
		return nil
	}
}
