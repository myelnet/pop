package payments

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/specs-actors/v3/actors/builtin"
	"github.com/filecoin-project/specs-actors/v3/actors/builtin/paych"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	cbor "github.com/ipfs/go-ipld-cbor"
	"github.com/myelnet/pop/filecoin"
	"github.com/myelnet/pop/wallet"
)

// Manager is the interface required to handle payments for the pop exchange
type Manager interface {
	GetChannel(ctx context.Context, from, to address.Address, amt filecoin.BigInt) (*ChannelResponse, error)
	WaitForChannel(context.Context, cid.Cid) (address.Address, error)
	ListChannels() ([]address.Address, error)
	GetChannelInfo(address.Address) (*ChannelInfo, error)
	CreateVoucher(context.Context, address.Address, filecoin.BigInt, uint64) (*VoucherCreateResult, error)
	AllocateLane(context.Context, address.Address) (uint64, error)
	AddVoucherInbound(context.Context, address.Address, *paych.SignedVoucher, []byte, filecoin.BigInt) (filecoin.BigInt, error)
	ChannelAvailableFunds(address.Address) (*AvailableFunds, error)
	Settle(context.Context, address.Address) error
	StartAutoCollect(context.Context) error
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

	stopmu sync.Mutex
	stop   chan struct{}
}

