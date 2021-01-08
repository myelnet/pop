package hop

import (
	"github.com/ipfs/go-cid"
	"github.com/ipld/go-ipld-prime"
	"github.com/libp2p/go-libp2p-core/peer"

	datatransfer "github.com/filecoin-project/go-data-transfer"
)

type UnifiedRequestValidator struct{}

func (v *UnifiedRequestValidator) ValidatePush(
	sender peer.ID,
	voucher datatransfer.Voucher,
	baseCid cid.Cid,
	selector ipld.Node,
) (datatransfer.VoucherResult, error) {
	// TODO
	return nil, nil
}

func (v *UnifiedRequestValidator) ValidatePull(
	receiver peer.ID,
	voucher datatransfer.Voucher,
	baseCid cid.Cid,
	selector ipld.Node,
) (datatransfer.VoucherResult, error) {
	// TODO: Add a bit of security?
	return nil, nil
}
