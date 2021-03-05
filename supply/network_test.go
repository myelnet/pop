package supply

import (
	"context"
	"testing"
	"time"

	blocksutil "github.com/ipfs/go-ipfs-blocksutil"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/myelnet/go-hop-exchange/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var blockGenerator = blocksutil.NewBlockGenerator()

type testReceiver struct {
	t             *testing.T
	streamHandler func(RequestStreamer)
}

func (tr *testReceiver) HandleRequest(s RequestStreamer) {
	defer s.Close()
	if tr.streamHandler != nil {
		tr.streamHandler(s)
	}
}

func TestRequestStream(t *testing.T) {

	ctxBg := context.Background()

	mn := mocknet.New(ctxBg)

	n1 := testutil.NewTestNode(mn, t)
	n2 := testutil.NewTestNode(mn, t)

	err := mn.LinkAll()
	require.NoError(t, err)

	regions := []Region{
		{
			Name: "Test",
			Code: CustomRegion,
		},
	}

	net1 := NewNetwork(n1.Host, regions)
	net2 := NewNetwork(n2.Host, regions)

	payload := blockGenerator.Next().Cid()
	done := make(chan bool)
	tr2 := &testReceiver{t: t, streamHandler: func(s RequestStreamer) {
		r, err := s.ReadRequest()
		require.NoError(t, err)

		assert.Equal(t, payload, r.PayloadCID)
		done <- true
	}}
	net2.SetDelegate(tr2)

	ctx, cancel := context.WithTimeout(ctxBg, 10*time.Second)
	defer cancel()

	s, err := net1.NewRequestStream(n2.Host.ID())
	require.NoError(t, err)

	go require.NoError(t, s.WriteRequest(Request{
		PayloadCID: payload,
		Size:       16,
	}))

	select {
	case <-ctx.Done():
		t.Error("no request received")
	case <-done:
	}
}
