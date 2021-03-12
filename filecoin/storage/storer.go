package storage

import (
	"context"
	"errors"
	"fmt"
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
	fil "github.com/myelnet/go-hop-exchange/filecoin"
	"github.com/myelnet/go-hop-exchange/wallet"
)

const dealStartBufferHours uint64 = 49

// BlockDelaySecs is the time elapsed between each block
const BlockDelaySecs = uint64(builtin.EpochDurationSeconds)

// StoreIDGetter allows the storage module to find the store ID associated with content we want to store
type StoreIDGetter interface {
	GetStoreID(cid.Cid) (multistore.StoreID, error)
}

// MinerLister allows the storage module to get a list of Filecoin miners to store with
type MinerLister interface {
	ListMiners(ctx context.Context) ([]address.Address, error)
}

// Supplier is a generic interface for supplying the storage module with dynamic information about content
// and network agents
type Supplier interface {
	StoreIDGetter
	MinerLister
}

// Storage is a minimal system for creating basic storage deals on Filecoin
type Storage struct {
	client  storagemarket.StorageClient
	adapter *Adapter
	fundmgr *FundManager
	fAPI    fil.API
	sp      Supplier
	disc    *discoveryimpl.Local
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
	sp Supplier,
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
		client:  c,
		adapter: ad,
		fundmgr: fundmgr,
		sp:      sp,
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

// Miner encapsulates some information about a storage miner
type Miner struct {
	Ask                 *storagemarket.StorageAsk
	Info                *storagemarket.StorageProviderInfo
	WindowPoStProofType abi.RegisteredPoStProof
}

// MinerSelectionParams defines the criterias for selecting a list of miners
type MinerSelectionParams struct {
	PieceSize uint64
	RF        int
}

// LoadMiners selects a set of miners to queue storage deals with
func (s *Storage) LoadMiners(ctx context.Context, msp MinerSelectionParams) ([]Miner, error) {
	addrs, err := s.sp.ListMiners(ctx)
	if err != nil {
		return nil, err
	}

	i := 0
	var sel []Miner
	// We add 1 on top of the replication factor in case one deal fails
	for len(sel) < msp.RF+2 && i < len(addrs) {
		mi, err := s.fAPI.StateMinerInfo(ctx, addrs[i], fil.EmptyTSK)
		if err != nil {
			return nil, err
		}
		// PeerId is often nil which causes panics down the road
		if mi.PeerId == nil {
			return nil, fmt.Errorf("no peer id for miner %v", addrs[i])
		}
		info := NewStorageProviderInfo(addrs[i], mi.Worker, mi.SectorSize, *mi.PeerId, mi.Multiaddrs)
		i++
		ask, err := s.client.GetAsk(ctx, info)
		if err != nil {
			continue
		}
		// Check miners can fit our piece
		if msp.PieceSize > uint64(ask.MaxPieceSize) ||
			msp.PieceSize < uint64(ask.MinPieceSize) {
			continue
		}

		sel = append(sel, Miner{
			Ask:                 ask,
			Info:                &info,
			WindowPoStProofType: mi.WindowPoStProofType,
		})
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
	storeID, err := s.sp.GetStoreID(params.Data.Root)
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
}

// Quote is an estimate of who can store given content and for how much
type Quote struct {
	Total  fil.FIL
	Miners []address.Address
}

// GetMarketQuote returns the costs of storing for a given CID and duration
func (s *Storage) GetMarketQuote(ctx context.Context, params QuoteParams) (*Quote, error) {
	miners, err := s.LoadMiners(ctx, MinerSelectionParams{
		PieceSize: params.PieceSize,
		RF:        params.RF,
	})
	if err != nil {
		return nil, err
	}

	gib := fil.NewInt(1 << 30)

	var mAddrs []address.Address
	var epochPrices []big.Int

	epochs := calcEpochs(params.Duration)

	pricePerGib := big.Zero()

	for _, m := range miners {
		mAddrs = append(mAddrs, m.Info.Address)
		p := m.Ask.Price

		pricePerGib = fil.BigAdd(pricePerGib, p)
		epochPrice := fil.BigDiv(fil.BigMul(p, fil.NewInt(params.PieceSize)), gib)
		epochPrices = append(epochPrices, epochPrice)

	}
	if len(mAddrs) == 0 {
		return nil, errors.New("no miners fit those parameters")
	}

	epochPrice := fil.BigDiv(fil.BigMul(pricePerGib, fil.NewInt(uint64(params.PieceSize))), gib)
	totalPrice := fil.BigMul(epochPrice, fil.NewInt(uint64(epochs)))
	return &Quote{
		Total:  fil.FIL(totalPrice),
		Miners: mAddrs,
	}, nil
}

// Params are the global parameters for storing on Filecoin with given replication
type Params struct {
	Payload  *storagemarket.DataRef
	Duration time.Duration
	Address  address.Address
	RF       int
}

// NewParams creates a new Params struct for storage
func NewParams(root cid.Cid, dur time.Duration, w address.Address, rf int) Params {
	return Params{
		Payload: &storagemarket.DataRef{
			TransferType: storagemarket.TTGraphsync,
			Root:         root,
		},
		Duration: dur,
		Address:  w,
		RF:       rf,
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
	miners, err := s.LoadMiners(ctx, MinerSelectionParams{
		PieceSize: uint64(p.Payload.PieceSize),
		RF:        p.RF,
	})
	if err != nil {
		return nil, err
	}
	var ma []address.Address
	for _, m := range miners {
		ma = append(ma, m.Info.Address)
	}
	epochs := calcEpochs(p.Duration)
	var drfs []cid.Cid
	for _, m := range miners {
		pcid, err := s.StartDeal(ctx, StartDealParams{
			Data:              p.Payload,
			Wallet:            p.Address,
			Miner:             m,
			EpochPrice:        m.Ask.Price,
			MinBlocksDuration: uint64(epochs),
			DealStartEpoch:    -1,
			FastRetrieval:     false,
			VerifiedDeal:      false,
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
