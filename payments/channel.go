package payments

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/filecoin-project/go-address"
	cborutil "github.com/filecoin-project/go-cbor-util"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	cbortypes "github.com/filecoin-project/go-state-types/cbor"
	init2 "github.com/filecoin-project/specs-actors/v7/actors/builtin/init"
	"github.com/filecoin-project/specs-actors/v7/actors/builtin/paych"
	"github.com/filecoin-project/specs-actors/v7/actors/util/adt"
	"github.com/hannahhoward/go-pubsub"
	"github.com/ipfs/go-cid"
	"github.com/myelnet/pop/filecoin"
	"github.com/myelnet/pop/wallet"
	"github.com/rs/zerolog/log"
)

// channel manages the lifecycle of a payment channel
type channel struct {
	from          address.Address
	to            address.Address
	ctx           context.Context
	api           filecoin.API
	wal           wallet.Driver
	store         *Store
	actStore      *FilObjectStore
	lk            *multiLock
	fundsReqQueue []*fundsReq
	msgListeners  msgListeners
}

// get ensures that a channel exists between the from and to addresses,
// and adds the given amount of funds.
// If the channel does not exist a create channel message is sent and the
// message CID is returned.
// If the channel does exist an add funds message is sent and both the channel
// address and message CID are returned.
// If there is an in progress operation (create channel / add funds), get
// blocks until the previous operation completes, then returns both the channel
// address and the CID of the new add funds message.
// If an operation returns an error, subsequent waiting operations will still
// be attempted.
func (ch *channel) get(ctx context.Context, amt filecoin.BigInt) (address.Address, cid.Cid, error) {
	// Add the request to add funds to a queue and wait for the result
	freq := newFundsReq(ctx, amt)
	ch.enqueue(freq)
	select {
	case res := <-freq.promise:
		return res.channel, res.mcid, res.err
	case <-ctx.Done():
		freq.cancel()
		return address.Undef, cid.Undef, ctx.Err()
	}
}

// getWaitReady waits for the response to the message with the given cid
func (ch *channel) getWaitReady(ctx context.Context, mcid cid.Cid) (address.Address, error) {
	ch.lk.Lock()

	// First check if the message has completed
	msgInfo, err := ch.store.GetMessage(mcid)
	if err != nil {
		ch.lk.Unlock()

		return address.Undef, err
	}

	// If the create channel / add funds message failed, return an error
	if len(msgInfo.Err) > 0 {
		ch.lk.Unlock()

		return address.Undef, fmt.Errorf(msgInfo.Err)
	}

	// If the message has completed successfully
	if msgInfo.Received {
		ch.lk.Unlock()

		// Get the channel address
		ci, err := ch.store.ByMessageCid(mcid)
		if err != nil {
			return address.Undef, err
		}

		if ci.Channel == nil {
			log.Panic().
				Str("cid", mcid.String()).
				Msg("create / add funds message succeeded but channelInfo.Channel is nil")
		}
		return *ci.Channel, nil
	}

	// The message hasn't completed yet so wait for it to complete
	promise := ch.msgPromise(ctx, mcid)

	// Unlock while waiting
	ch.lk.Unlock()

	select {
	case res := <-promise:
		return res.channel, res.err
	case <-ctx.Done():
		return address.Undef, ctx.Err()
	}
}

type onMsgRes struct {
	channel address.Address
	err     error
}

// msgPromise returns a channel that receives the result of the message with
// the given CID
func (ch *channel) msgPromise(ctx context.Context, mcid cid.Cid) chan onMsgRes {
	promise := make(chan onMsgRes)
	triggerUnsub := make(chan struct{})
	unsub := ch.msgListeners.onMsgComplete(mcid, func(err error) {
		close(triggerUnsub)

		// Use a go-routine so as not to block the event handler loop
		go func() {
			res := onMsgRes{err: err}
			if res.err == nil {
				// Get the channel associated with the message cid
				ci, err := ch.store.ByMessageCid(mcid)
				if err != nil {
					res.err = err
				} else {
					res.channel = *ci.Channel
				}
			}

			// Pass the result to the caller
			select {
			case promise <- res:
			case <-ctx.Done():
			}
		}()
	})

	// Unsubscribe when the message is received or the context is done
	go func() {
		select {
		case <-ctx.Done():
		case <-triggerUnsub:
		}

		unsub()
	}()

	return promise
}

///========== Queue Operations ====================

// Queue up an add funds operation
func (ch *channel) enqueue(task *fundsReq) {
	ch.lk.Lock()
	defer ch.lk.Unlock()

	ch.fundsReqQueue = append(ch.fundsReqQueue, task)
	go ch.processQueue("")
}

// Run the operations in the queue
func (ch *channel) processQueue(channelID string) (*AvailableFunds, error) {
	ch.lk.Lock()
	defer ch.lk.Unlock()

	// Remove cancelled requests
	ch.filterQueue()

	// If there's nothing in the queue, bail out
	if len(ch.fundsReqQueue) == 0 {
		return ch.currentAvailableFunds(channelID, filecoin.NewInt(0))
	}

	// Merge all pending requests into one.
	// For example if there are pending requests for 3, 2, 4 then
	// amt = 3 + 2 + 4 = 9
	merged := newMergedFundsReq(ch.fundsReqQueue[:])
	amt := merged.sum()
	if amt.IsZero() {
		// Note: The amount can be zero if requests are cancelled as we're
		// building the mergedFundsReq
		return ch.currentAvailableFunds(channelID, amt)
	}

	res := ch.processTask(merged.ctx, amt)

	// If the task is waiting on an external event (eg something to appear on
	// chain) it will return nil
	if res == nil {
		// Stop processing the fundsReqQueue and wait. When the event occurs it will
		// call processQueue() again
		return ch.currentAvailableFunds(channelID, amt)
	}

	// Finished processing so clear the queue
	ch.fundsReqQueue = nil

	// Call the task callback with its results
	merged.onComplete(res)

	return ch.currentAvailableFunds(channelID, filecoin.NewInt(0))
}

