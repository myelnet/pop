package storage

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/filecoin-project/go-address"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	discoveryimpl "github.com/filecoin-project/go-fil-markets/discovery/impl"
	"github.com/filecoin-project/go-fil-markets/storagemarket"
	storageimpl "github.com/filecoin-project/go-fil-markets/storagemarket/impl"
	smnet "github.com/filecoin-project/go-fil-markets/storagemarket/network"
	"github.com/filecoin-project/go-multistore"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/go-state-types/dline"
	"github.com/filecoin-project/specs-actors/v3/actors/builtin"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/myelnet/pop/filecoin"
	fil "github.com/myelnet/pop/filecoin"
	"github.com/myelnet/pop/wallet"
)

const dealStartBufferHours uint64 = 49

// BlockDelaySecs is the time elapsed between each block
const BlockDelaySecs = uint64(builtin.EpochDurationSeconds)

// StoreIDGetter allows the storage module to find the store ID associated with content we want to store
type StoreIDGetter interface {
	GetStoreID(cid.Cid) (multistore.StoreID, error)
}

// MinerDetails represents a miner from the json encoded result
type MinerDetails struct {
	Address string `json:"address"`
	Region  string `json:"region"`
}

// MinerPagination is data about Filrep API pagination
type MinerPagination struct {
	Total  int `json:"total"`
	Offset int `json:"offset"`
	Limit  int `json:"limit"`
}

// MinersResult is a list of miners returned as the result of the miners query
type MinersResult struct {
	Miners     []MinerDetails  `json:"miners"`
	Pagination MinerPagination `json:"pagination,omitempty"`
}

// MinerParams are used to filter the miners returned in the FindMiners query
type MinerParams struct {
	Limit  int
	Offset int
	Region string
}

// URLEncode returns the params as url encoded params
func (mp MinerParams) URLEncode() string {
	params := url.Values{}

	limit := defaultLimit
	if mp.Limit > 0 {
		limit = mp.Limit
	}

	params.Add("limit", fmt.Sprintf("%d", limit))

	if mp.Region != "" {
		params.Add("region", mp.Region)
	}
	if mp.Offset > 0 {
		params.Add("offset", fmt.Sprintf("%d", mp.Offset))
	}
	return params.Encode()
}

// MinerFinder allows the storage module to get a list of Filecoin miners to store with
type MinerFinder interface {
	FindMiners(context.Context, MinerParams) (MinersResult, error)
}

// Storage is a minimal system for creating basic storage deals on Filecoin
type Storage struct {
	host    host.Host
	client  storagemarket.StorageClient
	adapter *Adapter
	fundmgr *FundManager
	fAPI    fil.API
	disc    *discoveryimpl.Local
	sg      StoreIDGetter
	mf      MinerFinder
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
	sg StoreIDGetter,
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

	c, err := storageimpl.NewClient(net, bs, ms, dt, disc, ds, ad, storageimpl.DealPollingInterval(time.Second))
	if err != nil {
		return nil, err
	}

	return &Storage{
		host:    h,
		client:  c,
		adapter: ad,
		fundmgr: fundmgr,
		sg:      sg,
		mf:      NewFilRep(),
		fAPI:    api,
		disc:    disc,
	}, nil
}

// Start is required to launch the fund manager and storage client before making new deals
func (s *Storage) Start(ctx context.Context) error {
	// start discovery ds migrations
	if err := s.disc.Start(ctx); err != nil {
		return err
	}

	err := s.fundmgr.Start()
	if err != nil {
		return err
	}
	return s.client.Start(ctx)
}

// PeerInfo resolves a Filecoin address to find the peer info and add to our address book
func (s *Storage) PeerInfo(ctx context.Context, addr address.Address) (*peer.AddrInfo, error) {
	miner, err := s.fAPI.StateMinerInfo(ctx, addr, filecoin.EmptyTSK)
	if err != nil {
		return nil, err
	}
	multiaddrs := make([]ma.Multiaddr, 0, len(miner.Multiaddrs))
	for _, a := range miner.Multiaddrs {
		maddr, err := ma.NewMultiaddrBytes(a)
		if err != nil {
			return nil, err
		}
		multiaddrs = append(multiaddrs, maddr)
	}
	if miner.PeerId == nil {
		return nil, fmt.Errorf("no peer id available")
	}
	if len(miner.Multiaddrs) == 0 {
		return nil, fmt.Errorf("no peer address available")
	}
	pi := peer.AddrInfo{
		ID:    *miner.PeerId,
		Addrs: multiaddrs,
	}
	return &pi, nil
}

// Miner encapsulates some information about a storage miner
type Miner struct {
	Ask                 *storagemarket.StorageAsk
	Info                *storagemarket.StorageProviderInfo
	WindowPoStProofType abi.RegisteredPoStProof
}

