package retrieval

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-multistore"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/specs-actors/v3/actors/builtin/paych"
	tutils "github.com/filecoin-project/specs-actors/v3/support/testing"
	"github.com/ipfs/go-cid"
	blocksutil "github.com/ipfs/go-ipfs-blocksutil"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/stretchr/testify/require"

	"github.com/myelnet/pop/filecoin"
	"github.com/myelnet/pop/internal/testutil"
	"github.com/myelnet/pop/internal/utils"
	"github.com/myelnet/pop/payments"
	"github.com/myelnet/pop/retrieval/client"
	"github.com/myelnet/pop/retrieval/deal"
	"github.com/myelnet/pop/retrieval/provider"
	"github.com/myelnet/pop/selectors"
)

var blockGen = blocksutil.NewBlockGenerator()

type mockPayments struct {
	chResponse *payments.ChannelResponse
	chAddr     address.Address
	chFunds    *payments.AvailableFunds
	lk         sync.Mutex
}

func (p *mockPayments) GetChannel(ctx context.Context, from, to address.Address, amt filecoin.BigInt) (*payments.ChannelResponse, error) {
	p.lk.Lock()
	defer p.lk.Unlock()
	p.chFunds.ConfirmedAmt = big.Add(p.chFunds.ConfirmedAmt, amt)
	return p.chResponse, nil
}

func (p *mockPayments) WaitForChannel(ctx context.Context, id cid.Cid) (address.Address, error) {
	return p.chAddr, nil
}

func (p *mockPayments) ListChannels() ([]address.Address, error) {
	return nil, nil
}

func (p *mockPayments) GetChannelInfo(addr address.Address) (*payments.ChannelInfo, error) {
	return nil, nil
}

func (p *mockPayments) CreateVoucher(ctx context.Context, addr address.Address, amt filecoin.BigInt, lane uint64) (*payments.VoucherCreateResult, error) {
	if amt.GreaterThan(p.chFunds.ConfirmedAmt) {
		return &payments.VoucherCreateResult{
			Shortfall: big.Sub(amt, p.chFunds.ConfirmedAmt),
		}, nil
	}
	// sig := &crypto.Signature{Type: crypto.SigTypeBLS, Data: []byte("doesn't matter")}
	vouch := &paych.SignedVoucher{
		ChannelAddr: addr,
		TimeLockMin: abi.ChainEpoch(1),
		TimeLockMax: abi.ChainEpoch(0),
		Lane:        lane,
		Nonce:       0,
		Amount:      big.Sub(amt, p.chFunds.VoucherReedeemedAmt),
		// Signature:      sig,
	}
	p.chFunds.VoucherReedeemedAmt = big.Add(p.chFunds.VoucherReedeemedAmt, vouch.Amount)
	vouchRes := &payments.VoucherCreateResult{
		Voucher:   vouch,
		Shortfall: filecoin.NewInt(0),
	}
	return vouchRes, nil
}

func (p *mockPayments) AllocateLane(ctx context.Context, add address.Address) (uint64, error) {
	return 0, nil
}

func (p *mockPayments) AddVoucherInbound(ctx context.Context, addr address.Address, vouch *paych.SignedVoucher, prrof []byte, expectedAmount filecoin.BigInt) (filecoin.BigInt, error) {
	return vouch.Amount, nil
}

func (p *mockPayments) ChannelAvailableFunds(chAddr address.Address) (*payments.AvailableFunds, error) {
	return p.chFunds, nil
}

func (p *mockPayments) SetChannelAvailableFunds(funds payments.AvailableFunds) {
	p.lk.Lock()
	defer p.lk.Unlock()
	chFunds := addZeroesToAvailableFunds(funds)
	p.chFunds = &chFunds
}

func (p *mockPayments) Settle(ctx context.Context, addr address.Address) error {
	return nil
}

func (p *mockPayments) StartAutoCollect(ctx context.Context) error {
	return nil
}

type mockStoreIDGetter struct {
	id  multistore.StoreID
	err error
}

func (m *mockStoreIDGetter) GetStoreID(c cid.Cid) (multistore.StoreID, error) {
	return m.id, m.err
}

