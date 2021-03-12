package storage

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/specs-actors/v3/actors/builtin"
	"github.com/filecoin-project/specs-actors/v3/actors/builtin/market"
	tutils "github.com/filecoin-project/specs-actors/v3/support/testing"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	dss "github.com/ipfs/go-datastore/sync"
	keystore "github.com/ipfs/go-ipfs-keystore"
	fil "github.com/myelnet/go-hop-exchange/filecoin"
	"github.com/myelnet/go-hop-exchange/wallet"
	"github.com/stretchr/testify/require"
)

// Adding the basic test from lotus for sanity

// TestFundManagerBasic verifies that the basic fund manager operations work
func TestFundManagerBasic(t *testing.T) {
	s := setup(t)
	defer s.fm.Stop()

	// Reserve 10
	// balance:  0 -> 10
	// reserved: 0 -> 10
	amt := abi.NewTokenAmount(10)
	sentinel, err := s.fm.Reserve(s.ctx, s.walletAddr, s.acctAddr, amt)
	require.NoError(t, err)

	msg := s.mockApi.getSentMessage(sentinel)
	checkAddMessageFields(t, msg, s.walletAddr, s.acctAddr, amt)

	s.mockApi.completeMsg(sentinel)

	// Reserve 7
	// balance:  10 -> 17
	// reserved: 10 -> 17
	amt = abi.NewTokenAmount(7)
	sentinel, err = s.fm.Reserve(s.ctx, s.walletAddr, s.acctAddr, amt)
	require.NoError(t, err)

	msg = s.mockApi.getSentMessage(sentinel)
	checkAddMessageFields(t, msg, s.walletAddr, s.acctAddr, amt)

	s.mockApi.completeMsg(sentinel)

	// Release 5
	// balance:  17
	// reserved: 17 -> 12
	amt = abi.NewTokenAmount(5)
	err = s.fm.Release(s.acctAddr, amt)
	require.NoError(t, err)

	// Withdraw 2
	// balance:  17 -> 15
	// reserved: 12
	amt = abi.NewTokenAmount(2)
	sentinel, err = s.fm.Withdraw(s.ctx, s.walletAddr, s.acctAddr, amt)
	require.NoError(t, err)

	msg = s.mockApi.getSentMessage(sentinel)
	checkWithdrawMessageFields(t, msg, s.walletAddr, s.acctAddr, amt)

	s.mockApi.completeMsg(sentinel)

	// Reserve 3
	// balance:  15
	// reserved: 12 -> 15
	// Note: reserved (15) is <= balance (15) so should not send on-chain
	// message
	msgCount := s.mockApi.messageCount()
	amt = abi.NewTokenAmount(3)
	sentinel, err = s.fm.Reserve(s.ctx, s.walletAddr, s.acctAddr, amt)
	require.NoError(t, err)
	require.Equal(t, msgCount, s.mockApi.messageCount())
	require.Equal(t, sentinel, cid.Undef)

	// Reserve 1
	// balance:  15 -> 16
	// reserved: 15 -> 16
	// Note: reserved (16) is above balance (15) so *should* send on-chain
	// message to top up balance
	amt = abi.NewTokenAmount(1)
	topUp := abi.NewTokenAmount(1)
	sentinel, err = s.fm.Reserve(s.ctx, s.walletAddr, s.acctAddr, amt)
	require.NoError(t, err)

	s.mockApi.completeMsg(sentinel)
	msg = s.mockApi.getSentMessage(sentinel)
	checkAddMessageFields(t, msg, s.walletAddr, s.acctAddr, topUp)

	// Withdraw 1
	// balance:  16
	// reserved: 16
	// Note: Expect failure because there is no available balance to withdraw:
	// balance - reserved = 16 - 16 = 0
	amt = abi.NewTokenAmount(1)
	sentinel, err = s.fm.Withdraw(s.ctx, s.walletAddr, s.acctAddr, amt)
	require.Error(t, err)
}

type scaffold struct {
	ctx        context.Context
	ds         datastore.Batching
	wllt       wallet.Driver
	walletAddr address.Address
	acctAddr   address.Address
	mockApi    *mockFundManagerAPI
	fm         *FundManager
}

func setup(t *testing.T) *scaffold {
	ctx, cancel := context.WithCancel(context.Background())

	w := wallet.NewIPFS(keystore.NewMemKeystore(), nil)

	walletAddr, err := w.NewKey(context.Background(), wallet.KTSecp256k1)
	if err != nil {
		t.Fatal(err)
	}

	acctAddr := tutils.NewActorAddr(t, "addr")

	mockAPI := newMockFundManagerAPI(walletAddr)
	dstore := dss.MutexWrap(datastore.NewMapDatastore())

	fm := &FundManager{
		ctx:         ctx,
		shutdown:    cancel,
		api:         mockAPI,
		str:         newStore(dstore),
		fundedAddrs: make(map[address.Address]*fundedAddress),
	}
	return &scaffold{
		ctx:        ctx,
		ds:         dstore,
		wllt:       w,
		walletAddr: walletAddr,
		acctAddr:   acctAddr,
		mockApi:    mockAPI,
		fm:         fm,
	}
}

func checkAddMessageFields(t *testing.T, msg *fil.Message, from address.Address, to address.Address, amt abi.TokenAmount) {
	require.Equal(t, from, msg.From)
	require.Equal(t, builtin.StorageMarketActorAddr, msg.To)
	require.Equal(t, amt, msg.Value)

	var paramsTo address.Address
	err := paramsTo.UnmarshalCBOR(bytes.NewReader(msg.Params))
	require.NoError(t, err)
	require.Equal(t, to, paramsTo)
}