// MinerSelectionParams defines the criterias for selecting a list of miners
type MinerSelectionParams struct {
	MaxPrice uint64
	RF       int
	Region   string
}

// LoadMiners selects a set of miners to queue storage deals with
func (s *Storage) LoadMiners(ctx context.Context, msp MinerSelectionParams) ([]Miner, error) {
	var sel []Miner

	limit := msp.RF
	offset := 0

	// Iterate across miner pages
	for len(sel) < msp.RF {
		result, err := s.mf.FindMiners(ctx, MinerParams{
			Limit:  limit,
			Region: msp.Region,
			Offset: offset,
		})
		if err != nil {
			return nil, err
		}

		for _, m := range result.Miners {
			a, err := address.NewFromString(m.Address)
			if err != nil {
				continue
			}

			mi, err := s.fAPI.StateMinerInfo(ctx, a, fil.EmptyTSK)
			if err != nil {
				continue
			}
			// PeerId is often nil which causes panics down the road
			if mi.PeerId == nil {
				continue
			}
			info := NewStorageProviderInfo(a, mi.Worker, mi.SectorSize, *mi.PeerId, mi.Multiaddrs)

			ai := peer.AddrInfo{
				ID:    info.PeerID,
				Addrs: info.Addrs,
			}
			// We need to connect directly with the peer to ping them
			err = s.host.Connect(ctx, ai)
			if err != nil {
				continue
			}

			ask, err := s.client.GetAsk(ctx, info)
			if err != nil {
				continue
			}

			// Any miner requesting more than our price ceiling is ignored
			if fil.NewInt(msp.MaxPrice).LessThan(ask.Price) {
				continue
			}

			sel = append(sel, Miner{
				Ask:                 ask,
				Info:                &info,
				WindowPoStProofType: mi.WindowPoStProofType,
			})
			if len(sel) == msp.RF {
				return sel, nil
			}
		}
		// total - (offset + result)
		// This is all the results we can find
		if result.Pagination.Total-(result.Pagination.Offset+result.Pagination.Limit) == 0 {
			return sel, nil
		}
		offset += limit
	}
	return sel, nil
}

// StartDealParams are params configurable on the user side
type StartDealParams struct {
	Data               *storagemarket.DataRef
	Wallet             address.Address
	Miner              Miner
	EpochPrice         fil.BigInt
	MinBlocksDuration  uint64
	ProviderCollateral big.Int
	DealStartEpoch     abi.ChainEpoch
	FastRetrieval      bool
	VerifiedDeal       bool
}

// StartDeal starts a new storage deal with a Filecoin storage miner
func (s *Storage) StartDeal(ctx context.Context, params StartDealParams) (*cid.Cid, error) {
	storeID, err := s.sg.GetStoreID(params.Data.Root)
	if err != nil {
		return nil, err
	}
	md, err := s.fAPI.StateMinerProvingDeadline(ctx, params.Miner.Info.Address, fil.EmptyTSK)
	if err != nil {
		return nil, fmt.Errorf("failed getting miner's deadline info: %w", err)
	}

	dealStart := params.DealStartEpoch
	if dealStart <= 0 { // unset, or explicitly 'epoch undefine'
		ts, err := s.fAPI.ChainHead(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed getting chain height: %w", err)
		}

		blocksPerHour := 60 * 60 / BlockDelaySecs
		dealStart = ts.Height() + abi.ChainEpoch(dealStartBufferHours*blocksPerHour) // TODO: Get this from storage ask
	}

	st, err := PreferredSealProofTypeFromWindowPoStType(params.Miner.WindowPoStProofType)
	if err != nil {
		return nil, fmt.Errorf("failed to get seal proof type: %w", err)
	}

	fmt.Println("ProposeStorageDeal")
	result, err := s.client.ProposeStorageDeal(ctx, storagemarket.ProposeStorageDealParams{
		Addr:          params.Wallet,
		Info:          params.Miner.Info,
		Data:          params.Data,
		StartEpoch:    dealStart,
		EndEpoch:      calcDealExpiration(params.MinBlocksDuration, md, dealStart),
		Price:         params.EpochPrice,
		Collateral:    params.ProviderCollateral,
		Rt:            st,
		FastRetrieval: params.FastRetrieval,
		VerifiedDeal:  params.VerifiedDeal,
		StoreID:       &storeID,
	})

	if err != nil {
		return nil, fmt.Errorf("failed to start deal: %w", err)
	}

	return &result.ProposalCid, nil
}

// QuoteParams is the params to calculate the storage quote with.
type QuoteParams struct {
	PieceSize uint64
	Duration  time.Duration
	RF        int
	MaxPrice  uint64
	Region    string
	Verified  bool
}

// Quote is an estimate of who can store given content and for how much
// It also returns the minimum size of the piece all those miners can store
type Quote struct {
	Miners       []Miner
	Prices       map[address.Address]fil.FIL
	MinPieceSize uint64
}

