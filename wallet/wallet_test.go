package wallet

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/filecoin-project/go-address"
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

func TestImportKey(t *testing.T) {
	ctx := context.Background()
	ks := keystore.NewMemKeystore()

	w := NewIPFS(ks)

	h := "7b2254797065223a22626c73222c22507269766174654b6579223a226a6b55704e6a53493749664a4632434f6f505169344f79477a475241532b766b616c314e5a616f7a3853633d227d"
	decoded, _ := hex.DecodeString(h)

	var ki KeyInfo
	if err := json.Unmarshal(decoded, &ki); err != nil {
		t.Fatal(err)
	}

	addr, err := w.ImportKey(ctx, &ki)
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := address.NewFromString("f3w2ll4guubkslpmxseiqhtemwtmxdnhnshogd25gfrbhe6dso6kly2aj756wmcx2gq4jehn6x2z3ji4zlzioq")
	require.Equal(t, expected, addr)
}
