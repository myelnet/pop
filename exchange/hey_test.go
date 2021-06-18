package exchange

import (
	"time"

	"github.com/libp2p/go-libp2p-core/peer"
)

type pmanager struct {
	heys chan Hey
	lats chan time.Duration
}

func (pm *pmanager) receive(p peer.ID, msg Hey) {
	pm.heys <- msg
}

func (pm *pmanager) recordLatency(p peer.ID, l time.Duration) error {
	pm.lats <- l
	return nil
}

type hgetter struct {
	hey Hey
}

func (hg *hgetter) getHey() Hey {
	return hg.hey
}

//
//func TestHey(t *testing.T) {
//	lat := time.Millisecond * 100
//	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
//	defer cancel()
//
//	mn := mocknet.New(ctx)
//	n1 := testutil.NewTestNode(mn, t)
//	n2 := testutil.NewTestNode(mn, t)
//
//	h1ch := make(chan Hey, 1)
//	l1ch := make(chan time.Duration, 1)
//	hey1 := Hey{
//		Regions: []RegionCode{GlobalRegion},
//	}
//	h1 := &HeyService{n1.Host, &pmanager{h1ch, l1ch}, &hgetter{hey1}}
//	require.NoError(t, h1.Run(ctx))
//
//	h2ch := make(chan Hey, 1)
//	l2ch := make(chan time.Duration, 1)
//	hey2 := Hey{
//		Regions: []RegionCode{GlobalRegion, EuropeRegion},
//	}
//	h2 := &HeyService{n2.Host, &pmanager{h2ch, l2ch}, &hgetter{hey2}}
//	require.NoError(t, h2.Run(ctx))
//
//	mn.SetLinkDefaults(mocknet.LinkOptions{Latency: lat})
//	require.NoError(t, mn.LinkAll())
//	require.NoError(t, mn.ConnectAllButSelf())
//
//	for h1ch != nil || h2ch != nil || l1ch != nil || l2ch != nil {
//		select {
//		case r := <-h1ch:
//			// h1ch should receive hey from n2
//			require.Equal(t, hey2, r)
//			h1ch = nil
//		case r := <-h2ch:
//			// h2ch should receive hey from n1
//			require.Equal(t, hey1, r)
//			h2ch = nil
//		case l := <-l1ch:
//			l1ch = nil
//			require.GreaterOrEqual(t, int64(l), int64(lat))
//		case l := <-l2ch:
//			l2ch = nil
//			require.GreaterOrEqual(t, int64(l), int64(lat))
//		case <-ctx.Done():
//			t.Fatal("didn't receive all heys")
//		}
//	}
//}
