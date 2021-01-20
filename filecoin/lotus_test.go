package filecoin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-jsonrpc"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/crypto"
	"github.com/ipfs/go-cid"
	"github.com/stretchr/testify/require"
)

func testBlockHeader() *BlockHeader {
	addr, err := address.NewIDAddress(12512063)
	if err != nil {
		panic(err)
	}

	c, err := cid.Decode("bafyreicmaj5hhoy5mgqvamfhgexxyergw7hdeshizghodwkjg6qmpoco7i")
	if err != nil {
		panic(err)
	}

	return &BlockHeader{
		Miner: addr,
		Ticket: &Ticket{
			VRFProof: []byte("vrf proof0000000vrf proof0000000"),
		},
		ElectionProof: &ElectionProof{
			VRFProof: []byte("vrf proof0000000vrf proof0000000"),
		},
		Parents:               []cid.Cid{c, c},
		ParentMessageReceipts: c,
		BLSAggregate:          &crypto.Signature{Type: crypto.SigTypeBLS, Data: []byte("bls signature")},
		ParentWeight:          NewInt(123125126212),
		Messages:              c,
		Height:                85919298723,
		ParentStateRoot:       c,
		BlockSig:              &crypto.Signature{Type: crypto.SigTypeBLS, Data: []byte("block signature")},
		ParentBaseFee:         NewInt(3432432843291),
	}
}

type testLotusNode struct{}

func (tln *testLotusNode) ChainHead(context.Context) (*TipSet, error) {
	blh := testBlockHeader()
	return NewTipSet([]*BlockHeader{blh})
}

func TestRPC(t *testing.T) {
	rpcServer := jsonrpc.NewServer()
	handler := &testLotusNode{}
	rpcServer.Register("Filecoin", handler)
	testServ := httptest.NewServer(rpcServer)

	addr := testServ.Listener.Addr()
	listenAddr := "ws://" + addr.String()

	bgCtx := context.Background()

	ctx, cancel := context.WithCancel(bgCtx)
	defer cancel()

	api, err := NewLotusRPC(ctx, listenAddr, http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	head, err := api.ChainHead(ctx)
	if err != nil {
		t.Fatal(err)
	}

	require.Equal(t, abi.ChainEpoch(85919298723), head.Height())
}
