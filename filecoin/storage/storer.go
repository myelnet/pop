package storage

import (
	"context"
	"time"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	discoveryimpl "github.com/filecoin-project/go-fil-markets/discovery/impl"
	"github.com/filecoin-project/go-fil-markets/storagemarket"
	storageimpl "github.com/filecoin-project/go-fil-markets/storagemarket/impl"
	smnet "github.com/filecoin-project/go-fil-markets/storagemarket/network"
	"github.com/filecoin-project/go-multistore"
	"github.com/ipfs/go-datastore"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	"github.com/libp2p/go-libp2p-core/host"
	fil "github.com/myelnet/go-hop-exchange/filecoin"
	"github.com/myelnet/go-hop-exchange/wallet"
)

// Storage is a minimal system for creating basic storage deals on Filecoin
type Storage struct {
	client  storagemarket.StorageClient
	adapter *Adapter
	fundmgr *FundManager
}

// New creates a new storage client instance
func New(
	h host.Host,
	bs blockstore.Blockstore,
	ms *multistore.MultiStore,
	ds datastore.Batching,
	dt datatransfer.Manager,
	w wallet.Driver,
	api fil.API,
) (*Storage, error) {
	fundmgr := NewFundManager(ds, api, w)
	ad := &Adapter{
		fAPI:    api,
		wallet:  w,
		fundmgr: fundmgr,
	}

	marketsRetryParams := smnet.RetryParameters(time.Second, 5*time.Minute, 15, 5)
	net := smnet.NewFromLibp2pHost(h, marketsRetryParams)

	disc, err := discoveryimpl.NewLocal(ds)
	if err != nil {
		return nil, err
	}

	c, err := storageimpl.NewClient(net, bs, ms, ds, disc, ds, ad, storageimpl.DealPollingInterval(time.Second))
	if err != nil {
		return nil, err
	}

	return &Storage{
		client:  c,
		adapter: ad,
		fundmgr: fundmgr,
	}, nil
}

// Start is required to launch the fund manager and storage client before making new deals
func (s *Storage) Start(ctx context.Context) error {
	err := s.fundmgr.Start()
	if err != nil {
		return err
	}
	return s.client.Start(ctx)
}

// Client exposes the storage client API
func (s *Storage) Client() storagemarket.StorageClient {
	return s.client
}
