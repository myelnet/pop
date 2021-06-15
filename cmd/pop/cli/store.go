package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/AlecAivazis/survey/v2"
	"github.com/myelnet/pop/filecoin"
	"github.com/myelnet/pop/node"
	"github.com/peterbourgon/ff/v2/ffcli"
)

var storeArgs struct {
	RF       int
	duration time.Duration
	maxPrice uint64
	verified bool
}

var storeCmd = &ffcli.Command{
	Name:       "store",
	ShortUsage: "store <tx_cid,..>",
	ShortHelp:  "Aggregate DAGs into a new Filecoin storage deal",
	LongHelp: strings.TrimSpace(`

The 'pop store' command attempts to store the given transaction(s) in a Filecoin storage deal. If the size of the content is not large enough, it is staged to be added with the content of the next store call.

`),
	Exec: runStore,
	FlagSet: (func() *flag.FlagSet {
		fs := flag.NewFlagSet("store", flag.ExitOnError)
		fs.IntVar(&commArgs.RF, "storage-rf", 2, "number of storage providers to start deals with")
		fs.DurationVar(&commArgs.duration, "duration", 24*time.Hour*time.Duration(180), "duration we need the content stored for")
		// MaxStoragePrice is our price ceiling to filter out bad storage miners who charge too much
		fs.Uint64Var(&commArgs.maxPrice, "max-storage-price", uint64(20_000_000_000), "maximum price per byte our node is willing to pay for storage")
		fs.BoolVar(&commArgs.verified, "verified", false, "verified deals should be cheaper but require a data cap associated with the client address used")
		return fs
	})(),
}

func runStore(ctx context.Context, args []string) error {
	c, cc, ctx, cancel := connect(ctx)
	defer cancel()

	src := make(chan *node.StoreResult, 1)
	cc.SetNotifyCallback(func(n node.Notify) {
		if sr := n.StoreResult; sr != nil {
			src <- sr
		}
	})
	go receive(ctx, cc, c)

	cc.Store(&node.StoreArgs{
		Refs:      args,
		StorageRF: storeArgs.RF,
		Duration:  storeArgs.duration,
		MaxPrice:  storeArgs.maxPrice,
		Verified:  storeArgs.verified,
	})

	select {
	case sr := <-src:
		if sr.Err != "" {
			return errors.New(sr.Err)
		}
		if cr.Capacity > 0 {
			fmt.Printf("%s left before batch can be stored\n", filecoin.SizeStr(filecoin.NewInt(cr.Capacity)))
			return nil
		}
		if len(cr.Miners) > 0 {
			fmt.Printf("Started storage deals with %s\n", cr.Miners)
		}
	case <-ctx.Done():
		return ctx.Err()
	}

	return nil
}

// TODO: use a separate quote command if getting a quote is useful
func runQuote(ctx context.Context, c net.Conn, cc *node.CommandClient, refs []string) (map[string]bool, error) {
	qrc := make(chan *node.QuoteResult, 1)
	cc.SetNotifyCallback(func(n node.Notify) {
		if qr := n.QuoteResult; qr != nil {
			qrc <- qr
		}
	})
	go receive(ctx, cc, c)

	fmt.Printf("Calculating storage price...\n")

	cc.Quote(&node.QuoteArgs{
		Refs:      refs,
		Duration:  storeArgs.duration,
		StorageRF: storeArgs.RF,
		MaxPrice:  storeArgs.maxPrice,
	})

	miners := make(map[string]bool)
	select {
	case qr := <-qrc:
		if qr.Err != "" {
			return nil, errors.New(qr.Err)
		}
		var selectn []string
		var options []string
		for k, p := range qr.Quotes {
			options = append(options, fmt.Sprintf("%s - %s", k, p))
		}
		sel := &survey.MultiSelect{
			Message: "Pick miners to store with",
			Options: options,
		}
		survey.AskOne(sel, &selectn)
		if len(selectn) == 0 {
			return nil, errors.New("push aborted")
		}
		total := fil.NewInt(0)

		for _, k := range selectn {
			sp := strings.Split(k, " - ")
			miners[sp[0]] = true
			f, err := fil.ParseFIL(sp[1])
			if err != nil {
				return nil, err
			}
			total = fil.BigAdd(fil.BigInt(f), total)
		}
		exec := false
		conf := &survey.Confirm{
			Message: fmt.Sprintf(
				"Store %s during %s for %s?",
				qr.Ref,
				commArgs.duration,
				fil.FIL(total).String(),
			),
		}
		survey.AskOne(conf, &exec)
		if !exec {
			return nil, errors.New("push aborted")
		}

	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return miners, nil
}
