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
	streamHandler func(*addRequestStream)
}

func (tr *testReceiver) HandleAddRequest(s *addRequestStream) {
	defer s.Close()
	if tr.streamHandler != nil {
		tr.streamHandler(s)
	}
}

func TestAddRequestStream(t *testing.T) {

	ctxBg := context.Background()

	mn := mocknet.New(ctxBg)

	n1 := testutil.NewTestNode(mn, t)
	n2 := testutil.NewTestNode(mn, t)

	err := mn.LinkAll()
	require.NoError(t, err)

	net1 := NewNetwork(n1.Host)
	net2 := NewNetwork(n2.Host)

	payload := blockGenerator.Next().Cid()
	done := make(chan bool)
	tr2 := &testReceiver{t: t, streamHandler: func(s *addRequestStream) {
		r, err := s.ReadAddRequest()
		require.NoError(t, err)

		assert.Equal(t, payload, r.PayloadCID)
		done <- true
	}}
	net2.SetDelegate(tr2)

	ctx, cancel := context.WithTimeout(ctxBg, 10*time.Second)
	defer cancel()

	s, err := net1.NewAddRequestStream(n2.Host.ID())
	require.NoError(t, err)

	go require.NoError(t, s.WriteAddRequest(AddRequest{
		PayloadCID: payload,
		Size:       16,
	}))

	select {
	case <-ctx.Done():
		t.Error("no request received")
	case <-done:
	}
}