// filterQueue filters cancelled requests out of the queue
func (ch *channel) filterQueue() {
	if len(ch.fundsReqQueue) == 0 {
		return
	}

	// Remove cancelled requests
	i := 0
	for _, r := range ch.fundsReqQueue {
		if r.isActive() {
			ch.fundsReqQueue[i] = r
			i++
		}
	}

	// Allow GC of remaining slice elements
	for rem := i; rem < len(ch.fundsReqQueue); rem++ {
		ch.fundsReqQueue[i] = nil
	}

	// Resize slice
	ch.fundsReqQueue = ch.fundsReqQueue[:i]
}

// queueSize is the size of the funds request queue (used by tests)
func (ch *channel) queueSize() int {
	ch.lk.Lock()
	defer ch.lk.Unlock()

	return len(ch.fundsReqQueue)
}

// processTask checks the state of the channel and takes appropriate action
// (see description of getPaych).
// Note that processTask may be called repeatedly in the same state, and should
// return nil if there is no state change to be made (eg when waiting for a
// message to be confirmed on chain)
func (ch *channel) processTask(ctx context.Context, amt filecoin.BigInt) *paychFundsRes {
	// Get the payment channel for the from/to addresses.
	// Note: It's ok if we get ErrChannelNotTracked. It just means we need to
	// create a channel.
	channelInfo, err := ch.store.OutboundActiveByFromTo(ch.from, ch.to)
	if err != nil && err != ErrChannelNotTracked {
		return &paychFundsRes{err: err}
	}

	// If a channel has not yet been created, create one.
	if channelInfo == nil {
		mcid, err := ch.create(ctx, amt)
		if err != nil {
			return &paychFundsRes{err: err}
		}

		return &paychFundsRes{mcid: mcid}
	}

	// If the create channel message has been sent but the channel hasn't
	// been created on chain yet
	if channelInfo.CreateMsg != nil {
		// Wait for the channel to be created before trying again
		return nil
	}

	// If an add funds message was sent to the chain but hasn't been confirmed
	// on chain yet
	if channelInfo.AddFundsMsg != nil {
		// Wait for the add funds message to be confirmed before trying again
		return nil
	}

	// We need to add more funds, so send an add funds message to
	// cover the amount for this request
	mcid, err := ch.addFunds(ctx, channelInfo, amt)
	if err != nil {
		return &paychFundsRes{err: err}
	}
	return &paychFundsRes{channel: *channelInfo.Channel, mcid: *mcid}
}

/// ==============================

func (ch *channel) messageBuilder(ctx context.Context, from address.Address) (MessageBuilder, error) {
	// TODO: check network version and make adjustments on actor version
	return message{from}, nil
}

// create the payment channel with an initial amout
func (ch *channel) create(ctx context.Context, amt filecoin.BigInt) (cid.Cid, error) {
	mb, err := ch.messageBuilder(ctx, ch.from)
	if err != nil {
		return cid.Undef, err
	}

	msg, err := mb.Create(ch.to, amt)
	cp := *msg
	msg = &cp
	if err != nil {
		return cid.Undef, err
	}

	smsg, err := ch.mpoolPush(ctx, msg)
	if err != nil {
		return cid.Undef, err
	}

	ci, err := ch.store.CreateChannel(ch.from, ch.to, smsg.Cid(), amt)
	if err != nil {
		return cid.Undef, fmt.Errorf("Unable to create channel in store: %v", err)
	}

	// Wait for the channel to be created on chain
	go ch.waitForPaychCreateMsg(ci.ChannelID, smsg.Cid())

	return smsg.Cid(), nil
}

func (ch *channel) mpoolPush(ctx context.Context, msg *filecoin.Message) (*filecoin.SignedMessage, error) {
	msg, err := ch.api.GasEstimateMessageGas(ctx, msg, nil, filecoin.EmptyTSK)
	if err != nil {
		return nil, err
	}

	act, err := ch.api.StateGetActor(ctx, msg.From, filecoin.EmptyTSK)
	if err != nil {
		return nil, err
	}
	msg.Nonce = act.Nonce
	mbl, err := msg.ToStorageBlock()
	if err != nil {
		return nil, err
	}

	sig, err := ch.wal.Sign(ctx, msg.From, mbl.Cid().Bytes())
	if err != nil {
		return nil, err
	}

	smsg := &filecoin.SignedMessage{
		Message:   *msg,
		Signature: *sig,
	}

	if _, err := ch.api.MpoolPush(ctx, smsg); err != nil {
		if strings.Contains(err.Error(), "already in mpool, increase GasPremium") {
			// incGas picks up the suggested gas premium from the error message and tries to push
			// a new message to the pool with that amount
			log.Error().Msg("message already in pool, increasing gas")
			return ch.increaseGas(ctx, msg, err.Error())
		}
		return nil, fmt.Errorf("MpoolPush failed with error: %v", err)
	}

	return smsg, nil
}

func (ch *channel) increaseGas(ctx context.Context, msg *filecoin.Message, rec string) (*filecoin.SignedMessage, error) {
	r := regexp.MustCompile(`to (\d+)`)
	match := r.FindStringSubmatch(rec)
	if len(match) != 2 {
		return nil, fmt.Errorf("failed to match new gas price")
	}
	prem, err := big.FromString(match[1])
	if err != nil {
		return nil, err
	}
	msg.GasPremium = prem
	mbl, err := msg.ToStorageBlock()
	if err != nil {
		return nil, err
	}

	sig, err := ch.wal.Sign(ctx, msg.From, mbl.Cid().Bytes())
	if err != nil {
		return nil, err
	}

	smsg := &filecoin.SignedMessage{
		Message:   *msg,
		Signature: *sig,
	}

	if _, err := ch.api.MpoolPush(ctx, smsg); err != nil {
		return nil, fmt.Errorf("MpoolPush failed with error: %v", err)
	}
	return smsg, nil
}