func checkWithdrawMessageFields(t *testing.T, msg *fil.Message, from address.Address, addr address.Address, amt abi.TokenAmount) {
	require.Equal(t, from, msg.From)
	require.Equal(t, builtin.StorageMarketActorAddr, msg.To)
	require.Equal(t, abi.NewTokenAmount(0), msg.Value)

	var params market.WithdrawBalanceParams
	err := params.UnmarshalCBOR(bytes.NewReader(msg.Params))
	require.NoError(t, err)
	require.Equal(t, addr, params.ProviderOrClientAddress)
	require.Equal(t, amt, params.Amount)
}

type sentMsg struct {
	msg   *fil.SignedMessage
	ready chan struct{}
}

type mockFundManagerAPI struct {
	wallet address.Address

	lk            sync.Mutex
	escrow        map[address.Address]abi.TokenAmount
	sentMsgs      map[cid.Cid]*sentMsg
	completedMsgs map[cid.Cid]struct{}
	waitingFor    map[cid.Cid]chan struct{}
}

func newMockFundManagerAPI(wallet address.Address) *mockFundManagerAPI {
	return &mockFundManagerAPI{
		wallet:        wallet,
		escrow:        make(map[address.Address]abi.TokenAmount),
		sentMsgs:      make(map[cid.Cid]*sentMsg),
		completedMsgs: make(map[cid.Cid]struct{}),
		waitingFor:    make(map[cid.Cid]chan struct{}),
	}
}

func (mapi *mockFundManagerAPI) MpoolPushMessage(ctx context.Context, message *fil.Message, spec *MessageSendSpec) (*fil.SignedMessage, error) {
	mapi.lk.Lock()
	defer mapi.lk.Unlock()

	smsg := &fil.SignedMessage{Message: *message}
	mapi.sentMsgs[smsg.Cid()] = &sentMsg{msg: smsg, ready: make(chan struct{})}

	return smsg, nil
}

func (mapi *mockFundManagerAPI) getSentMessage(c cid.Cid) *fil.Message {
	mapi.lk.Lock()
	defer mapi.lk.Unlock()

	for i := 0; i < 1000; i++ {
		if pending, ok := mapi.sentMsgs[c]; ok {
			return &pending.msg.Message
		}
		time.Sleep(time.Millisecond)
	}
	panic("expected message to be sent")
}

func (mapi *mockFundManagerAPI) messageCount() int {
	mapi.lk.Lock()
	defer mapi.lk.Unlock()

	return len(mapi.sentMsgs)
}

func (mapi *mockFundManagerAPI) completeMsg(msgCid cid.Cid) {
	mapi.lk.Lock()

	pmsg, ok := mapi.sentMsgs[msgCid]
	if ok {
		if pmsg.msg.Message.Method == builtin.MethodsMarket.AddBalance {
			var escrowAcct address.Address
			err := escrowAcct.UnmarshalCBOR(bytes.NewReader(pmsg.msg.Message.Params))
			if err != nil {
				panic(err)
			}

			escrow := mapi.getEscrow(escrowAcct)
			before := escrow
			escrow = fil.BigAdd(escrow, pmsg.msg.Message.Value)
			mapi.escrow[escrowAcct] = escrow
			fmt.Printf("%s:   escrow %d -> %d\n", escrowAcct, before, escrow)
		} else {
			var params market.WithdrawBalanceParams
			err := params.UnmarshalCBOR(bytes.NewReader(pmsg.msg.Message.Params))
			if err != nil {
				panic(err)
			}
			escrowAcct := params.ProviderOrClientAddress

			escrow := mapi.getEscrow(escrowAcct)
			before := escrow
			escrow = fil.BigSub(escrow, params.Amount)
			mapi.escrow[escrowAcct] = escrow
			fmt.Printf("%s:   escrow %d -> %d\n", escrowAcct, before, escrow)
		}
	}

	mapi.completedMsgs[msgCid] = struct{}{}

	ready, ok := mapi.waitingFor[msgCid]

	mapi.lk.Unlock()

	if ok {
		close(ready)
	}
}

func (mapi *mockFundManagerAPI) StateMarketBalance(ctx context.Context, a address.Address, key fil.TipSetKey) (fil.MarketBalance, error) {
	mapi.lk.Lock()
	defer mapi.lk.Unlock()

	return fil.MarketBalance{
		Locked: abi.NewTokenAmount(0),
		Escrow: mapi.getEscrow(a),
	}, nil
}

func (mapi *mockFundManagerAPI) getEscrow(a address.Address) abi.TokenAmount {
	escrow := mapi.escrow[a]
	if escrow.Nil() {
		return abi.NewTokenAmount(0)
	}
	return escrow
}

func (mapi *mockFundManagerAPI) publish(addr address.Address, amt abi.TokenAmount) {
	mapi.lk.Lock()
	defer mapi.lk.Unlock()

	escrow := mapi.escrow[addr]
	if escrow.Nil() {
		return
	}
	escrow = fil.BigSub(escrow, amt)
	if escrow.LessThan(abi.NewTokenAmount(0)) {
		escrow = abi.NewTokenAmount(0)
	}
	mapi.escrow[addr] = escrow
}

func (mapi *mockFundManagerAPI) StateWaitMsg(ctx context.Context, c cid.Cid, confidence uint64) (*fil.MsgLookup, error) {
	res := &fil.MsgLookup{
		Message: c,
		Receipt: fil.MessageReceipt{
			ExitCode: 0,
			Return:   nil,
		},
	}
	ready := make(chan struct{})

	mapi.lk.Lock()
	_, ok := mapi.completedMsgs[c]
	if !ok {
		mapi.waitingFor[c] = ready
	}
	mapi.lk.Unlock()

	if !ok {
		select {
		case <-ctx.Done():
		case <-ready:
		}
	}
	return res, nil
}
