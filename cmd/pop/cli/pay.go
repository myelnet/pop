package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math"
	"strings"

	"github.com/myelnet/pop/node"
	"github.com/peterbourgon/ff/v3/ffcli"
)

var payArgs struct {
	lane uint64
}

var submitCmd = &ffcli.Command{
	Name:       "submit",
	ShortUsage: "pay submit <channel-address>",
	ShortHelp:  "submit all vouchers for a given payment channel",
	Exec:       runSubmit,
}

var payListCmd = &ffcli.Command{
	Name:       "list",
	ShortUsage: "pay list",
	ShortHelp:  "list all existing payment channels and they current state",
	Exec:       runPayList,
}

var paySettleCmd = &ffcli.Command{
	Name:       "settle",
	ShortUsage: "pay settle <channel-address>",
	ShortHelp:  "settle a given payment channel",
	Exec:       runPaySettle,
}

var payCollectCmd = &ffcli.Command{
	Name:       "collect",
	ShortUsage: "pay collect <channel-address>",
	ShortHelp:  "collect a given payment channel",
	Exec:       runPayCollect,
}

var payTrackCmd = &ffcli.Command{
	Name:       "track",
	ShortUsage: "pay track <channel-address>",
	ShortHelp:  "track a given channel in the local store",
	Exec:       runPayTrack,
}

var payCmd = &ffcli.Command{
	Name:      "pay",
	ShortHelp: "Manage payment channels",
	LongHelp: strings.TrimSpace(`

Payment channel operations are for the most part automated, these commands offer finer grained control
for inbound payment management.

`),
	Exec: func(context.Context, []string) error {
		return flag.ErrHelp
	},
	FlagSet: (func() *flag.FlagSet {
		fs := flag.NewFlagSet("pay", flag.ExitOnError)
		fs.Uint64Var(&payArgs.lane, "lane", math.MaxUint64, "specific channel lane. Default will apply to all lanes")
		return fs
	})(),
	Subcommands: []*ffcli.Command{submitCmd, payListCmd, paySettleCmd, payCollectCmd, payTrackCmd},
}

func runSubmit(ctx context.Context, args []string) error {
	c, cc, ctx, cancel := connect(ctx)
	defer cancel()

	submitResults := make(chan *node.PayResult, 1)
	cc.SetNotifyCallback(func(n node.Notify) {
		if sr := n.PayResult; sr != nil {
			submitResults <- sr
		}
	})
	go receive(ctx, cc, c)

	chAddr := args[0]

	cc.PaySubmit(&node.PayArgs{ChAddr: chAddr, Lane: payArgs.lane})

	select {
	case sr := <-submitResults:
		if sr.Err != "" {
			return errors.New(sr.Err)
		}
		fmt.Println("Submitted vouchers")
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func runPayList(ctx context.Context, args []string) error {
	c, cc, ctx, cancel := connect(ctx)
	defer cancel()

	listResults := make(chan *node.PayResult, 1)
	cc.SetNotifyCallback(func(n node.Notify) {
		if lr := n.PayResult; lr != nil {
			listResults <- lr
		}
	})
	go receive(ctx, cc, c)

	cc.PayList(&node.PayArgs{})

	select {
	case lr := <-listResults:
		if lr.Err != "" {
			return errors.New(lr.Err)
		}
		fmt.Printf(lr.ChannelList)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func runPayTrack(ctx context.Context, args []string) error {
	c, cc, ctx, cancel := connect(ctx)
	defer cancel()

	trackResults := make(chan *node.PayResult, 1)
	cc.SetNotifyCallback(func(n node.Notify) {
		if tr := n.PayResult; tr != nil {
			trackResults <- tr
		}
	})
	go receive(ctx, cc, c)

	chAddr := args[0]

	cc.PayTrack(&node.PayArgs{ChAddr: chAddr})

	select {
	case tr := <-trackResults:
		if tr.Err != "" {
			return errors.New(tr.Err)
		}
		fmt.Printf(tr.ChannelList)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func runPaySettle(ctx context.Context, args []string) error {
	c, cc, ctx, cancel := connect(ctx)
	defer cancel()

	settleResults := make(chan *node.PayResult, 1)
	cc.SetNotifyCallback(func(n node.Notify) {
		if sr := n.PayResult; sr != nil {
			settleResults <- sr
		}
	})
	go receive(ctx, cc, c)

	chAddr := args[0]

	cc.PaySettle(&node.PayArgs{ChAddr: chAddr})

	select {
	case sr := <-settleResults:
		if sr.Err != "" {
			return errors.New(sr.Err)
		}
		fmt.Println("Channel settling in", sr.SettlingIn)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func runPayCollect(ctx context.Context, args []string) error {
	c, cc, ctx, cancel := connect(ctx)
	defer cancel()

	collectResults := make(chan *node.PayResult, 1)
	cc.SetNotifyCallback(func(n node.Notify) {
		if sr := n.PayResult; sr != nil {
			collectResults <- sr
		}
	})
	go receive(ctx, cc, c)

	chAddr := args[0]

	cc.PayCollect(&node.PayArgs{ChAddr: chAddr})

	select {
	case cr := <-collectResults:
		if cr.Err != "" {
			return errors.New(cr.Err)
		}
		fmt.Println("Channel collected")
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