// Change the state of the channel in the store
func (ch *channel) mutateChannelInfo(channelID string, mutate func(*ChannelInfo)) {
	channelInfo, err := ch.store.ByChannelID(channelID)

	// If there's an error reading or writing to the store just log an error.
	// For now we're assuming it's unlikely to happen in practice.
	// Later we may want to implement a transactional approach, whereby
	// we record to the store that we're going to send a message, send
	// the message, and then record that the message was sent.
	if err != nil {
		log.Error().Err(err).Msg("error reading channel info from store")
		return
	}

	mutate(channelInfo)

	err = ch.store.putChannelInfo(channelInfo)
	if err != nil {
		log.Error().Err(err).Msg("error writing channel info to store")
	}
}

// waitForPaychCreateMsg waits for mcid to appear on chain and stores the robust address of the
// created payment channel
func (ch *channel) waitForPaychCreateMsg(channelID string, mcid cid.Cid) {
	err := ch.waitPaychCreateMsg(channelID, mcid)
	ch.msgWaitComplete(mcid, err)
}

// waitPaychCreateMsg wait for a given confidence index and cleans up if the message failed
func (ch *channel) waitPaychCreateMsg(channelID string, mcid cid.Cid) error {
	mwait, err := ch.api.StateWaitMsg(ch.ctx, mcid, 3)
	if err != nil {
		return err
	}

	// If channel creation failed
	if mwait.Receipt.ExitCode != 0 {
		ch.lk.Lock()
		defer ch.lk.Unlock()

		// Channel creation failed, so remove the channel from the datastore
		dserr := ch.store.RemoveChannel(channelID)
		if dserr != nil {
			log.Error().Err(dserr).Str("channelID", channelID).Msg("failed to remove channel")
		}

		// Exit code 7 means out of gas
		err := fmt.Errorf("payment channel creation failed (exit code %d)", mwait.Receipt.ExitCode)
		log.Error().Err(err).Msg("error")
		return err
	}

	// TODO: ActorUpgrade abstract over this.
	// This "works" because it hasn't changed from v0 to v2, but we still
	// need an abstraction here.
	var decodedReturn init2.ExecReturn
	err = decodedReturn.UnmarshalCBOR(bytes.NewReader(mwait.Receipt.Return))
	if err != nil {
		log.Error().Err(err).Msg("error decoding Receipt")
		return err
	}

	ch.lk.Lock()
	defer ch.lk.Unlock()

	// Store robust address of channel
	ch.mutateChannelInfo(channelID, func(channelInfo *ChannelInfo) {
		channelInfo.Channel = &decodedReturn.RobustAddress
		channelInfo.Amount = channelInfo.PendingAmount
		channelInfo.PendingAmount = filecoin.NewInt(0)
		channelInfo.CreateMsg = nil
	})

	return nil
}

// msgWaitComplete is called when the message for a previous task is confirmed
// or there is an error.
func (ch *channel) msgWaitComplete(mcid cid.Cid, err error) {
	ch.lk.Lock()
	defer ch.lk.Unlock()

	// Save the message result to the store
	dserr := ch.store.SaveMessageResult(mcid, err)
	if dserr != nil {
		log.Error().Err(dserr).Msg("saving message result")
	}

	// Inform listeners that the message has completed
	ch.msgListeners.fireMsgComplete(mcid, err)

	// The queue may have been waiting for msg completion to proceed, so
	// process the next queue item
	if len(ch.fundsReqQueue) > 0 {
		go ch.processQueue("")
	}
}

// addFunds sends a message to add funds to the channel and returns the message cid
func (ch *channel) addFunds(ctx context.Context, channelInfo *ChannelInfo, amt filecoin.BigInt) (*cid.Cid, error) {
	msg := &filecoin.Message{
		To:     *channelInfo.Channel,
		From:   channelInfo.Control,
		Value:  amt,
		Method: 0,
	}

	smsg, err := ch.mpoolPush(ctx, msg)
	if err != nil {
		return nil, err
	}
	mcid := smsg.Cid()

	// Store the add funds message CID on the channel
	ch.mutateChannelInfo(channelInfo.ChannelID, func(ci *ChannelInfo) {
		ci.PendingAmount = amt
		ci.AddFundsMsg = &mcid
	})

	// Store a reference from the message CID to the channel, so that we can
	// look up the channel from the message CID
	err = ch.store.SaveNewMessage(channelInfo.ChannelID, mcid)
	if err != nil {
		log.Error().Err(err).Str("mcid", mcid.String()).Msg("saving add funds message CID")
	}

	go ch.waitForAddFundsMsg(channelInfo.ChannelID, mcid)

	return &mcid, nil
}

// waitForAddFundsMsg waits for mcid to appear on chain and returns error, if any
func (ch *channel) waitForAddFundsMsg(channelID string, mcid cid.Cid) {
	err := ch.waitAddFundsMsg(channelID, mcid)
	ch.msgWaitComplete(mcid, err)
}

func (ch *channel) waitAddFundsMsg(channelID string, mcid cid.Cid) error {
	mwait, err := ch.api.StateWaitMsg(ch.ctx, mcid, 3)
	if err != nil {
		log.Error().Err(err).Str("mcid", mcid.String()).Msg("error waiting for chain message")
		return err
	}

	if mwait.Receipt.ExitCode != 0 {
		err := fmt.Errorf("voucher channel creation failed: adding funds (exit code %d)", mwait.Receipt.ExitCode)
		log.Error().Err(err).Msg("error")

		ch.lk.Lock()
		defer ch.lk.Unlock()

		ch.mutateChannelInfo(channelID, func(channelInfo *ChannelInfo) {
			channelInfo.PendingAmount = filecoin.NewInt(0)
			channelInfo.AddFundsMsg = nil
		})

		return err
	}

	ch.lk.Lock()
	defer ch.lk.Unlock()

	// Store updated amount
	ch.mutateChannelInfo(channelID, func(channelInfo *ChannelInfo) {
		channelInfo.Amount = filecoin.BigAdd(channelInfo.Amount, channelInfo.PendingAmount)
		channelInfo.PendingAmount = filecoin.NewInt(0)
		channelInfo.AddFundsMsg = nil
	})

	return nil
}

