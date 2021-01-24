package payments

import (
	"context"
	"fmt"
	"sync"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/specs-actors/v3/actors/builtin/paych"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	cbor "github.com/ipfs/go-ipld-cbor"
	"github.com/myelnet/go-hop-exchange/filecoin"
	"github.com/myelnet/go-hop-exchange/wallet"
)

// Manager is the interface required to handle payments for the hop exchange
type Manager interface {
	GetChannel(ctx context.Context, from, to address.Address, amt filecoin.BigInt) (*ChannelResponse, error)
	WaitForChannel(context.Context, cid.Cid) (address.Address, error)
	ListChannels() ([]address.Address, error)
	GetChannelInfo(address.Address) (*ChannelInfo, error)
	CreateVoucher(context.Context, address.Address, filecoin.BigInt, uint64) (*VoucherCreateResult, error)
}

// Payments is our full payment system, it manages payment channels,
// stores vouchers and interacts with the filecoin chain to send transactions
type Payments struct {
	ctx      context.Context
	api      filecoin.API
	wal      wallet.Driver
	store    *Store
	actStore *cbor.BasicIpldStore

	lk       sync.RWMutex
	channels map[string]*channel
}

// New creates a new instance of payments manager
func New(ctx context.Context, api filecoin.API, w wallet.Driver, ds datastore.Batching, cbors *cbor.BasicIpldStore) Manager {
	store := NewStore(ds)
	return &Payments{
		ctx:      ctx,
		api:      api,
		wal:      w,
		store:    store,
		actStore: cbors,
		channels: make(map[string]*channel),
	}
}

// GetChannel adds fund to a new channel in a given direction, if one already exists it will update it
// it does not wait for the message to be confirmed on chain
func (p *Payments) GetChannel(ctx context.Context, from, to address.Address, amt filecoin.BigInt) (*ChannelResponse, error) {
	ch, err := p.channelByFromTo(from, to)
	if err != nil {
		return nil, fmt.Errorf("Unable to get or create channel accessor: %v", err)
	}
	addr, pcid, err := ch.get(ctx, amt)
	if err != nil {
		return nil, fmt.Errorf("Unable to get or create pay channel from accessor: %v", err)
	}
	return &ChannelResponse{
		Channel:      addr,
		WaitSentinel: pcid,
	}, nil
}

// WaitForChannel to be ready and return the address on chain
func (p *Payments) WaitForChannel(ctx context.Context, mcid cid.Cid) (address.Address, error) {
	// Find the channel associated with the message CID
	p.lk.Lock()
	ci, err := p.store.ByMessageCid(mcid)
	p.lk.Unlock()

	if err != nil {
		if err == datastore.ErrNotFound {
			return address.Undef, fmt.Errorf("Could not find wait msg cid %s", mcid)
		}
		return address.Undef, err
	}

	ch, err := p.channelByFromTo(ci.Control, ci.Target)
	if err != nil {
		return address.Undef, err
	}

	return ch.getWaitReady(ctx, mcid)
}

// CreateVoucher creates a voucher for a given payment channel
func (p *Payments) CreateVoucher(ctx context.Context, chAddr address.Address, amt filecoin.BigInt, lane uint64) (*VoucherCreateResult, error) {
	vouch := paych.SignedVoucher{Amount: amt, Lane: lane}
	ch, err := p.channelByAddress(chAddr)
	if err != nil {
		return nil, fmt.Errorf("Unable to find channel to create voucher for: %v", err)
	}
	return ch.createVoucher(ctx, chAddr, vouch)
}

// AllocateLane creates a new lane for a given channel
func (p *Payments) AllocateLane(chAddr address.Address) (uint64, error) {
	ch, err := p.channelByAddress(chAddr)
	if err != nil {
		return 0, fmt.Errorf("Unable to find channel to allocate lane: %v", err)
	}
	return ch.allocateLane(chAddr)
}

// ChannelAvailableFunds returns the amount a channel can still spend
func (p *Payments) ChannelAvailableFunds(chAddr address.Address) (*AvailableFunds, error) {
	ch, err := p.channelByAddress(chAddr)
	if err != nil {
		return nil, err
	}

	ci, err := ch.getChannelInfo(chAddr)
	if err != nil {
		return nil, err
	}

	return ch.availableFunds(ci.ChannelID)
}

// ListChannels we have in the store
func (p *Payments) ListChannels() ([]address.Address, error) {
	// Need to take an exclusive lock here so that channel operations can't run
	// in parallel (see channelLock)
	p.lk.Lock()
	defer p.lk.Unlock()

	return p.store.ListChannels()
}

// ListVouchers we have created so far
func (p *Payments) ListVouchers(ctx context.Context, chAddr address.Address) ([]*VoucherInfo, error) {
	ch, err := p.channelByAddress(chAddr)
	if err != nil {
		return nil, err
	}
	return ch.listVouchers(ctx, chAddr)
}

// GetChannelInfo from the store
func (p *Payments) GetChannelInfo(chAddr address.Address) (*ChannelInfo, error) {
	ch, err := p.channelByAddress(chAddr)
	if err != nil {
		return nil, err
	}
	return ch.getChannelInfo(chAddr)
}

func (p *Payments) channelByFromTo(from address.Address, to address.Address) (*channel, error) {
	key := p.channelCacheKey(from, to)

	// First take a read lock and check the cache
	p.lk.RLock()
	ch, ok := p.channels[key]
	p.lk.RUnlock()
	if ok {
		return ch, nil
	}

	// Not in cache, so take a write lock
	p.lk.Lock()
	defer p.lk.Unlock()

	// Need to check cache again in case it was updated between releasing read
	// lock and taking write lock
	ch, ok = p.channels[key]
	if !ok {
		// Not in cache, so create a new one and store in cache
		ch = p.addChannelToCache(from, to)
	}

	return ch, nil
}

func (p *Payments) addChannelToCache(from address.Address, to address.Address) *channel {
	key := p.channelCacheKey(from, to)
	ch := &channel{
		from:         from,
		to:           to,
		ctx:          p.ctx,
		api:          p.api,
		wal:          p.wal,
		actStore:     p.actStore,
		store:        p.store,
		lk:           &multiLock{globalLock: &p.lk},
		msgListeners: newMsgListeners(),
	}
	// TODO: Use LRU
	p.channels[key] = ch
	return ch
}

func (p *Payments) channelByAddress(chAddr address.Address) (*channel, error) {
	// Get the channel from / to
	p.lk.RLock()
	channelInfo, err := p.store.ByAddress(chAddr)
	p.lk.RUnlock()
	if err != nil {
		return nil, err
	}

	// TODO: cache by channel address so we can get by address instead of using from / to
	return p.channelByFromTo(channelInfo.Control, channelInfo.Target)
}

func (p *Payments) channelCacheKey(from address.Address, to address.Address) string {
	return from.String() + "->" + to.String()
}

// ChannelResponse is the result of calling GetChannel
type ChannelResponse struct {
	Channel      address.Address
	WaitSentinel cid.Cid
}