// GetMarketQuote returns the costs of storing for a given CID and duration
func (s *Storage) GetMarketQuote(ctx context.Context, params QuoteParams) (*Quote, error) {
	miners, err := s.LoadMiners(ctx, MinerSelectionParams{
		RF:       params.RF,
		MaxPrice: params.MaxPrice,
		Region:   params.Region,
	})
	if err != nil {
		return nil, err
	}
	if len(miners) == 0 {
		return nil, errors.New("no miners fit those parameters")
	}

	gib := fil.NewInt(1 << 30)

	epochs := calcEpochs(params.Duration)

	prices := make(map[address.Address]fil.FIL)

	// The highest min piece size
	var minPieceSize uint64
	for _, m := range miners {
		p := m.Ask.Price
		if params.Verified {
			p = m.Ask.VerifiedPrice
		}
		if uint64(m.Ask.MinPieceSize) > minPieceSize {
			minPieceSize = uint64(m.Ask.MinPieceSize)
		}
		epochPrice := fil.BigDiv(fil.BigMul(p, fil.NewInt(params.PieceSize)), gib)
		prices[m.Info.Address] = fil.FIL(fil.BigMul(epochPrice, fil.NewInt(uint64(epochs))))
	}

	return &Quote{
		Miners:       miners,
		Prices:       prices,
		MinPieceSize: minPieceSize,
	}, nil
}

// Params are the global parameters for storing on Filecoin with given replication
type Params struct {
	Payload  *storagemarket.DataRef
	Duration time.Duration
	Address  address.Address
	Miners   []Miner
	Verified bool
}

// NewParams creates a new Params struct for storage
func NewParams(root cid.Cid, dur time.Duration, w address.Address, mnrs []Miner, verified bool) Params {
	return Params{
		Payload: &storagemarket.DataRef{
			TransferType: storagemarket.TTGraphsync,
			Root:         root,
		},
		Duration: dur,
		Address:  w,
		Miners:   mnrs,
		Verified: verified,
	}
}

// Receipt compiles all information about our content storage contracts
type Receipt struct {
	Miners   []address.Address
	DealRefs []cid.Cid
}

// Store is the main storage operation which automatically stores content for a given CID
// with the best conditions available
func (s *Storage) Store(ctx context.Context, p Params) (*Receipt, error) {
	var ma []address.Address
	for _, m := range p.Miners {
		ma = append(ma, m.Info.Address)
	}
	epochs := calcEpochs(p.Duration)
	var drfs []cid.Cid
	for _, m := range p.Miners {
		pcid, err := s.StartDeal(ctx, StartDealParams{
			Data:              p.Payload,
			Wallet:            p.Address,
			Miner:             m,
			EpochPrice:        m.Ask.Price,
			MinBlocksDuration: uint64(epochs),
			DealStartEpoch:    -1,
			FastRetrieval:     false,
			VerifiedDeal:      p.Verified,
		})
		if err != nil {
			return nil, err
		}
		if pcid != nil {
			drfs = append(drfs, *pcid)
		}
	}

	return &Receipt{
		Miners:   ma,
		DealRefs: drfs,
	}, nil
}

func PreferredSealProofTypeFromWindowPoStType(proof abi.RegisteredPoStProof) (abi.RegisteredSealProof, error) {
	switch proof {
	case abi.RegisteredPoStProof_StackedDrgWindow2KiBV1:
		return abi.RegisteredSealProof_StackedDrg2KiBV1_1, nil
	case abi.RegisteredPoStProof_StackedDrgWindow8MiBV1:
		return abi.RegisteredSealProof_StackedDrg8MiBV1_1, nil
	case abi.RegisteredPoStProof_StackedDrgWindow512MiBV1:
		return abi.RegisteredSealProof_StackedDrg512MiBV1_1, nil
	case abi.RegisteredPoStProof_StackedDrgWindow32GiBV1:
		return abi.RegisteredSealProof_StackedDrg32GiBV1_1, nil
	case abi.RegisteredPoStProof_StackedDrgWindow64GiBV1:
		return abi.RegisteredSealProof_StackedDrg64GiBV1_1, nil
	default:
		return -1, fmt.Errorf("unrecognized window post type: %d", proof)
	}
}

func calcDealExpiration(minDuration uint64, md *dline.Info, startEpoch abi.ChainEpoch) abi.ChainEpoch {
	// Make sure we give some time for the miner to seal
	minExp := startEpoch + abi.ChainEpoch(minDuration)

	// Align on miners ProvingPeriodBoundary
	return minExp + md.WPoStProvingPeriod - (minExp % md.WPoStProvingPeriod) + (md.PeriodStart % md.WPoStProvingPeriod) - 1
}

func calcEpochs(t time.Duration) abi.ChainEpoch {
	return abi.ChainEpoch(t / (time.Duration(uint64(builtin.EpochDurationSeconds)) * time.Second))
}
