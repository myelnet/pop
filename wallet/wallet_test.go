package wallet

import (
	"context"
	"testing"

	"github.com/filecoin-project/go-state-types/big"
	"github.com/ipfs/go-ipfs/keystore"
	fil "github.com/myelnet/go-hop-exchange/lotus"
	"github.com/stretchr/testify/require"
)

func TestSecpSignature(t *testing.T) {
	ctx := context.Background()
	ks := keystore.NewMemKeystore()

	w := NewIPFS(ks)

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
