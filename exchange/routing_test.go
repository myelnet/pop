package exchange

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/ipfs/go-cid"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	bhost "github.com/libp2p/go-libp2p-blankhost"
	"github.com/libp2p/go-libp2p-core/peer"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	swarmt "github.com/libp2p/go-libp2p-swarm/testing"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/myelnet/pop/internal/testutil"
	"github.com/myelnet/pop/retrieval/deal"
	sel "github.com/myelnet/pop/selectors"
	"github.com/stretchr/testify/require"
)

type Topology func(*testing.T, mocknet.Mocknet, []*testutil.TestNode, []*testutil.TestNode)

func All(t *testing.T, mn mocknet.Mocknet, rn []*testutil.TestNode, prs []*testutil.TestNode) {
	require.NoError(t, mn.LinkAll())
	require.NoError(t, mn.ConnectAllButSelf())
}

func OneToOne(t *testing.T, mn mocknet.Mocknet, rn []*testutil.TestNode, prs []*testutil.TestNode) {
	require.NoError(t, testutil.Connect(rn[0], prs[0]))
	time.Sleep(time.Second)
}

func Markov(t *testing.T, mn mocknet.Mocknet, rn []*testutil.TestNode, prs []*testutil.TestNode) {
	prevPeer := rn[0]
	var peers []*testutil.TestNode
	peers = append(peers, rn[1:]...)
	peers = append(peers, prs...)
	for _, tn := range peers {
		require.NoError(t, testutil.Connect(prevPeer, tn))
		prevPeer = tn
	}
	time.Sleep(time.Second)
}

func noop(*testutil.TestNode) {}

func calcResponse(ctx context.Context, p peer.ID, r Region, q deal.Query) (deal.Offer, error) {
	return deal.Offer{
		PayloadCID:                 q.PayloadCID,
		Size:                       uint64(268009),
		PaymentAddress:             address.TestAddress,
		MinPricePerByte:            abi.NewTokenAmount(2),
		MaxPaymentInterval:         deal.DefaultPaymentInterval,
		MaxPaymentIntervalIncrease: deal.DefaultPaymentIntervalIncrease,
	}, nil
}

func TestGossipRouting(t *testing.T) {
	withSwarmT := func(tn *testutil.TestNode) {
		netw := swarmt.GenSwarm(t, context.Background())
		h := bhost.NewBlankHost(netw)
		tn.Host = h
	}
	testCases := []struct {
		name     string
		topology Topology
		peers    int
		clients  int
		files    int
		netOpts  func(*testutil.TestNode)
	}{
		{
			name:     "Connect all",
			topology: All,
			peers:    11,
			clients:  1,
			netOpts:  noop,
			files:    1,
		},
		{
			name:     "Connect all with 2 clients",
			topology: All,
			peers:    11,
			clients:  2,
			netOpts:  noop,
			files:    1,
		},

		{
			name:     "One to one",
			topology: OneToOne,
			peers:    2,
			clients:  1,
			netOpts:  withSwarmT,
			files:    1,
		},
		{
			name:     "Markov",
			topology: Markov,
			peers:    6,
			clients:  1,
			netOpts:  withSwarmT,
			files:    3,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			bgCtx := context.Background()

			ctx, cancel := context.WithTimeout(bgCtx, 5*time.Second)
			defer cancel()

			mn := mocknet.New(bgCtx)

			clients := make(map[peer.ID]*GossipRouting)
			var cnodes []*testutil.TestNode

			providers := make(map[peer.ID]*GossipRouting)
			var pnodes []*testutil.TestNode

			fnames := make([]string, testCase.files)
			for i := range fnames {
				// This just creates the file without adding it
				fnames[i] = (&testutil.TestNode{}).CreateRandomFile(t, 256000)
			}
			roots := make([]cid.Cid, testCase.files)

			var rootCid cid.Cid

			for i := 0; i < testCase.peers; i++ {
				n := testutil.NewTestNode(mn, t, testCase.netOpts)

				tracer := NewGossipTracer()
				ps, err := pubsub.NewGossipSub(ctx, n.Host, pubsub.WithEventTracer(tracer))
				require.NoError(t, err)
				routing := NewGossipRouting(n.Host, ps, tracer, []Region{global})

				require.NoError(t, routing.StartProviding(ctx, calcResponse))

				if i < testCase.clients {
					clients[n.Host.ID()] = routing
					cnodes = append(cnodes, n)
				} else {
					providers[n.Host.ID()] = routing
					pnodes = append(pnodes, n)

					for i, name := range fnames {
						link, _, _ := n.LoadFileToNewStore(ctx, t, name)
						rootCid = link.(cidlink.Link).Cid
						roots[i] = rootCid
					}
				}
			}

			testCase.topology(t, mn, cnodes, pnodes)

			for _, client := range clients {
				for _, root := range roots {
					offers := make(chan deal.Offer)
					client.SetReceiver(func(r deal.Offer) {
						offers <- r
					})
					err := client.Query(ctx, root, sel.All())
					require.NoError(t, err)

					// execute a job for each offer
					for i := 0; i < testCase.peers-testCase.clients; i++ {
						select {
						case r := <-offers:
							require.Equal(t, r.Size, uint64(268009))
						case <-ctx.Done():
							t.Fatal("couldn't get all the responses")
						}
					}
				}
			}

		})
	}

}

