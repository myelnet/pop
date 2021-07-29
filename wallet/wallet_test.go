package wallet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-jsonrpc"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/ipfs/go-cid"
	blocksutil "github.com/ipfs/go-ipfs-blocksutil"
	keystore "github.com/ipfs/go-ipfs-keystore"
	fil "github.com/myelnet/pop/filecoin"
	"github.com/stretchr/testify/require"
)

var blockGenerator = blocksutil.NewBlockGenerator()

func TestSecpSignature(t *testing.T) {
	ctx := context.Background()
	ks := keystore.NewMemKeystore()

	w := NewFromKeystore(ks)

	addr1, err := w.NewKey(ctx, KTSecp256k1)
	if err != nil {
		t.Fatal(err)
	}

	addr2, err := w.NewKey(ctx, KTSecp256k1)
	if err != nil {
		t.Fatal(err)
	}

	msg := &fil.Message{
		To:         addr2,
		From:       addr1,
		Nonce:      0,
		Value:      big.NewInt(1000),
		GasLimit:   222,
		GasFeeCap:  big.NewInt(333),
		GasPremium: big.NewInt(333),
		Method:     0,
	}

	mbl, err := msg.ToStorageBlock()
	if err != nil {
		t.Fatal(err)
	}

	sig, err := w.Sign(ctx, addr1, mbl.Cid().Bytes())
	if err != nil {
		t.Fatal(err)
	}

	smsg := &fil.SignedMessage{
		Message:   *msg,
		Signature: *sig,
	}

	valid, err := w.Verify(ctx, addr1, mbl.Cid().Bytes(), &smsg.Signature)
	if err != nil {
		t.Fatal(err)
	}
	require.True(t, valid)
}

func TestDefaultAddress(t *testing.T) {
	ctx := context.Background()
	ks := keystore.NewMemKeystore()

	w := NewFromKeystore(ks)

	addr1, err := w.NewKey(ctx, KTSecp256k1)
	require.NoError(t, err)

	def := w.DefaultAddress()
	require.NoError(t, err)
	require.Equal(t, addr1, def)

	addr2, err := w.NewKey(ctx, KTSecp256k1)
	require.NoError(t, err)

	def = w.DefaultAddress()
	require.NoError(t, err)
	require.Equal(t, addr1, def)

	err = w.SetDefaultAddress(addr2)
	require.NoError(t, err)

	def = w.DefaultAddress()
	require.NoError(t, err)
	require.Equal(t, addr2, def)

	expected := []address.Address{addr1, addr2}
	sort.Slice(expected, func(i, j int) bool {
		return expected[i].String() < expected[j].String()
	})
	list, err := w.List()
	require.NoError(t, err)
	require.Equal(t, expected, list)
}

type testLotusNode struct{}

func (tln *testLotusNode) GasEstimateMessageGas(ctx context.Context, msg *fil.Message, spec *fil.MessageSendSpec, tsk fil.TipSetKey) (*fil.Message, error) {
	msg.GasLimit = int64(123)
	msg.GasPremium = fil.NewInt(234)
	msg.GasFeeCap = fil.NewInt(345)
	return msg, nil
}

func (tln *testLotusNode) StateGetActor(ctx context.Context, addr address.Address, tsk fil.TipSetKey) (*fil.Actor, error) {
	return &fil.Actor{
		Code:    blockGenerator.Next().Cid(),
		Head:    blockGenerator.Next().Cid(),
		Nonce:   uint64(7),
		Balance: fil.NewInt(30),
	}, nil
}

func (tln *testLotusNode) MpoolPush(ctx context.Context, msg *fil.SignedMessage) (cid.Cid, error) {
	return blockGenerator.Next().Cid(), nil
}

func (tln *testLotusNode) StateSearchMsg(ctx context.Context, c cid.Cid) (*fil.MsgLookup, error) {
	return &fil.MsgLookup{
		Message: c,
		Receipt: fil.MessageReceipt{
			ExitCode: 0,
		},
	}, nil
}

func TestTransfer(t *testing.T) {
	rpcServer := jsonrpc.NewServer()
	handler := &testLotusNode{}
	rpcServer.Register("Filecoin", handler)
	testServ := httptest.NewServer(rpcServer)

	addr := testServ.Listener.Addr()
	listenAddr := "ws://" + addr.String()

	bgCtx := context.Background()

	ctx, cancel := context.WithCancel(bgCtx)
	defer cancel()

	api, err := fil.NewLotusRPC(ctx, listenAddr, http.Header{})
	if err != nil {
		t.Fatal(ctx)
	}
	defer api.Close()
	ks := keystore.NewMemKeystore()

	w := NewFromKeystore(ks, WithFilAPI(api), WithConfidence(0))

	addr1, err := w.NewKey(ctx, KTSecp256k1)
	if err != nil {
		t.Fatal(err)
	}

	addr2, err := w.NewKey(ctx, KTSecp256k1)
	if err != nil {
		t.Fatal(err)
	}

	err = w.Transfer(ctx, addr1, addr2, "12")
	require.NoError(t, err)
}
