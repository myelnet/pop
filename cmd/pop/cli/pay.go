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

var submit = &ffcli.Command{
	Name:       "submit",
	ShortUsage: "pay submit <channel-address>",
	ShortHelp:  "submit all vouchers for a given payment channel",
	Exec:       runSubmit,
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
	Subcommands: []*ffcli.Command{submit},
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
