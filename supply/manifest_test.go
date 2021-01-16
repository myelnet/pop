package supply

import (
	"context"
	"testing"

	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	peer "github.com/libp2p/go-libp2p-core/peer"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/myelnet/go-hop-exchange/testutil"
	"github.com/stretchr/testify/require"
)

type mockAddRequestStream struct {
	req AddRequest
	p   peer.ID
}

func (a *mockAddRequestStream) ReadAddRequest() (AddRequest, error) {
	return a.req, nil
}

func (a *mockAddRequestStream) WriteAddRequest(AddRequest) error {
	return nil
}

func (a *mockAddRequestStream) Close() error {
	return nil
}

func (a *mockAddRequestStream) OtherPeer() peer.ID {
	return a.p
}

func TestHandleAddRequest(t *testing.T) {
	bgCtx := context.Background()

	mn := mocknet.New(bgCtx)

	n1 := testutil.NewTestNode(mn, t)
	n2 := testutil.NewTestNode(mn, t)

	err := mn.LinkAll()
	require.NoError(t, err)

	n1.SetupDataTransfer(bgCtx, t)
	require.NoError(t, n1.Dt.RegisterVoucherType(&AddRequest{}, &testutil.FakeDTValidator{}))

	n2.SetupDataTransfer(bgCtx, t)
	require.NoError(t, n2.Dt.RegisterVoucherType(&AddRequest{}, &testutil.FakeDTValidator{}))

	// n1 is our client and is adding a file to the network
	link, origBytes := n1.LoadUnixFSFileToStore(bgCtx, t, "/supply/readme.md")
	rootCid := link.(cidlink.Link).Cid

	// n2 is our provider and received a request from n1 (mocked in this test)
	// it calls HandleAddRequest to maybe retrieve it from the client
	manifest := NewManifest(n2.Host, n2.Dt)
	stream := &mockAddRequestStream{
		req: AddRequest{
			PayloadCID: rootCid,
			Size:       16,
		},
		p: n1.Host.ID(),
	}

	// We pass a mocked stream with a message our provider would have received from the client
	manifest.HandleAddRequest(stream)

	// Now we check if we have received the blocks
	n2.VerifyFileTransferred(bgCtx, t, rootCid, origBytes)
}
