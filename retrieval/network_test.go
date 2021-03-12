package retrieval

import (
	"context"
	"testing"

	"github.com/filecoin-project/go-address"
	cid "github.com/ipfs/go-cid"
	blocksutil "github.com/ipfs/go-ipfs-blocksutil"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/myelnet/pop/retrieval/deal"
	"github.com/myelnet/pop/testutil"
	"github.com/stretchr/testify/require"
)

var blockGenerator = blocksutil.NewBlockGenerator()

type testhandler struct {
	net  QueryNetwork
	root cid.Cid
	t    *testing.T
}

func (h *testhandler) HandleQueryStream(stream QueryStream) {
	defer stream.Close()

	query, err := stream.ReadQuery()
	require.NoError(h.t, err)
	require.Equal(h.t, h.root, query.PayloadCID)

	addr, _ := address.NewIDAddress(uint64(10))

	answer := deal.QueryResponse{
		Status:                     deal.QueryResponseAvailable,
		Size:                       1600,
		PaymentAddress:             addr,
		MinPricePerByte:            deal.DefaultPricePerByte,
		MaxPaymentInterval:         deal.DefaultPaymentInterval,
		MaxPaymentIntervalIncrease: deal.DefaultPaymentIntervalIncrease,
	}
	err = stream.WriteQueryResponse(answer)
	require.NoError(h.t, err)
}

func TestNetwork(t *testing.T) {
	bgCtx := context.Background()

	mn := mocknet.New(bgCtx)

	root := blockGenerator.Next().Cid()

	cnode := testutil.NewTestNode(mn, t)
	pnode := testutil.NewTestNode(mn, t)

	cnet := NewQueryNetwork(cnode.Host)
	pnet := NewQueryNetwork(pnode.Host)

	phandler := &testhandler{pnet, root, t}
	pnet.SetDelegate(phandler)

	require.NoError(t, mn.LinkAll())

	require.NoError(t, mn.ConnectAllButSelf())

	stream, err := cnet.NewQueryStream(pnode.Host.ID())
	require.NoError(t, err)
	defer stream.Close()

	err = stream.WriteQuery(deal.Query{
		PayloadCID:  root,
		QueryParams: deal.QueryParams{},
	})
	require.NoError(t, err)

	res, err := stream.ReadQueryResponse()
	require.NoError(t, err)

	require.Equal(t, res.Status, deal.QueryResponseAvailable)
}

/****
 * Useful for debugging when communicating with go-fil-markets impl

type testRetHandler struct {
	net  retnet.RetrievalMarketNetwork
	root cid.Cid
	t    *testing.T
}

func (h *testRetHandler) HandleQueryStream(stream retnet.RetrievalQueryStream) {
	defer stream.Close()
	query, err := stream.ReadQuery()
	require.NoError(h.t, err)
	require.Equal(h.t, query.PayloadCID, h.root)

	addr, _ := address.NewIDAddress(uint64(10))

	answer := retrievalmarket.QueryResponse{
		Status:                     retrievalmarket.QueryResponseAvailable,
		Size:                       1600,
		PaymentAddress:             addr,
		PieceCIDFound:              retrievalmarket.QueryItemUnavailable,
		MinPricePerByte:            retrievalmarket.DefaultPricePerByte,
		MaxPaymentInterval:         retrievalmarket.DefaultPaymentInterval,
		MaxPaymentIntervalIncrease: retrievalmarket.DefaultPaymentIntervalIncrease,
		UnsealPrice:                retrievalmarket.DefaultUnsealPrice,
	}
	err = stream.WriteQueryResponse(answer)
	require.NoError(h.t, err)
}

func TestNetworkWithRetNet(t *testing.T) {
	bgCtx := context.Background()

	mn := mocknet.New(bgCtx)

	root := blockGenerator.Next().Cid()

	cnode := testutil.NewTestNode(mn, t)
	pnode := testutil.NewTestNode(mn, t)

	cnet := NewFromLibp2pHost(cnode.Host)
	pnet := retnet.NewFromLibp2pHost(pnode.Host)

	phandler := &testRetHandler{pnet, root, t}
	pnet.SetDelegate(phandler)

	require.NoError(t, mn.LinkAll())

	require.NoError(t, mn.ConnectAllButSelf())

	stream, err := cnet.NewQueryStream(pnode.Host.ID())
	require.NoError(t, err)
	defer stream.Close()

	err = stream.WriteQuery(Query{
		PayloadCID:  root,
		QueryParams: QueryParams{},
	})
	require.NoError(t, err)

	res, err := stream.ReadQueryResponse()
	require.NoError(t, err)

	require.Equal(t, res.Status, QueryResponseAvailable)
}

*/
