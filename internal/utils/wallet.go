package utils

import (
	"context"
	"github.com/filecoin-project/go-address"
	"github.com/myelnet/pop/wallet"
)

// NewKey is the only function to be used to create a FIL Key
func NewKey(ctx context.Context, w wallet.Driver) (address.Address, error) {
	return w.NewKey(ctx, wallet.KTSecp256k1)
}
