package retrieval

import (
	"context"
	"testing"
	"time"

	"github.com/filecoin-project/go-address"
	peer "github.com/libp2p/go-libp2p-peer"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/myelnet/pop/internal/testutil"
	"github.com/myelnet/pop/retrieval/deal"
	"github.com/stretchr/testify/require"
)

type receiver struct {
	offers      chan deal.Offer
	isRecipient bool
	recipient   peer.ID
}

// Receive sends a new offer to the queue
func (r receiver) Receive(p peer.ID, res deal.QueryResponse) {
	r.offers <- deal.Offer{
		PeerID:   p,
		Response: res,
	}
}

func (r receiver) Recipient(id string) (peer.ID, error) {
	return r.recipient, nil
}

func (r receiver) IsRecipient(id string) bool {
	return r.isRecipient
}

func TestNetwork(t *testing.T) {
	bgCtx := context.Background()
	ctx, cancel := context.WithTimeout(bgCtx, 4*time.Second)
	defer cancel()

	mn := mocknet.New(bgCtx)

	cnode := testutil.NewTestNode(mn, t)
	cnet := NewQueryNetwork(cnode.Host)
	r := receiver{make(chan deal.Offer), true, ""}
	cnet.Start(r)

	pnodes := make(map[peer.ID]*testutil.TestNode)
	pnets := make(map[peer.ID]*Libp2pQueryNetwork)

	for i := 0; i < 11; i++ {
		pnode := testutil.NewTestNode(mn, t)
		pnet := NewQueryNetwork(pnode.Host)
		pnodes[pnode.Host.ID()] = pnode
		pnets[pnode.Host.ID()] = pnet
	}

	require.NoError(t, mn.LinkAll())

	require.NoError(t, mn.ConnectAllButSelf())

	// Simulating a bunch of nodes sending responses at the same time
	for _, net := range pnets {
		net := net
		go func() {
			stream, err := net.NewQueryStream(cnode.Host.ID())
			require.NoError(t, err)
			defer stream.Close()

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
			require.NoError(t, err)
		}()
	}

	for i := 0; i < 11; i++ {
		select {
		case of := <-r.offers:
			require.Equal(t, of.Response.Size, uint64(1600))
		case <-ctx.Done():
			require.NoError(t, ctx.Err())
		}
	}
}

func TestNetworkForwarding(t *testing.T) {
	bgCtx := context.Background()
	ctx, cancel := context.WithTimeout(bgCtx, 4*time.Second)
	defer cancel()

	mn := mocknet.New(bgCtx)

	cnode := testutil.NewTestNode(mn, t)
	cnet := NewQueryNetwork(cnode.Host)
	r := receiver{make(chan deal.Offer), true, ""}
	cnet.Start(r)

	var pnodes []*testutil.TestNode
	var pnets []*Libp2pQueryNetwork

	for i := 0; i < 11; i++ {
		pnode := testutil.NewTestNode(mn, t)
		pnet := NewQueryNetwork(pnode.Host)
		// Each node is forwwarding to next one
		pp := cnet.ID()
		if i > 0 {
			pp = pnets[i-1].ID()
		}
		pnet.Start(&receiver{make(chan deal.Offer), false, pp})
		pnodes = append(pnodes, pnode)
		pnets = append(pnets, pnet)
	}

	require.NoError(t, mn.LinkAll())

	require.NoError(t, mn.ConnectAllButSelf())

	// Simulating a bunch of nodes sending responses at the same time
	for i, net := range pnets {
		net := net
		pp := cnet.ID()
		if i > 0 {
			pp = pnets[i-1].ID()
		}
		go func(p peer.ID) {
			stream, err := net.NewQueryStream(p)
			require.NoError(t, err)
			defer stream.Close()

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
			require.NoError(t, err)
		}(pp)
	}

	for i := 0; i < 11; i++ {
		select {
		case of := <-r.offers:
			require.Equal(t, of.Response.Size, uint64(1600))
		case <-ctx.Done():
			require.NoError(t, ctx.Err())
		}
	}

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