func TestGossipDuplicateRequests(t *testing.T) {
	bgCtx := context.Background()
	ctx, cancel := context.WithTimeout(bgCtx, 4*time.Second)
	defer cancel()

	// make a mock network
	mn := mocknet.New(ctx)

	// Generate a random file and keep reference to its location on disk
	fileName := (&testutil.TestNode{}).CreateRandomFile(t, 256000)

	// Keep a reference to the root CID of the file
	var rootCID cid.Cid

	// We can keep reference to our providers here
	providers := make(map[peer.ID]*GossipRouting)
	var pnodes []*testutil.TestNode

	// Generate providers
	for i := 0; i < 11; i++ {
		n := testutil.NewTestNode(mn, t)

		// Create all our service instances
		tracer := NewGossipTracer()
		ps, err := pubsub.NewGossipSub(ctx, n.Host, pubsub.WithEventTracer(tracer))
		require.NoError(t, err)
		routing := NewGossipRouting(n.Host, ps, tracer, []Region{global})
		// This will start listening for gossip messages (calcResponse is mocked at routing_test.go:50)
		require.NoError(t, routing.StartProviding(ctx, calcResponse))

		// each provider is loading the same file in their blockstore
		link, _, _ := n.LoadFileToNewStore(ctx, t, fileName)
		// The Link interface must be cast to access the Cid field
		rootCID = link.(cidlink.Link).Cid

		providers[n.Host.ID()] = routing
		pnodes = append(pnodes, n)

	}

	// Make a single client
	n := testutil.NewTestNode(mn, t)

	tracer := NewGossipTracer()
	ps, err := pubsub.NewGossipSub(ctx, n.Host, pubsub.WithEventTracer(tracer))
	require.NoError(t, err)
	client := NewGossipRouting(n.Host, ps, tracer, []Region{global})

	// calcResponse is mocked at routing_test.go:50
	require.NoError(t, client.StartProviding(ctx, calcResponse))

	// Connect all our nodes
	require.NoError(t, mn.LinkAll())
	// ConnectAllButSelf means all the nodes will dial each other except themselves
	require.NoError(t, mn.ConnectAllButSelf())

	// Wait for the responses the client will get here
	resps := make(chan deal.Offer)
	// This will be called each time the client receives a response from a provider
	client.SetReceiver(func(r deal.Offer) {
		resps <- r
	})

	for duplicateQueryCpt := 0; duplicateQueryCpt < 3; duplicateQueryCpt++ {
		// publish a gossip message containing the CID and the selector to all the subscribed providers
		err = client.Query(ctx, rootCID, sel.All())
		require.NoError(t, err)

		// iterate over all the responses to verify they're all here
		for i := 0; i < 11; i++ {
			select {
			case r := <-resps:
				require.Equal(t, r.Size, uint64(268009))
			case <-ctx.Done():
				t.Fatal("couldn't get all the responses")
			}
		}
	}
}

type mtracker struct {
	isRecipient bool
	recipient   peer.ID
}

func (mt mtracker) Sender(id string) (peer.ID, error) {
	return mt.recipient, nil
}

func (mt mtracker) Published(id string) bool {
	return mt.isRecipient
}

