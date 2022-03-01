package node

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"text/tabwriter"
	"time"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/specs-actors/v7/actors/builtin"
	"github.com/myelnet/pop/filecoin"
	"github.com/rs/zerolog/log"
)

// PaySubmit vouchers for a given payment channel
func (nd *Pop) PaySubmit(ctx context.Context, args *PayArgs) {
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

// PaySettle sends a settle message for a given actor and saves the time after which it can be collected
func (nd *Pop) PaySettle(ctx context.Context, args *PayArgs) {
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

	epoch, err := nd.exch.Payments().Settle(ctx, addr)
	if err != nil {
		sendErr(err)
		return
	}
	head, err := nd.exch.FilecoinAPI().ChainHead(ctx)
	if err != nil {
		sendErr(err)
		return
	}
	curEpoch := head.Height()
	delay := time.Duration(epoch-curEpoch) * builtin.EpochDurationSeconds * time.Second

	nd.send(Notify{PayResult: &PayResult{SettlingIn: delay.String()}})
}

// PayCollect sends a collect message for a given actor, validating before to make sure the channel is in the right state
func (nd *Pop) PayCollect(ctx context.Context, args *PayArgs) {
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
	if err := nd.exch.Payments().Collect(ctx, addr); err != nil {
		sendErr(err)
		return
	}
	nd.send(Notify{PayResult: &PayResult{}})
}

// PayTrack the given channel loading the state from chain and adding it to the store
// currently only for inbound channels. Mostly for debugging purposes
func (nd *Pop) PayTrack(ctx context.Context, args *PayArgs) {
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

	info, err := nd.exch.Payments().TrackChannel(ctx, addr)
	if err != nil {
		sendErr(err)
		return
	}
	buf := bytes.NewBuffer(nil)
	w := new(tabwriter.Writer)
	w.Init(buf, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "Saved payment channel %s with funds %s\n", info.Channel, filecoin.FIL(info.Amount))
	w.Flush()

	nd.send(Notify{PayResult: &PayResult{ChannelList: buf.String()}})
}

// PayList all vouchers for all payment channels we have
func (nd *Pop) PayList(ctx context.Context, args *PayArgs) {
	sendErr := func(err error) {
		nd.send(Notify{PayResult: &PayResult{
			Err: err.Error(),
		}})
	}
	head, err := nd.exch.FilecoinAPI().ChainHead(ctx)
	if err != nil {
		sendErr(err)
		return
	}

	channels, err := nd.exch.Payments().ListChannels()
	if err != nil {
		sendErr(err)
		return
	}
	buf := bytes.NewBuffer(nil)
	w := new(tabwriter.Writer)
	w.Init(buf, 0, 4, 2, ' ', 0)
	if len(channels) == 0 {
		fmt.Fprintf(w, "No channels in store\n")
	}
	i := 0
	for _, ch := range channels {
		info, err := nd.exch.Payments().GetChannelInfo(ch)
		if err != nil {
			log.Error().Err(err).Str("channel", ch.String()).Msg("could not get channel info")
			continue
		}
		funds, err := nd.exch.Payments().ChannelAvailableFunds(ch)
		if err != nil {
			log.Error().Err(err).Str("channel", ch.String()).Msg("could not get available funds")
			continue
		}

		fmt.Fprintf(w, "%s\ttotal: %s(aFIL)\tremaining: %s(aFIL)\n", ch, funds.ConfirmedAmt, filecoin.BigSub(funds.ConfirmedAmt, funds.VoucherRedeemedAmt))

		vouchers, err := nd.exch.Payments().ListVouchers(ctx, ch)
		if err != nil {
			log.Error().Err(err).Msg("could not list vouchers")
			continue
		}
		for _, v := range vouchers {
			fmt.Fprintf(w, "amount: %s\t lane: %d\t submitted: %t\n", v.Voucher.Amount, v.Voucher.Lane, v.Submitted)
		}
		// when a channel is settled, the balance shows as 0
		if funds.ConfirmedAmt.Equals(big.Zero()) && funds.VoucherRedeemedAmt.GreaterThan(big.Zero()) {
			fmt.Fprintf(w, "channel is ready to be collected")
		}

		if info.Settling {
			at := time.Duration(info.SettlingAt-head.Height()) * builtin.EpochDurationSeconds * time.Second
			fmt.Fprintf(w, "channel can be collected in %s\n", at)
		}

		i++
	}
	if i != len(channels) {
		fmt.Fprintf(w, "Error listing channels\n")
	}
	w.Flush()
	nd.send(Notify{PayResult: &PayResult{ChannelList: buf.String()}})
}