// New creates a new instance of payments manager
func New(ctx context.Context, api filecoin.API, w wallet.Driver, ds datastore.Batching, bs blockstore.Blockstore) *Payments {
	store := NewStore(ds)
	return &Payments{
		ctx:      ctx,
		api:      api,
		wal:      w,
		store:    store,
		actStore: cbor.NewCborStore(bs),
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
func (p *Payments) AllocateLane(ctx context.Context, chAddr address.Address) (uint64, error) {
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

// AddVoucherInbound adds a voucher for an inbound channel.
// If the channel is not in the store, fetches the channel from state (and checks that
// the channel To address is owned by the wallet).
func (p *Payments) AddVoucherInbound(ctx context.Context, chAddr address.Address, sv *paych.SignedVoucher, proof []byte, minDelta filecoin.BigInt) (filecoin.BigInt, error) {
	if len(proof) > 0 {
		return filecoin.NewInt(0), fmt.Errorf("err proof not supported")
	}
	// Get the channel, creating it from state if necessary
	ch, err := p.inboundChannel(ctx, chAddr)
	if err != nil {
		return filecoin.BigInt{}, err
	}
	ch.lk.Lock()
	defer ch.lk.Unlock()
	return ch.addVoucherUnlocked(ctx, chAddr, sv, minDelta)
}

// SubmitVoucher gets a channel from the store and submits a new voucher to the chain
func (p *Payments) SubmitVoucher(ctx context.Context, addr address.Address, sv *paych.SignedVoucher, secret []byte, proof []byte) (cid.Cid, error) {
	if len(proof) > 0 {
		return cid.Undef, fmt.Errorf("err proof not supported")
	}
	ch, err := p.channelByAddress(addr)
	if err != nil {
		return cid.Undef, err
	}
	return ch.submitVoucher(ctx, addr, sv, secret)
}

// Settle a given channel and submits relevant vouchers then return the successfully submitted voucher
func (p *Payments) Settle(ctx context.Context, addr address.Address) error {
	ch, err := p.channelByAddress(addr)
	if err != nil {
		return err
	}
	best, err := p.bestSpendableByLane(ctx, addr)
	if err != nil {
		return err
	}
	if len(best) == 0 {
		// If we have no vouchers to redeem it's probably not worth settling
		return errors.New("no vouchers to redeem")
	}

	var wg sync.WaitGroup
	wg.Add(len(best) + 1)

	// We send our settle message at the same time as our voucher update messages
	// hopefully they get on chain at the same time
	mcid, err := ch.settle(ctx, addr)
	if err != nil {
		return err
	}
	// cancelling the context will timeout the wait function and all our goroutines will return
	go func(sc cid.Cid) {
		defer wg.Done()
		lookup, err := p.api.StateWaitMsg(ctx, sc, uint64(5))
		if err != nil {
			fmt.Printf("settling payment channel %s failed: %v\n", addr, err)
			return
		}
		if lookup.Receipt.ExitCode != 0 {
			fmt.Printf("payment channel %s execution failed with code: %d\n", addr, lookup.Receipt.ExitCode)
		}
	}(mcid)

	// I think all lanes should be merged so we might only get a single message but just in case
	// we iterate over all the vouchers
	for _, voucher := range best {
		mcid, err := ch.submitVoucher(ctx, addr, voucher, nil)
		if err != nil {
			fmt.Printf("unable to submit voucher: %v\n", err)
			continue
		}
		go func(vouch *paych.SignedVoucher, mcid cid.Cid) {
			defer wg.Done()
			lookup, err := p.api.StateWaitMsg(ctx, mcid, uint64(5))
			if err != nil {
				fmt.Printf("waiting for voucher to submit on channel %s: %v\n", addr, err)
				return
			}
			if lookup.Receipt.ExitCode != 0 {
				fmt.Printf("voucher update execution failed for channel %s with code %d\n", addr, lookup.Receipt.ExitCode)
			}
		}(voucher, mcid)
	}
	go func() {
		// Wait to settle and send the last vouchers then we save the collection epoch
		wg.Wait()
		state, err := ch.loadActorState(addr)
		if err != nil {
			fmt.Printf("loading actor state: %v\n", err)
			return
		}
		ci, err := p.store.ByAddress(addr)
		if err != nil {
			return
		}
		ep, err := state.SettlingAt()
		if err != nil {
			return
		}
		p.store.SetChannelSettlingAt(ci, ep)
	}()
	return nil
}

// StartAutoCollect is a routine that ticks every epoch and tries to collect settling payment channels
// called usually at startup
func (p *Payments) StartAutoCollect(ctx context.Context) error {
	if p.api == nil {
		return nil
	}
	// TODO: we may want to load all channel info into memory to avoid reading from the store every 30s
	go p.collectLoop(ctx)
	return nil
}

// collectLoop is the collection routine
func (p *Payments) collectLoop(ctx context.Context) {
	p.stopmu.Lock()
	if p.stop != nil {
		return // already running
	}
	p.stop = make(chan struct{})
	p.stopmu.Unlock()

	head, err := p.api.ChainHead(ctx)
	if err != nil {
		return
	}
	epoch := head.Height()
	for {
		select {
		case <-time.Tick(builtin.EpochDurationSeconds * time.Second):
			epoch++
			p.collectForEpoch(ctx, epoch)

			// We've prob lost sync with the clock so let's query again just in case
			head, err := p.api.ChainHead(ctx)
			// no need to fail the whole routine if the request fails once in a while
			if err != nil {
				fmt.Printf("%v\n", err)
				continue
			}
			epoch = head.Height()

		case <-ctx.Done():
			return
		case <-p.stop:
			return
		}
	}
}

// collectForEpoch tries to collect any channel matching with the epoch
func (p *Payments) collectForEpoch(ctx context.Context, epoch abi.ChainEpoch) error {
	settling, err := p.store.ListSettlingChannels()
	if err != nil || len(settling) == 0 {
		return err
	}
	for _, sci := range settling {
		if sci.SettlingAt < epoch {
			// Using ByFromTo to avoid another store read
			ch, err := p.channelByFromTo(sci.Control, sci.Target)
			if err != nil {
				return err
			}
			mcid, err := ch.collect(ctx, *sci.Channel)
			if err != nil {
				return err
			}
			lookup, err := p.api.StateWaitMsg(ctx, mcid, uint64(5))
			if err != nil {
				return fmt.Errorf("waiting to collect channel %s: %v", sci.Channel, err)
			}
			if lookup.Receipt.ExitCode != 0 {
				return fmt.Errorf("collecting channel %s failed with code %d", sci.Channel, lookup.Receipt.ExitCode)
			}
			if lookup.Receipt.ExitCode == 0 {
				ch.mutateChannelInfo(sci.ChannelID, func(ci *ChannelInfo) {
					ci.Settling = false
				})
			}

		}
	}
	return nil
}

// CheckVoucherSpendable checks if the given voucher is currently spendable
func (p *Payments) CheckVoucherSpendable(ctx context.Context, addr address.Address, sv *paych.SignedVoucher, secret []byte, proof []byte) (bool, error) {
	// Voucher can take proofs with a secret allowing to send vouchers securely without giving authorization
	//  to spend them unless the secret is communicated separately
	if len(proof) > 0 {
		return false, fmt.Errorf("proof not supported yet")
	}
	ch, err := p.channelByAddress(addr)
	if err != nil {
		return false, err
	}

	return ch.checkVoucherSpendable(ctx, addr, sv, secret)
}

// bestSpendableByLane only returns the merged vouchers for each lane
func (p *Payments) bestSpendableByLane(ctx context.Context, ch address.Address) (map[uint64]*paych.SignedVoucher, error) {
	vouchers, err := p.ListVouchers(ctx, ch)
	if err != nil {
		return nil, err
	}

	bestByLane := make(map[uint64]*paych.SignedVoucher)
	for _, vi := range vouchers {
		spendable, err := p.CheckVoucherSpendable(ctx, ch, vi.Voucher, nil, nil)
		if err != nil {
			return nil, err
		}
		if spendable && (bestByLane[vi.Voucher.Lane] == nil || vi.Voucher.Amount.GreaterThan(bestByLane[vi.Voucher.Lane].Amount)) {
			bestByLane[vi.Voucher.Lane] = vi.Voucher
		}
	}
	return bestByLane, nil
}

// inboundChannel gets an accessor for the given channel. The channel
// must either exist in the store, or be an inbound channel that can be created
// from state.
func (p *Payments) inboundChannel(ctx context.Context, chAddr address.Address) (*channel, error) {
	// Make sure channel is in store, or can be fetched from state, and that
	// the channel To address is owned by the wallet
	ci, err := p.trackInboundChannel(ctx, chAddr)
	if err != nil {
		return nil, err
	}

	// This is an inbound channel, so To is the Control address (this node)
	from := ci.Target
	to := ci.Control
	return p.channelByFromTo(from, to)
}

func (p *Payments) trackInboundChannel(ctx context.Context, chAddr address.Address) (*ChannelInfo, error) {
	// Need to take an exclusive lock here so that channel operations can't run
	// in parallel (see channelLock)
	p.lk.Lock()
	defer p.lk.Unlock()

	// Check if channel is in store
	ci, err := p.store.ByAddress(chAddr)
	if err == nil {
		// Channel is in store, so it's already being tracked
		return ci, nil
	}

	// If there's an error (besides channel not in store) return err
	if err != ErrChannelNotTracked {
		return nil, err
	}

	// Channel is not in store, so get channel from state
	stateCi, err := p.loadStateChannelInfo(chAddr, DirInbound)
	if err != nil {
		return nil, err
	}

	// Check that channel To address is in wallet
	// to := stateCi.Control // Inbound channel so To addr is Control (this node)
	// toKey, err := pm.api.StateAccountKey(ctx, to, filecoin.EmptyTSK)
	// if err != nil {
	// 	return nil, err
	// }
	// has, err := pm.wallet.WalletHas(ctx, toKey)
	// if err != nil {
	// 	return nil, err
	// }
	// if !has {
	// 	msg := "cannot add voucher for channel %s: wallet does not have key for address %s"
	// 	return nil, fmt.Errorf(msg, ch, to)
	// }

	// Save channel to store
	return p.store.TrackChannel(stateCi)
}

func (p *Payments) loadStateChannelInfo(chAddr address.Address, dir uint64) (*ChannelInfo, error) {
	ch := &channel{
		ctx:          p.ctx,
		api:          p.api,
		wal:          p.wal,
		actStore:     p.actStore,
		store:        p.store,
		lk:           &multiLock{globalLock: &p.lk},
		msgListeners: newMsgListeners(),
	}
	as, err := ch.loadActorState(chAddr)
	if err != nil {
		return nil, err
	}
	f, err := as.From()
	if err != nil {
		return nil, err
	}
	from, err := p.api.StateAccountKey(p.ctx, f, filecoin.EmptyTSK)
	if err != nil {
		return nil, fmt.Errorf("Unable to get account key for From actor state: %v", err)
	}
	t, err := as.To()
	if err != nil {
		return nil, err
	}
	to, err := p.api.StateAccountKey(p.ctx, t, filecoin.EmptyTSK)
	if err != nil {
		return nil, fmt.Errorf("Unable to get account key for To actor state: %v", err)
	}
	nextLane, err := nextLaneFromState(as)
	if err != nil {
		return nil, fmt.Errorf("Unable to get next lane from state: %v", err)
	}
	ci := &ChannelInfo{
		Channel:   &chAddr,
		Direction: dir,
		NextLane:  nextLane,
	}
	if dir == DirOutbound {
		ci.Control = from
		ci.Target = to
	} else {
		ci.Control = to
		ci.Target = from
	}
	return ci, nil

}

func nextLaneFromState(st ChannelState) (uint64, error) {
	laneCount, err := st.LaneCount()
	if err != nil {
		return 0, err
	}
	if laneCount == 0 {
		return 0, nil
	}

	maxID := uint64(0)
	if err := st.ForEachLaneState(func(idx uint64, _ LaneState) error {
		if idx > maxID {
			maxID = idx
		}
		return nil
	}); err != nil {
		return 0, err
	}

	return maxID + 1, nil
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
