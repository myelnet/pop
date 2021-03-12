package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/myelnet/go-hop-exchange/node"
	"github.com/peterbourgon/ff/v2/ffcli"
)

var quoteArgs struct {
	rf  int // replication factor
	dur time.Duration
}

var quoteCmd = &ffcli.Command{
	Name:       "quote",
	ShortUsage: "quote <archive-cid>",
	ShortHelp:  "Get a storage price quote for a given content commit",
	LongHelp: strings.TrimSpace(`

The 'hop quote' gets a storage price quote from the market. It automatically selects reliable miners in the
given region and compiles a set of asks with size and duration parameters.

`),
	Exec: runQuote,
	FlagSet: (func() *flag.FlagSet {
		fs := flag.NewFlagSet("quote", flag.ExitOnError)
		fs.IntVar(&quoteArgs.rf, "rf", 6, "storage replication factors, the content will be stored by n providers")
		// Filecoin has a current minimum storage duration of 180 days
		fs.DurationVar(&quoteArgs.dur, "duration", 24*time.Hour*time.Duration(180), "storage contract duration")
		return fs
	})(),
}

func runQuote(ctx context.Context, args []string) error {
	c, cc, ctx, cancel := connect(ctx)
	defer cancel()

	qrc := make(chan *node.QuoteResult, 1)
	cc.SetNotifyCallback(func(n node.Notify) {
		if qr := n.QuoteResult; qr != nil {
			qrc <- qr
		}
	})
	go receive(ctx, cc, c)

	com := ""
	if len(args) > 0 {
		com = args[0]
	}
	cc.Quote(&node.QuoteArgs{
		Commit:    com,
		Duration:  quoteArgs.dur,
		StorageRF: quoteArgs.rf,
	})
	select {
	case qr := <-qrc:
		if qr.Err != "" {
			return errors.New(qr.Err)
		}
		fmt.Printf(qr.Output)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