// load the actor state from the lotus chain when we don't have a record of it locally
func (ch *channel) loadActorState(chAddr address.Address) (ChannelState, error) {
	actorState, err := ch.api.StateReadState(ch.ctx, chAddr, filecoin.EmptyTSK)

	// TODO: this is a hack to cast the types into the proper data model
	// there's probably a nicer way to do it
	stateEncod, err := json.Marshal(actorState.State)

	adtStore := adt.WrapStore(ch.ctx, ch.actStore)
	state := channelState{store: adtStore}
	err = json.Unmarshal(stateEncod, &state)
	if err != nil {
		return nil, fmt.Errorf("Error parsing actor state: %v", err)
	}

	return &state, nil
}

// createVoucher creates a voucher with the given specification, setting its
// nonce, signing the voucher and storing it in the local datastore.
// If there are not enough funds in the channel to create the voucher, returns
// the shortfall in funds.
func (ch *channel) createVoucher(ctx context.Context, chAddr address.Address, voucher paych.SignedVoucher) (*VoucherCreateResult, error) {
	ch.lk.Lock()
	defer ch.lk.Unlock()

	// Find the channel for the voucher
	ci, err := ch.store.ByAddress(chAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to get channel info by address: %v", err)
	}

	// Set the voucher channel
	sv := &voucher
	sv.ChannelAddr = chAddr

	// Get the next nonce on the given lane
	sv.Nonce = ch.nextNonceForLane(ci, voucher.Lane)

	// Sign the voucher
	vb, err := sv.SigningBytes()
	if err != nil {
		return nil, fmt.Errorf("failed to get voucher signing bytes: %v", err)
	}

	sig, err := ch.wal.Sign(ctx, ci.Control, vb)
	if err != nil {
		return nil, fmt.Errorf("failed to sign voucher: %v", err)
	}
	sv.Signature = sig

	// Store the voucher
	if _, err := ch.addVoucherUnlocked(ctx, chAddr, sv, filecoin.NewInt(0)); err != nil {
		// If there are not enough funds in the channel to cover the voucher,
		// return a voucher create result with the shortfall
		if ife, ok := err.(insufficientFundsErr); ok {
			return &VoucherCreateResult{
				Shortfall: ife.Shortfall(),
			}, nil
		}

		return nil, fmt.Errorf("failed to persist voucher: %v", err)
	}

	return &VoucherCreateResult{Voucher: sv, Shortfall: filecoin.NewInt(0)}, nil
}

func (ch *channel) nextNonceForLane(ci *ChannelInfo, lane uint64) uint64 {
	var maxnonce uint64
	for _, v := range ci.Vouchers {
		if v.Voucher.Lane == lane {
			if v.Voucher.Nonce > maxnonce {
				maxnonce = v.Voucher.Nonce
			}
		}
	}

	return maxnonce + 1
}

func (ch *channel) addVoucherUnlocked(ctx context.Context, chAddr address.Address, sv *paych.SignedVoucher, minDelta filecoin.BigInt) (filecoin.BigInt, error) {
	ci, err := ch.store.ByAddress(chAddr)
	if err != nil {
		return filecoin.BigInt{}, fmt.Errorf("looking for channel: %w", err)
	}

	// Check if the voucher has already been added
	for _, v := range ci.Vouchers {
		eq, err := cborutil.Equals(sv, v.Voucher)
		if err != nil {
			return filecoin.BigInt{}, fmt.Errorf("cborutil.Equals: %w", err)
		}
		if eq {
			// Ignore the duplicate voucher.
			log.Info().Msg("AddVoucher: voucher re-added")
			return filecoin.NewInt(0), nil
		}

	}

	// Check voucher validity
	laneStates, balance, err := ch.checkVoucherValidUnlocked(ctx, chAddr, sv)
	if err != nil {
		return filecoin.NewInt(0), err
	}

	// The change in value is the delta between the voucher amount and
	// the highest previous voucher amount for the lane
	laneState, exists := laneStates[sv.Lane]
	redeemed := big.NewInt(0)
	if exists {
		redeemed, err = laneState.Redeemed()
		if err != nil {
			return filecoin.NewInt(0), fmt.Errorf("laneState.Redeemed(): %w", err)
		}
	}

	delta := filecoin.BigSub(sv.Amount, redeemed)
	if minDelta.GreaterThan(delta) {
		return delta, fmt.Errorf("addVoucher: supplied token amount too low; minD=%s, D=%s; laneAmt=%s; v.Amt=%s", minDelta, delta, redeemed, sv.Amount)
	}

	ci.Vouchers = append(ci.Vouchers, &VoucherInfo{
		Voucher: sv,
	})

	if ci.NextLane <= sv.Lane {
		ci.NextLane = sv.Lane + 1
	}

	// This is used in the case of a provider adding vouchers. The Amount should be 0 since we didn't
	// create the channel. As a result we can avoid an extra call to the actor state when needing to
	// check available funds.
	if ci.Amount.LessThan(balance) {
		ci.Amount = balance
	}

	return delta, ch.store.putChannelInfo(ci)
}

