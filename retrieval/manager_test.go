package retrieval

import (
	"context"
	"testing"
	"time"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/myelnet/go-hop-exchange/testutil"
	"github.com/stretchr/testify/require"

	"github.com/myelnet/go-hop-exchange/retrieval/deal"
)

func TestRetrieval(t *testing.T) {
	bgCtx := context.Background()

	mn := mocknet.New(bgCtx)

	n1 := testutil.NewTestNode(mn, t)
	n2 := testutil.NewTestNode(mn, t)

	err := mn.LinkAll()
	require.NoError(t, err)

	n1.SetupDataTransfer(bgCtx, t)
	r1, err := New(n1.Ms, n1.Dt, n1.Ds, n1.Counter)
	require.NoError(t, err)

	n2.SetupDataTransfer(bgCtx, t)
	_, err = New(n2.Ms, n2.Dt, n2.Ds, n2.Counter)

	// n1 is our client and is retrieving a file n2 has so we add it first
	link, origBytes := n2.LoadUnixFSFileToStore(bgCtx, t, "/retrieval/readme.md")
	rootCid := link.(cidlink.Link).Cid

	clientAddr, err := address.NewIDAddress(uint64(10))
	require.NoError(t, err)
	providerAddr, err := address.NewIDAddress(uint64(99))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(bgCtx, 10*time.Second)
	defer cancel()

	clientStoreID := n1.Ms.Next()
	pricePerByte := abi.NewTokenAmount(1000)
	paymentInterval := uint64(10000)
	paymentIntervalIncrease := uint64(1000)
	unsealPrice := big.Zero()
	params, err := deal.NewParams(pricePerByte, paymentInterval, paymentIntervalIncrease, AllSelector(), nil, unsealPrice)
	require.NoError(t, err)

	expectedTotal := big.Mul(pricePerByte, abi.NewTokenAmount(int64(len(origBytes))))

	did, err := r1.Retrieve(ctx, rootCid, params, expectedTotal, n2.Host.ID(), clientAddr, providerAddr, &clientStoreID)
	require.NoError(t, err)
	require.Equal(t, did, deal.ID(0))

	// n1.VerifyFileTransferred(bgCtx, t, rootCid, origBytes)
}
