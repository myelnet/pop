package exchange

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-eventbus"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/myelnet/pop/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestHeyEvtPeerMgr(t *testing.T) {
	lat := time.Millisecond * 100
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	mn := mocknet.New()
	n1 := testutil.NewTestNode(mn, t)
	n2 := testutil.NewTestNode(mn, t)

	idx, err := NewIndex(n1.Ds, n1.Bs)
	require.NoError(t, err)

	p1 := NewPeerMgr(n1.Host, idx, []Region{global})
	p2 := NewPeerMgr(n2.Host, idx, []Region{global})
	sub1, err := p1.h.EventBus().Subscribe(new(HeyEvt), eventbus.BufSize(16))
	require.NoError(t, err)

	sub2, err := p2.h.EventBus().Subscribe(new(HeyEvt), eventbus.BufSize(16))
	require.NoError(t, err)

	require.NoError(t, p1.Run(ctx))
	require.NoError(t, p2.Run(ctx))

	time.Sleep(time.Second)

	mn.SetLinkDefaults(mocknet.LinkOptions{Latency: lat})
	require.NoError(t, mn.LinkAll())
	require.NoError(t, mn.ConnectAllButSelf())

	heyEvt1 := (<-sub1.Out()).(HeyEvt)
	heyEvt2 := (<-sub2.Out()).(HeyEvt)

	require.Equal(t, heyEvt1.Peer.String(), p2.h.ID().String())
	require.Equal(t, heyEvt2.Peer.String(), p1.h.ID().String())
}

func TestRecordLatency(t *testing.T) {
	mn := mocknet.New()
	n1 := testutil.NewTestNode(mn, t)
	n2 := testutil.NewTestNode(mn, t)
	idx, err := NewIndex(n1.Ds, n1.Bs)
	require.NoError(t, err)

	p1 := NewPeerMgr(n1.Host, idx, []Region{global})
	p1.handleHey(n2.Host.ID(), Hey{
		Regions:   []RegionCode{GlobalRegion},
		IndexRoot: nil,
	})

	now := time.Now()
	start := now.Add(-3 * time.Second)
	latency := now.Sub(start)

	err = p1.recordLatency(n2.Host.ID(), now, start)
	require.NoError(t, err)

	p1Latency := p1.peers[n2.Host.ID()].Latency
	require.Equal(t, latency, p1Latency)
}