func (ch *channel) checkVoucherValidUnlocked(ctx context.Context, chAddr address.Address, sv *paych.SignedVoucher) (map[uint64]LaneState, filecoin.BigInt, error) {
	ei := filecoin.EmptyInt
	if sv.ChannelAddr != chAddr {
		return nil, ei, fmt.Errorf("voucher ChannelAddr doesn't match channel address, got %s, expected %s", sv.ChannelAddr, chAddr)
	}

	act, err := ch.api.StateGetActor(ctx, chAddr, filecoin.EmptyTSK)
	if err != nil {
		return nil, ei, err
	}

	// Load payment channel actor state
	pchState, err := ch.loadActorState(chAddr)
	if err != nil {
		return nil, ei, fmt.Errorf("loadActorState: %w", err)
	}

	// Load channel "From" account actor state
	f, err := pchState.From()
	if err != nil {
		return nil, ei, err
	}

	from, err := ch.api.StateAccountKey(ctx, f, filecoin.EmptyTSK)
	if err != nil {
		return nil, ei, err
	}

	// verify voucher signature
	vb, err := sv.SigningBytes()
	if err != nil {
		return nil, ei, err
	}

	sig, err := wallet.SigTypeSig(sv.Signature.Type, ch.wal.Signers())
	if err != nil {
		return nil, ei, err
	}

	// TODO: technically, either party may create and sign a voucher.
	// However, for now, we only accept them from the channel creator.
	// More complex handling logic can be added later
	if err := sig.Verify(sv.Signature.Data, from, vb); err != nil {
		return nil, ei, err
	}

	// Check the voucher against the highest known voucher nonce / value
	laneStates, err := ch.laneState(pchState, chAddr)
	if err != nil {
		return nil, ei, fmt.Errorf("laneState: %v", err)
	}

	// If the new voucher nonce value is less than the highest known
	// nonce for the lane
	ls, lsExists := laneStates[sv.Lane]
	if lsExists {
		n, err := ls.Nonce()
		if err != nil {
			return nil, ei, err
		}

		// If the voucher amount is less than the highest known voucher amount
		r, err := ls.Redeemed()
		if err != nil {
			return nil, ei, err
		}
		if sv.Nonce <= n {
			return nil, ei, fmt.Errorf("nonce %d too low for lane %d with current nonce %d", sv.Nonce, sv.Lane, n)
		}
		if sv.Amount.LessThanEqual(r) {
			return nil, ei, fmt.Errorf("voucher amount is lower than amount for voucher with lower nonce")
		}

	}

	// Total redeemed is the total redeemed amount for all lanes, including
	// the new voucher
	// eg
	//
	// lane 1 redeemed:            3
	// lane 2 redeemed:            2
	// voucher for lane 1:         5
	//
	// Voucher supersedes lane 1 redeemed, therefore
	// effective lane 1 redeemed:  5
	//
	// lane 1:  5
	// lane 2:  2
	//          -
	// total:   7
	totalRedeemed, err := ch.totalRedeemedWithVoucher(laneStates, sv)
	if err != nil {
		return nil, ei, fmt.Errorf("totalRedeemedWithVoucher: %w", err)
	}

	// Total required balance must not exceed actor balance
	if act.Balance.LessThan(totalRedeemed) {
		return nil, ei, newErrInsufficientFunds(filecoin.BigSub(totalRedeemed, act.Balance))
	}

	if len(sv.Merges) != 0 {
		return nil, ei, fmt.Errorf("dont currently support paych lane merges")
	}

	return laneStates, act.Balance, nil
}

// Get the total redeemed amount across all lanes, after applying the voucher
func (ch *channel) totalRedeemedWithVoucher(laneStates map[uint64]LaneState, sv *paych.SignedVoucher) (big.Int, error) {
	// TODO: merges
	if len(sv.Merges) != 0 {
		return big.Int{}, fmt.Errorf("dont currently support paych lane merges")
	}

	total := big.NewInt(0)
	for _, ls := range laneStates {
		r, err := ls.Redeemed()
		if err != nil {
			return big.Int{}, err
		}
		total = big.Add(total, r)
	}

	lane, ok := laneStates[sv.Lane]
	if ok {
		// If the voucher is for an existing lane, and the voucher nonce
		// is higher than the lane nonce
		n, err := lane.Nonce()
		if err != nil {
			return big.Int{}, err
		}

		if sv.Nonce > n {
			// Add the delta between the redeemed amount and the voucher
			// amount to the total
			r, err := lane.Redeemed()
			if err != nil {
				return big.Int{}, err
			}

			delta := big.Sub(sv.Amount, r)
			total = big.Add(total, delta)
		}
	} else {
		// If the voucher is *not* for an existing lane, just add its
		// value (implicitly a new lane will be created for the voucher)
		total = big.Add(total, sv.Amount)
	}

	return total, nil
}

