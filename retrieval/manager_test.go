package retrieval

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"testing"
	"time"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/specs-actors/v3/actors/builtin/paych"
	tutils "github.com/filecoin-project/specs-actors/v3/support/testing"
	"github.com/ipfs/go-cid"
	blocksutil "github.com/ipfs/go-ipfs-blocksutil"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/myelnet/go-hop-exchange/filecoin"
	"github.com/myelnet/go-hop-exchange/payments"
	"github.com/myelnet/go-hop-exchange/testutil"
	"github.com/stretchr/testify/require"

	"github.com/myelnet/go-hop-exchange/retrieval/client"
	"github.com/myelnet/go-hop-exchange/retrieval/deal"
	"github.com/myelnet/go-hop-exchange/retrieval/provider"
)

var blockGen = blocksutil.NewBlockGenerator()

type mockPayments struct {
	chResponse *payments.ChannelResponse
	chAddr     address.Address
}

func (p *mockPayments) GetChannel(ctx context.Context, from, to address.Address, amt filecoin.BigInt) (*payments.ChannelResponse, error) {
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
	// sig := &crypto.Signature{Type: crypto.SigTypeBLS, Data: []byte("doesn't matter")}
	vouch := &paych.SignedVoucher{
		ChannelAddr: addr,
		TimeLockMin: abi.ChainEpoch(1),
		TimeLockMax: abi.ChainEpoch(0),
		Lane:        lane,
		Nonce:       0,
		Amount:      amt,
		// Signature:      sig,
	}
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
	return expectedAmount, nil
}

func TestRetrieval(t *testing.T) {

	testCases := []struct {
		name string
	}{
		{name: "Basic transfer"},
	}
	for i, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			bgCtx := context.Background()

			mn := mocknet.New(bgCtx)

			n1 := testutil.NewTestNode(mn, t)
			n2 := testutil.NewTestNode(mn, t)

			err := mn.LinkAll()
			require.NoError(t, err)

			dTTmpDir, err := ioutil.TempDir("", "dt-tmp")
			require.NoError(t, err)
			t.Cleanup(func() {
				_ = os.RemoveAll(dTTmpDir)
			})

			n1.SetupDataTransfer(bgCtx, t)
			chResponse := &payments.ChannelResponse{
				Channel:      address.Undef,
				WaitSentinel: blockGen.Next().Cid(),
			}
			chAddr := tutils.NewIDAddr(t, uint64(i*10))

			pay1 := &mockPayments{
				chResponse: chResponse,
				chAddr:     chAddr,
			}
			r1, err := New(bgCtx, n1.Ms, n1.Ds, n1.Counter, pay1, n1.Dt, n1.Host.ID())
			require.NoError(t, err)

			n2.SetupDataTransfer(bgCtx, t)
			pay2 := &mockPayments{}
			r2, err := New(bgCtx, n2.Ms, n2.Ds, n2.Counter, pay2, n2.Dt, n2.Host.ID())
			require.NoError(t, err)

			// n1 is our client and is retrieving a file n2 has so we add it first
			link, origBytes := n2.LoadUnixFSFileToStore(bgCtx, t, "/retrieval/readme.md")
			rootCid := link.(cidlink.Link).Cid

			clientAddr, err := address.NewIDAddress(uint64(10))
			require.NoError(t, err)
			providerAddr, err := address.NewIDAddress(uint64(99))
			require.NoError(t, err)

			ctx, cancel := context.WithTimeout(bgCtx, 10*time.Second)
			defer cancel()

			r2.Provider().SubscribeToEvents(func(event provider.Event, state deal.ProviderState) {
				fmt.Println("Provider:", deal.Statuses[state.Status])
			})

			clientDealStateChan := make(chan deal.ClientState)
			r1.Client().SubscribeToEvents(func(event client.Event, state deal.ClientState) {
				fmt.Println("Client:", deal.Statuses[state.Status])
				switch state.Status {
				case deal.StatusCompleted, deal.StatusCancelled, deal.StatusErrored:
					clientDealStateChan <- state
					return
				}
			})

			clientStoreID := n1.Ms.Next()
			pricePerByte := abi.NewTokenAmount(1000)
			paymentInterval := uint64(10000)
			paymentIntervalIncrease := uint64(1000)
			unsealPrice := big.Zero()
			params, err := deal.NewParams(pricePerByte, paymentInterval, paymentIntervalIncrease, AllSelector(), nil, unsealPrice)
			require.NoError(t, err)

			expectedTotal := big.Mul(pricePerByte, abi.NewTokenAmount(int64(len(origBytes))))

			did, err := r1.Client().Retrieve(ctx, rootCid, params, expectedTotal, n2.Host.ID(), clientAddr, providerAddr, &clientStoreID)
			require.NoError(t, err)
			require.Equal(t, did, deal.ID(0))

			select {
			case <-ctx.Done():
				t.Fatal("deal failed to complete")
			case clientDealState := <-clientDealStateChan:
				require.Equal(t, deal.StatusCompleted, clientDealState.Status)
			}

			n1.VerifyFileTransferred(bgCtx, t, rootCid, origBytes)
		})
	}
}
