package node

import (
	"context"
	"math"

	"github.com/filecoin-project/go-address"
)

// PaySubmit vouchers for a given payment channel
func (nd *node) PaySubmit(ctx context.Context, args *PayArgs) {
	sendErr := func(err error) {
		nd.send(Notify{PayResult: &PayResult{
			Err: err.Error(),
		}})
	}

	addr, err := address.NewFromString(args.ChAddr)
	if err != nil {
		sendErr(err)
		return
	}
	if args.Lane == math.MaxUint64 {
		if err := nd.exch.Payments().SubmitAllVouchers(ctx, addr); err != nil {
			sendErr(err)
			return
		}
		nd.send(Notify{PayResult: &PayResult{}})
		return
	}

	if err := nd.exch.Payments().SubmitVoucherForLane(ctx, addr, args.Lane); err != nil {
		sendErr(err)
		return
	}
	nd.send(Notify{PayResult: &PayResult{}})
}