// laneState gets the LaneStates from chain, then applies all vouchers in
// the data store over the chain state
func (ch *channel) laneState(state ChannelState, chAddr address.Address) (map[uint64]LaneState, error) {
	// TODO: we probably want to call UpdateChannelState with all vouchers to be fully correct
	//  (but technically dont't need to)

	laneCount, err := state.LaneCount()
	if err != nil {
		return nil, err
	}

	// Note: we use a map instead of an array to store laneStates because the
	// client sets the lane ID (the index) and potentially they could use a
	// very large index.
	laneStates := make(map[uint64]LaneState, laneCount)
	err = state.ForEachLaneState(func(idx uint64, ls LaneState) error {
		laneStates[idx] = ls
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Apply locally stored vouchers
	vouchers, err := ch.store.VouchersForPaych(chAddr)
	if err != nil && err != ErrChannelNotTracked {
		return nil, err
	}

	for _, v := range vouchers {
		for range v.Voucher.Merges {
			return nil, fmt.Errorf("paych merges not handled yet")
		}

		// Check if there is an existing laneState in the payment channel
		// for this voucher's lane
		ls, ok := laneStates[v.Voucher.Lane]

		// If the voucher does not have a higher nonce than the existing
		// laneState for this lane, ignore it
		if ok {
			n, err := ls.Nonce()
			if err != nil {
				return nil, err
			}
			if v.Voucher.Nonce < n {
				continue
			}
		}

		// Voucher has a higher nonce, so replace laneState with this voucher
		laneStates[v.Voucher.Lane] = &laneState{
			LaneState: paych.LaneState{
				Redeemed: v.Voucher.Amount,
				Nonce:    v.Voucher.Nonce,
			},
		}
	}

	return laneStates, nil
}

func (ch *channel) allocateLane(chAddr address.Address) (uint64, error) {
	ch.lk.Lock()
	defer ch.lk.Unlock()

	return ch.store.AllocateLane(chAddr)
}

func (ch *channel) availableFunds(channelID string) (*AvailableFunds, error) {
	return ch.processQueue(channelID)
}

func (ch *channel) currentAvailableFunds(channelID string, queuedAmt filecoin.BigInt) (*AvailableFunds, error) {
	if len(channelID) == 0 {
		return nil, nil
	}

	channelInfo, err := ch.store.ByChannelID(channelID)
	if err != nil {
		return nil, err
	}

	// The channel may have a pending create or add funds message
	waitSentinel := channelInfo.CreateMsg
	if waitSentinel == nil {
		waitSentinel = channelInfo.AddFundsMsg
	}

	// Get the total amount redeemed by vouchers.
	// This includes vouchers that have been submitted, and vouchers that are
	// in the datastore but haven't yet been submitted.
	totalRedeemed := filecoin.NewInt(0)
	if channelInfo.Channel != nil {
		chAddr := *channelInfo.Channel
		as, err := ch.loadActorState(chAddr)
		if err != nil {
			return nil, err
		}

		laneStates, err := ch.laneState(as, chAddr)
		if err != nil {
			return nil, err
		}

		for _, ls := range laneStates {
			r, err := ls.Redeemed()
			if err != nil {
				return nil, err
			}
			totalRedeemed = filecoin.BigAdd(totalRedeemed, r)
		}
	}

	return &AvailableFunds{
		Channel:             channelInfo.Channel,
		From:                channelInfo.from(),
		To:                  channelInfo.to(),
		ConfirmedAmt:        channelInfo.Amount,
		PendingAmt:          channelInfo.PendingAmount,
		PendingWaitSentinel: waitSentinel,
		QueuedAmt:           queuedAmt,
		VoucherRedeemedAmt:  totalRedeemed,
	}, nil
}

// submitVoucher sends an Update message to the payment channel actor to anounce a new voucher
func (ch *channel) submitVoucher(ctx context.Context, chAddr address.Address, sv *paych.SignedVoucher, secret []byte) (cid.Cid, error) {
	ch.lk.Lock()
	defer ch.lk.Unlock()

	ci, err := ch.store.ByAddress(chAddr)
	if err != nil {
		return cid.Undef, err
	}

	has, err := ci.hasVoucher(sv)
	if err != nil {
		return cid.Undef, err
	}

	// If the channel has the voucher
	if has {
		// Check that the voucher hasn't already been submitted
		submitted, err := ci.wasVoucherSubmitted(sv)
		if err != nil {
			return cid.Undef, err
		}
		if submitted {
			return cid.Undef, fmt.Errorf("cannot submit voucher that has already been submitted")
		}
	}

	mb, err := ch.messageBuilder(ctx, ci.Control)
	if err != nil {
		return cid.Undef, err
	}

	msg, err := mb.Update(chAddr, sv, secret)
	if err != nil {
		return cid.Undef, err
	}

	smsg, err := ch.mpoolPush(ctx, msg)
	if err != nil {
		return cid.Undef, err
	}

	// If the channel didn't already have the voucher
	if !has {
		// Add the voucher to the channel
		ci.Vouchers = append(ci.Vouchers, &VoucherInfo{
			Voucher: sv,
		})
	}

	// Mark the voucher and any lower-nonce vouchers as having been submitted
	err = ch.store.MarkVoucherSubmitted(ci, sv)
	if err != nil {
		return cid.Undef, err
	}

	return smsg.Cid(), nil
}

func (ch *channel) listVouchers(ctx context.Context, chAddr address.Address) ([]*VoucherInfo, error) {
	ch.lk.Lock()
	defer ch.lk.Unlock()

	// TODO: just having a passthrough method like this feels odd. Seems like
	// there should be some filtering we're doing here
	return ch.store.VouchersForPaych(chAddr)
}

// checkVoucherSpendable runs a state change on the api to verify the call will succeed before sending on chain
func (ch *channel) checkVoucherSpendable(ctx context.Context, addr address.Address, sv *paych.SignedVoucher, secret []byte) (bool, error) {
	ch.lk.Lock()
	defer ch.lk.Unlock()

	ci, err := ch.store.ByAddress(addr)
	if err != nil {
		return false, err
	}

	// Check if voucher has already been submitted
	submitted, err := ci.wasVoucherSubmitted(sv)
	if err != nil {
		return false, err
	}
	if submitted {
		return false, nil
	}

	mb, err := ch.messageBuilder(ctx, ch.to)
	if err != nil {
		return false, err
	}

	mes, err := mb.Update(addr, sv, secret)
	if err != nil {
		return false, err
	}

	ret, err := ch.api.StateCall(ctx, mes, filecoin.EmptyTSK)
	if err != nil {
		return false, err
	}

	if ret.MsgRct.ExitCode != 0 {
		return false, nil
	}

	return true, nil
}

// settle the given channel by sending a Settle message to the chain
func (ch *channel) settle(ctx context.Context, chAddr address.Address) (cid.Cid, error) {
	ch.lk.Lock()
	defer ch.lk.Unlock()

	ci, err := ch.store.ByAddress(chAddr)
	if err != nil {
		return cid.Undef, err
	}

	mb, err := ch.messageBuilder(ctx, ci.Control)
	if err != nil {
		return cid.Undef, err
	}
	msg, err := mb.Settle(chAddr)
	if err != nil {
		return cid.Undef, err
	}
	smgs, err := ch.mpoolPush(ctx, msg)
	if err != nil {
		return cid.Undef, err
	}

	ci.Settling = true
	err = ch.store.putChannelInfo(ci)
	if err != nil {
		log.Error().Err(err).Msg("error marking channel as settled")
	}

	return smgs.Cid(), err
}

// collect the given channel by sending a Collect message to the chain
func (ch *channel) collect(ctx context.Context, chAddr address.Address) (cid.Cid, error) {
	ch.lk.Lock()
	defer ch.lk.Unlock()

	ci, err := ch.store.ByAddress(chAddr)
	if err != nil {
		return cid.Undef, err
	}

	mb, err := ch.messageBuilder(ctx, ci.Control)
	if err != nil {
		return cid.Undef, err
	}

	msg, err := mb.Collect(chAddr)
	if err != nil {
		return cid.Undef, err
	}

	smsg, err := ch.mpoolPush(ctx, msg)
	if err != nil {
		return cid.Undef, err
	}

	return smsg.Cid(), nil
}

func (ch *channel) getChannelInfo(addr address.Address) (*ChannelInfo, error) {
	ch.lk.Lock()
	defer ch.lk.Unlock()

	return ch.store.ByAddress(addr)
}

// AvailableFunds describes the current state of a channel on chain
type AvailableFunds struct {
	// Channel is the address of the channel
	Channel *address.Address
	// From is the from address of the channel (channel creator)
	From address.Address
	// To is the to address of the channel
	To address.Address
	// ConfirmedAmt is the amount of funds that have been confirmed on-chain
	// for the channel
	ConfirmedAmt filecoin.BigInt
	// PendingAmt is the amount of funds that are pending confirmation on-chain
	PendingAmt filecoin.BigInt
	// PendingWaitSentinel can be used with PaychGetWaitReady to wait for
	// confirmation of pending funds
	PendingWaitSentinel *cid.Cid
	// QueuedAmt is the amount that is queued up behind a pending request
	QueuedAmt filecoin.BigInt
	// VoucherRedeemedAmt is the amount that is redeemed by vouchers on-chain
	// and in the local datastore
	VoucherRedeemedAmt filecoin.BigInt
}

// VoucherCreateResult is the response to createVoucher method
type VoucherCreateResult struct {
	// Voucher that was created, or nil if there was an error or if there
	// were insufficient funds in the channel
	Voucher *paych.SignedVoucher
	// Shortfall is the additional amount that would be needed in the channel
	// in order to be able to create the voucher
	Shortfall filecoin.BigInt
}

// ChannelState is an abstract version of payment channel state that works across
// versions. TODO: we need to handle future actor version upgrades
type ChannelState interface {
	cbortypes.Marshaler
	// Channel owner, who has funded the actor
	From() (address.Address, error)
	// Recipient of payouts from channel
	To() (address.Address, error)

	// Height at which the channel can be `Collected`
	SettlingAt() (abi.ChainEpoch, error)

	// Amount successfully redeemed through the payment channel, paid out on `Collect()`
	ToSend() (abi.TokenAmount, error)

	// Get total number of lanes
	LaneCount() (uint64, error)

	// Iterate lane states
	ForEachLaneState(cb func(idx uint64, dl LaneState) error) error
}

// channelState struct is a model for interacting with the payment channel actor state
type channelState struct {
	paych.State
	store adt.Store
	lsAmt *adt.Array
}

// Channel owner, who has funded the actor
func (s *channelState) From() (address.Address, error) {
	return s.State.From, nil
}

// Recipient of payouts from channel
func (s *channelState) To() (address.Address, error) {
	return s.State.To, nil
}

// Height at which the channel can be `Collected`
func (s *channelState) SettlingAt() (abi.ChainEpoch, error) {
	return s.State.SettlingAt, nil
}

// Amount successfully redeemed through the payment channel, paid out on `Collect()`
func (s *channelState) ToSend() (abi.TokenAmount, error) {
	return s.State.ToSend, nil
}

func (s *channelState) getOrLoadLsAmt() (*adt.Array, error) {
	if s.lsAmt != nil {
		return s.lsAmt, nil
	}

	// Get the lane state from the chain
	lsamt, err := adt.AsArray(s.store, s.State.LaneStates, 3)
	if err != nil {
		return nil, err
	}

	s.lsAmt = lsamt
	return lsamt, nil
}

// Get total number of lanes
func (s *channelState) LaneCount() (uint64, error) {
	lsamt, err := s.getOrLoadLsAmt()
	if err != nil {
		return 0, err
	}
	return lsamt.Length(), nil
}

// Iterate lane states
func (s *channelState) ForEachLaneState(cb func(idx uint64, dl LaneState) error) error {
	// Get the lane state from the chain
	lsamt, err := s.getOrLoadLsAmt()
	if err != nil {
		return err
	}

	// Note: we use a map instead of an array to store laneStates because the
	// client sets the lane ID (the index) and potentially they could use a
	// very large index.
	var ls paych.LaneState
	return lsamt.ForEach(&ls, func(i int64) error {
		return cb(uint64(i), &laneState{ls})
	})
}

// LaneState is an abstract copy of the state of a single lane
// not to be mixed up with the original paych.LaneState struct
type LaneState interface {
	Redeemed() (big.Int, error)
	Nonce() (uint64, error)
}

type laneState struct {
	paych.LaneState
}

func (ls *laneState) Redeemed() (big.Int, error) {
	return ls.LaneState.Redeemed, nil
}

func (ls *laneState) Nonce() (uint64, error) {
	return ls.LaneState.Nonce, nil
}

// paychFundsRes is the response to a create channel or add funds request
type paychFundsRes struct {
	channel address.Address
	mcid    cid.Cid
	err     error
}

// fundsReq is a request to create a channel or add funds to a channel
type fundsReq struct {
	ctx     context.Context
	promise chan *paychFundsRes
	amt     filecoin.BigInt

	lk sync.Mutex
	// merge parent, if this req is part of a merge
	merge *mergedFundsReq
	// whether the req's context has been cancelled
	active bool
}

func newFundsReq(ctx context.Context, amt filecoin.BigInt) *fundsReq {
	promise := make(chan *paychFundsRes)
	return &fundsReq{
		ctx:     ctx,
		promise: promise,
		amt:     amt,
		active:  true,
	}
}

// onComplete is called when the funds request has been executed
func (r *fundsReq) onComplete(res *paychFundsRes) {
	select {
	case <-r.ctx.Done():
	case r.promise <- res:
	}
}

// cancel is called when the req's context is cancelled
func (r *fundsReq) cancel() {
	r.lk.Lock()

	r.active = false
	m := r.merge

	r.lk.Unlock()

	// If there's a merge parent, tell the merge parent to check if it has any
	// active reqs left
	if m != nil {
		m.checkActive()
	}
}

// isActive indicates whether the req's context has been cancelled
func (r *fundsReq) isActive() bool {
	r.lk.Lock()
	defer r.lk.Unlock()

	return r.active
}

// setMergeParent sets the merge that this req is part of
func (r *fundsReq) setMergeParent(m *mergedFundsReq) {
	r.lk.Lock()
	defer r.lk.Unlock()

	r.merge = m
}

// mergedFundsReq merges together multiple add funds requests that are queued
// up, so that only one message is sent for all the requests (instead of one
// message for each request)
type mergedFundsReq struct {
	ctx    context.Context
	cancel context.CancelFunc
	reqs   []*fundsReq
}

func newMergedFundsReq(reqs []*fundsReq) *mergedFundsReq {
	ctx, cancel := context.WithCancel(context.Background())
	m := &mergedFundsReq{
		ctx:    ctx,
		cancel: cancel,
		reqs:   reqs,
	}

	for _, r := range m.reqs {
		r.setMergeParent(m)
	}

	// If the requests were all cancelled while being added, cancel the context
	// immediately
	m.checkActive()

	return m
}

// Called when a fundsReq is cancelled
func (m *mergedFundsReq) checkActive() {
	// Check if there are any active fundsReqs
	for _, r := range m.reqs {
		if r.isActive() {
			return
		}
	}

	// If all fundsReqs have been cancelled, cancel the context
	m.cancel()
}

// onComplete is called when the queue has executed the mergeFundsReq.
// Calls onComplete on each fundsReq in the mergeFundsReq.
func (m *mergedFundsReq) onComplete(res *paychFundsRes) {
	for _, r := range m.reqs {
		if r.isActive() {
			r.onComplete(res)
		}
	}
}

// sum is the sum of the amounts in all requests in the merge
func (m *mergedFundsReq) sum() filecoin.BigInt {
	sum := filecoin.NewInt(0)
	for _, r := range m.reqs {
		if r.isActive() {
			sum = filecoin.BigAdd(sum, r.amt)
		}
	}
	return sum
}

type rwlock interface {
	RLock()
	RUnlock()
}

// multiLock manages locking for a specific channel.
// Some operations update the state of a single channel, and need to block
// other operations only on the same channel's state.
// Some operations update state that affects all channels, and need to block
// any operation against any channel.
type multiLock struct {
	globalLock rwlock
	chanLock   sync.Mutex
}

func (l *multiLock) Lock() {
	// Wait for other operations by this channel to finish.
	// Exclusive per-channel (no other ops by this channel allowed).
	l.chanLock.Lock()
	// Wait for operations affecting all channels to finish.
	// Allows ops by other channels in parallel, but blocks all operations
	// if global lock is taken exclusively (eg when adding a channel)
	l.globalLock.RLock()
}

func (l *multiLock) Unlock() {
	l.globalLock.RUnlock()
	l.chanLock.Unlock()
}

type msgListeners struct {
	ps *pubsub.PubSub
}

type msgCompleteEvt struct {
	mcid cid.Cid
	err  error
}

type subscriberFn func(msgCompleteEvt)

func newMsgListeners() msgListeners {
	ps := pubsub.New(func(event pubsub.Event, subFn pubsub.SubscriberFn) error {
		evt, ok := event.(msgCompleteEvt)
		if !ok {
			return errors.New("wrong type of event")
		}
		sub, ok := subFn.(subscriberFn)
		if !ok {
			return errors.New("wrong type of subscriber")
		}
		sub(evt)
		return nil
	})
	return msgListeners{ps: ps}
}

// onMsgComplete registers a callback for when the message with the given cid
// completes
func (ml *msgListeners) onMsgComplete(mcid cid.Cid, cb func(error)) pubsub.Unsubscribe {
	var fn subscriberFn = func(evt msgCompleteEvt) {
		if mcid.Equals(evt.mcid) {
			cb(evt.err)
		}
	}
	return ml.ps.Subscribe(fn)
}

// fireMsgComplete is called when a message completes
func (ml *msgListeners) fireMsgComplete(mcid cid.Cid, err error) {
	e := ml.ps.Publish(msgCompleteEvt{mcid: mcid, err: err})
	if e != nil {
		// In theory we shouldn't ever get an error here
		log.Error().Err(e).Msg("unexpected error publishing message complete")
	}
}

type insufficientFundsErr interface {
	Shortfall() filecoin.BigInt
}

// ErrInsufficientFunds indicates that there are not enough funds in the
// channel to create a voucher
type ErrInsufficientFunds struct {
	shortfall filecoin.BigInt
}

func newErrInsufficientFunds(shortfall filecoin.BigInt) *ErrInsufficientFunds {
	return &ErrInsufficientFunds{shortfall: shortfall}
}

func (e *ErrInsufficientFunds) Error() string {
	return fmt.Sprintf("not enough funds in channel to cover voucher - shortfall: %d", e.shortfall)
}

// Shortfall is how much FIL the channel is missing
func (e *ErrInsufficientFunds) Shortfall() filecoin.BigInt {
	return e.shortfall
}
