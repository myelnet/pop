package node

import (
	"context"

	"github.com/ipfs/go-datastore"
	badgerds "github.com/ipfs/go-ds-badger"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/rs/zerolog/log"
)

// Options determines configurations for the IPFS node
type Options struct {
	// RepoPath is the file system path to use to persist our datastore
	RepoPath string
}

type node struct {
	host host.Host
	ds   datastore.Batching
}

// Run runs a hop enabled IPFS node
func Run(ctx context.Context, opts Options) error {
	nd := &node{}

	dsopts := badgerds.DefaultOptions
	dsopts.SyncWrites = false
	dsopts.Truncate = true

	ds, err := badgerds.NewDatastore(opts.RepoPath, &dsopts)
	if err != nil {
		return err
	}
	nd.ds = ds

	h, err := libp2p.New(ctx)
	if err != nil {
		return err
	}
	nd.host = h

	log.Info().Interface("addrs", nd.host.Addrs()).Msg("Node started libp2p")

	<-ctx.Done()

	return ctx.Err()
}
