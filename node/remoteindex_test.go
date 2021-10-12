package node

import (
	"context"
	"testing"

	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/multiformats/go-multiaddr"
	"github.com/myelnet/pop/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestRemoteIndex(t *testing.T) {

	mn := mocknet.New(context.Background())
	tn := testutil.NewTestNode(mn, t)

	ri, err := NewRemoteIndex("https://routing.myel.workers.dev", tn.Host, nil, []string{"ohio.myel.zone"})
	require.NoError(t, err)

	ma, err := multiaddr.NewMultiaddrBytes(ri.address)
	require.NoError(t, err)

	require.Equal(t, "/dns4/ohio.myel.zone/tcp/0/p2p/"+tn.Host.ID().String(), ma.String())
}
