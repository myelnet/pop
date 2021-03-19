package supply

import (
	"context"
	"testing"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	peer "github.com/libp2p/go-libp2p-core/peer"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/myelnet/pop/testutil"
	"github.com/stretchr/testify/require"
)

type mockRequestStream struct {
	req Request
	p   peer.ID
}

func (a *mockRequestStream) ReadRequest() (Request, error) {
	return a.req, nil
}

func (a *mockRequestStream) WriteRequest(Request) error {
	return nil
}

func (a *mockRequestStream) Close() error {
	return nil
}

func (a *mockRequestStream) OtherPeer() peer.ID {
	return a.p
}

func TestHandleRequest(t *testing.T) {
	bgCtx := context.Background()

	mn := mocknet.New(bgCtx)

	n1 := testutil.NewTestNode(mn, t)
	n2 := testutil.NewTestNode(mn, t)

	err := mn.LinkAll()
	require.NoError(t, err)

	n1.SetupDataTransfer(bgCtx, t)
	require.NoError(t, n1.Dt.RegisterVoucherType(&Request{}, &testutil.FakeDTValidator{}))

	n2.SetupDataTransfer(bgCtx, t)
	require.NoError(t, n2.Dt.RegisterVoucherType(&Request{}, &testutil.FakeDTValidator{}))

	name := n1.CreateRandomFile(t, 256000)

	// n1 is our client and is adding a file to the network
	link, storeID, origBytes := n1.LoadFileToNewStore(bgCtx, t, name)
	rootCid := link.(cidlink.Link).Cid

	// Need to tell the data transfer manager which store to use
	err = n1.Dt.RegisterTransportConfigurer(&Request{}, func(channelID datatransfer.ChannelID, voucher datatransfer.Voucher, transport datatransfer.Transport) {
		gsTransport, ok := transport.(StoreConfigurableTransport)
		if !ok {
			return
		}
		store, err := n1.Ms.Get(storeID)
		require.NoError(t, err)
		err = gsTransport.UseStore(channelID, store.Loader, store.Storer)
		require.NoError(t, err)
	})
	require.NoError(t, err)

	// n2 is our provider and received a request from n1 (mocked in this test)
	// it calls HandleAddRequest to maybe retrieve it from the client
	manifest := NewManifest(n2.Host, n2.Dt, n2.Ds, n2.Ms)
	stream := &mockRequestStream{
		req: Request{
			PayloadCID: rootCid,
			Size:       16,
		},
		p: n1.Host.ID(),
	}

	// We pass a mocked stream with a message our provider would have received from the client
	manifest.HandleRequest(stream)

	// Now we check if we have received the blocks
	n2.VerifyFileTransferred(bgCtx, t, n2.DAG, rootCid, origBytes)
}