func TestRetrieval(t *testing.T) {

	testCases := []struct {
		name            string
		addFunds        bool
		chFunds         payments.AvailableFunds
		filesize        int
		paymentInterval uint64
		free            bool
		failValidation  bool
	}{
		{
			name:            "Single block",
			filesize:        512,
			paymentInterval: uint64(1000),
		},
		{
			name:            "Multi blocks",
			filesize:        2048,
			paymentInterval: uint64(1000),
		},
		{
			name:            "Basic transfer",
			filesize:        256000,
			paymentInterval: uint64(10000),
		},
		{
			name:            "Existing channel",
			addFunds:        true,
			filesize:        125000,
			paymentInterval: uint64(10000),
		},
		{
			name: "Shortfall single block",
			chFunds: payments.AvailableFunds{
				ConfirmedAmt: abi.NewTokenAmount(-1851200),
			},
			filesize:        512,
			paymentInterval: uint64(1000),
		},
		{
			name: "Shortfall multi blocks",
			chFunds: payments.AvailableFunds{
				ConfirmedAmt: abi.NewTokenAmount(-7400000),
			},
			filesize:        2048,
			paymentInterval: uint64(1000),
		},
		{
			name: "Shortfall many blocks",
			chFunds: payments.AvailableFunds{
				ConfirmedAmt: abi.NewTokenAmount(-5864900),
			},
			filesize:        56000,
			paymentInterval: uint64(10000),
		},
		{
			name:            "Free transfer",
			free:            true,
			filesize:        256000,
			paymentInterval: uint64(10000),
		},
		{
			name:            "Validation error",
			failValidation:  true,
			filesize:        256000,
			paymentInterval: uint64(10000),
		},
	}
	for i, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			bgCtx := context.Background()

			mn := mocknet.New(bgCtx)

			n1 := testutil.NewTestNode(mn, t)
			n2 := testutil.NewTestNode(mn, t)

			err := mn.LinkAll()
			require.NoError(t, err)

			n1.SetupDataTransfer(bgCtx, t)
			chAddr := tutils.NewIDAddr(t, uint64(i*10))
			// If we are creating a new channel the response will return an undefined address
			// while we wait for the create operation to be confirmed
			chResAddr := address.Undef
			if testCase.addFunds {
				chResAddr = chAddr
			}
			chResponse := &payments.ChannelResponse{
				Channel:      chResAddr,
				WaitSentinel: blockGen.Next().Cid(),
			}

			chFunds := addZeroesToAvailableFunds(testCase.chFunds)

			pay1 := &mockPayments{
				chResponse: chResponse,
				chAddr:     chAddr,
				chFunds:    &chFunds,
			}
			// this is only needed on the provider side to find where content is stored
			sidg1 := &mockStoreIDGetter{}
			r1, err := New(bgCtx, n1.Ms, n1.Ds, pay1, n1.Dt, sidg1, n1.Host.ID())
			require.NoError(t, err)

			fname := n2.CreateRandomFile(t, testCase.filesize)
			// n1 is our client and is retrieving a file n2 has so we add it first
			link, storeID, origBytes := n2.LoadFileToNewStore(bgCtx, t, fname)
			rootCid := link.(cidlink.Link).Cid
			providerStore, err := n2.Ms.Get(storeID)
			require.NoError(t, err)

			n2.SetupDataTransfer(bgCtx, t)
			pay2 := &mockPayments{}
			sidg2 := &mockStoreIDGetter{id: storeID}
			r2, err := New(bgCtx, n2.Ms, n2.Ds, pay2, n2.Dt, sidg2, n2.Host.ID())
			require.NoError(t, err)

			clientAddr, err := address.NewIDAddress(uint64(10))
			require.NoError(t, err)
			providerAddr, err := address.NewIDAddress(uint64(99))
			require.NoError(t, err)

			ctx, cancel := context.WithTimeout(bgCtx, 1000*time.Second)
			defer cancel()

			r2.Provider().SubscribeToEvents(func(event provider.Event, state deal.ProviderState) {
				fmt.Printf(`
PROVIDER:
event: %s
status: %s
totalSent: %d
fundsReceived: %s
`, provider.Events[event], deal.Statuses[state.Status], state.TotalSent, state.FundsReceived)
			})

			clientDealStateChan := make(chan deal.ClientState)
			r1.Client().SubscribeToEvents(func(event client.Event, state deal.ClientState) {
				fmt.Printf(`
CLIENT:
event: %s
status: %s
totalReceived: %d
bytesPaidFor: %d
totalFunds: %s
`, client.Events[event], deal.Statuses[state.Status], state.TotalReceived, state.BytesPaidFor, state.TotalFunds.String())
				switch state.Status {
				case deal.StatusCompleted, deal.StatusCancelled, deal.StatusErrored, deal.StatusRejected:
					clientDealStateChan <- state
					return
				case deal.StatusInsufficientFunds:
					// Simulate reaprovisioning the payment channel
					pay1.SetChannelAvailableFunds(payments.AvailableFunds{
						ConfirmedAmt:        big.Add(pay1.chFunds.ConfirmedAmt, state.VoucherShortfall),
						VoucherReedeemedAmt: pay1.chFunds.VoucherReedeemedAmt,
					})
					// Need to wait a bit for status to update in state machine
					time.Sleep(10 * time.Millisecond)
					err := r1.Client().TryRestartInsufficientFunds(state.PaymentInfo.PayCh)
					require.NoError(t, err)
					return
				}
			})

			stat, err := utils.Stat(ctx, providerStore, rootCid, selectors.All())
			require.NoError(t, err)
			clientStoreID := n1.Ms.Next()
			pricePerByte := abi.NewTokenAmount(100)
			if testCase.free {
				pricePerByte = big.Zero()
			}
			paymentIntervalIncrease := uint64(1000)
			unsealPrice := big.Zero()
			params, err := deal.NewParams(pricePerByte, testCase.paymentInterval, paymentIntervalIncrease, selectors.All(), nil, unsealPrice)
			require.NoError(t, err)
			ask := deal.QueryResponse{
				MinPricePerByte:            pricePerByte,
				MaxPaymentInterval:         testCase.paymentInterval,
				MaxPaymentIntervalIncrease: paymentIntervalIncrease,
			}
			// The client is trying to retrieve at lower price than agreed upon in the query/response agreement
			if testCase.failValidation {
				ask.MinPricePerByte = big.Add(pricePerByte, abi.NewTokenAmount(int64(20)))
			}
			// We need to set the ask first
			r2.Provider().SetAsk(rootCid, ask)

			// We offset it a bit since it's usually higher with ipld encoding
			expectedTotal := big.Mul(pricePerByte, abi.NewTokenAmount(int64(stat.Size)))
			if testCase.free {
				expectedTotal = big.Zero()
			}

			did, err := r1.Client().Retrieve(ctx, rootCid, params, expectedTotal, n2.Host.ID(), clientAddr, providerAddr, &clientStoreID)
			require.NoError(t, err)
			require.NotEqual(t, did, deal.ID(0))

			select {
			case <-ctx.Done():
				t.Fatal("deal failed to complete")
			case clientDealState := <-clientDealStateChan:
				if testCase.failValidation {
					require.Equal(t, deal.StatusRejected, clientDealState.Status)
					return
				}
				require.Equal(t, deal.StatusCompleted, clientDealState.Status)
			}

			store, err := n1.Ms.Get(clientStoreID)
			require.NoError(t, err)
			n1.VerifyFileTransferred(bgCtx, t, store.DAG, rootCid, origBytes)

			// Check if the file is still intact in the provider blockstore
			pstore, err := n2.Ms.Get(storeID)
			require.NoError(t, err)
			n2.VerifyFileTransferred(bgCtx, t, pstore.DAG, rootCid, origBytes)
		})
	}
}

func addZeroesToAvailableFunds(channelAvailableFunds payments.AvailableFunds) payments.AvailableFunds {
	if channelAvailableFunds.ConfirmedAmt.Nil() {
		channelAvailableFunds.ConfirmedAmt = big.Zero()
	}
	if channelAvailableFunds.PendingAmt.Nil() {
		channelAvailableFunds.PendingAmt = big.Zero()
	}
	if channelAvailableFunds.QueuedAmt.Nil() {
		channelAvailableFunds.QueuedAmt = big.Zero()
	}
	if channelAvailableFunds.VoucherReedeemedAmt.Nil() {
		channelAvailableFunds.VoucherReedeemedAmt = big.Zero()
	}
	return channelAvailableFunds
}