// This test isolates the message forwarding part of the system essentially sending a bunch of
// responses to adjacent peers and expecting all the messages to make their way to the client
func TestMessageForwarding(t *testing.T) {
	bgCtx := context.Background()
	ctx, cancel := context.WithTimeout(bgCtx, 4*time.Second)
	defer cancel()

	mn := mocknet.New(bgCtx)

	cnode := testutil.NewTestNode(mn, t)
	// create a random block to mock the content
	blk := testutil.CreateRandomBlock(t, cnode.Bs)
	ps, err := pubsub.NewGossipSub(ctx, cnode.Host)
	require.NoError(t, err)
	// We don't need store getters or address getters as we're manually sending responses in
	cnet := NewGossipRouting(cnode.Host, ps, mtracker{true, ""}, []Region{global})
	responses := make(chan deal.Offer)
	cnet.receiveOffer = func(r deal.Offer) {
		responses <- r
	}
	require.NoError(t, cnet.StartProviding(ctx, calcResponse))
	var pnodes []*testutil.TestNode
	var pnets []*GossipRouting

	for i := 0; i < 11; i++ {
		pnode := testutil.NewTestNode(mn, t)
		// Each node is forwwarding to next one
		pp := cnode.Host.ID()
		if i > 0 {
			pp = pnets[i-1].h.ID()
		}
		ps, err := pubsub.NewGossipSub(ctx, pnode.Host)
		require.NoError(t, err)
		pnet := NewGossipRouting(pnode.Host, ps, mtracker{false, pp}, []Region{global})
		require.NoError(t, pnet.StartProviding(ctx, calcResponse))
		pnodes = append(pnodes, pnode)
		pnets = append(pnets, pnet)
	}

	require.NoError(t, mn.LinkAll())

	require.NoError(t, mn.ConnectAllButSelf())

	// Simulating a bunch of nodes sending responses at the same time
	for i, net := range pnets {
		net := net
		pp := cnet.h.ID()
		if i > 0 {
			pp = pnets[i-1].h.ID()
		}
		go func(p peer.ID) {
			stream, err := net.NewQueryStream(p, net.queryProtocols)
			require.NoError(t, err)
			defer stream.Close()

			addr, _ := address.NewIDAddress(uint64(10))
			addrs, _ := net.Addrs()

			answer := deal.Offer{
				PayloadCID:                 blk.Cid(),
				ID:                         "02Qm",
				PeerAddr:                   addrs[0].Bytes(),
				Size:                       1600,
				PaymentAddress:             addr,
				MinPricePerByte:            deal.DefaultPricePerByte,
				MaxPaymentInterval:         deal.DefaultPaymentInterval,
				MaxPaymentIntervalIncrease: deal.DefaultPaymentIntervalIncrease,
			}
			err = stream.WriteOffer(answer)
			require.NoError(t, err)
		}(pp)
	}

	for i := 0; i < 11; i++ {
		select {
		case r := <-responses:
			require.Equal(t, r.Size, uint64(1600))
		case <-ctx.Done():
			require.NoError(t, ctx.Err())
		}
	}

}

// Here we benchmark the process of intercepting, decoding and forwarding messages
func BenchmarkNetworkForwarding(b *testing.B) {
	bgCtx := context.Background()
	ctx, cancel := context.WithCancel(bgCtx)
	defer cancel()

	mn := mocknet.New(bgCtx)

	cnode := testutil.NewTestNode(mn, b)
	ps, err := pubsub.NewGossipSub(ctx, cnode.Host)
	require.NoError(b, err)
	cnet := NewGossipRouting(cnode.Host, ps, mtracker{true, ""}, []Region{global})
	responses := make(chan deal.Offer)
	cnet.receiveOffer = func(r deal.Offer) {
		responses <- r
	}
	require.NoError(b, cnet.StartProviding(ctx, calcResponse))

	var pnodes []*testutil.TestNode
	var pnets []*GossipRouting

	for i := 0; i < 1+b.N; i++ {
		pnode := testutil.NewTestNode(mn, b)
		// Each node is forwwarding to next one
		pp := cnet.h.ID()
		if i > 0 {
			pp = pnets[i-1].h.ID()
		}
		ps, err := pubsub.NewGossipSub(ctx, pnode.Host)
		require.NoError(b, err)

		pnet := NewGossipRouting(pnode.Host, ps, mtracker{false, pp}, []Region{global})
		require.NoError(b, pnet.StartProviding(ctx, calcResponse))
		pnodes = append(pnodes, pnode)
		pnets = append(pnets, pnet)
	}

	require.NoError(b, mn.LinkAll())

	require.NoError(b, mn.ConnectAllButSelf())

	runtime.GC()
	b.ResetTimer()
	b.ReportAllocs()

	// Simulating a bunch of nodes sending responses at the same time
	for i, net := range pnets {
		net := net
		pp := cnet.h.ID()
		if i > 0 {
			pp = pnets[i-1].h.ID()
		}
		go func(p peer.ID) {
			stream, err := net.NewQueryStream(p, net.queryProtocols)
			require.NoError(b, err)
			defer stream.Close()

			addr, _ := address.NewIDAddress(uint64(10))
			addrs, _ := net.Addrs()

			answer := deal.Offer{
				PayloadCID:                 blockGen.Next().Cid(),
				ID:                         "02Qm",
				PeerAddr:                   addrs[0].Bytes(),
				Size:                       1600,
				PaymentAddress:             addr,
				MinPricePerByte:            deal.DefaultPricePerByte,
				MaxPaymentInterval:         deal.DefaultPaymentInterval,
				MaxPaymentIntervalIncrease: deal.DefaultPaymentIntervalIncrease,
			}
			err = stream.WriteOffer(answer)
			require.NoError(b, err)
		}(pp)
	}

	for i := 0; i < 1+b.N; i++ {
		select {
		case r := <-responses:
			require.Equal(b, r.Size, uint64(1600))
		case <-ctx.Done():
			require.NoError(b, ctx.Err())
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
