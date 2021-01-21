package hop

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-graphsync"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	pin "github.com/ipfs/go-ipfs-pinner"
	"github.com/ipfs/go-ipfs/keystore"
	"github.com/libp2p/go-libp2p-core/host"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/myelnet/go-hop-exchange/filecoin"
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

// WithKeystore sets an IPFS keystore as the underlying key storage solution
func WithKeystore(ks keystore.Keystore) func(*Exchange) error {
	return func(ex *Exchange) error {
		ex.Keystore = ks
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

// WithPinner brings a custom Pinner interface
func WithPinner(pinner pin.Pinner) func(*Exchange) error {
	return func(ex *Exchange) error {
		ex.Pinner = pinner
		return nil
	}
}

// WithFilecoinAPI sets the api endpoint and auth token for a remote filecoin node
func WithFilecoinAPI(addr string, header http.Header) func(*Exchange) error {
	return func(ex *Exchange) error {
		ex.fEndpoint = filecoin.APIEndpoint{
			Address: addr,
			Header:  header,
		}
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
