package filecoin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/filecoin-project/go-jsonrpc"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/stretchr/testify/require"
)

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
