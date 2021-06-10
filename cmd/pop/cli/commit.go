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
	fil "github.com/myelnet/pop/filecoin"
	"github.com/myelnet/pop/node"
	"github.com/peterbourgon/ff/v3/ffcli"
)

var commArgs struct {
	cacheOnly bool
	cacheRF   int
	storageRF int
	duration  time.Duration
	maxPrice  uint64
	verified  bool
}

var commCmd = &ffcli.Command{
	Name:       "commit",
	ShortUsage: "commit",
	ShortHelp:  "Commit a DAG transaction to storage",
	LongHelp: strings.TrimSpace(`

The 'pop commit' command deploys a DAG archive initialized with one or multiple 'put' on the Filecoin storage
with a given level of cashing. By default it will attempt multiple storage deals for 6 months with caching in the initial regions.

`),
	Exec: runCommit,
	FlagSet: (func() *flag.FlagSet {
		fs := flag.NewFlagSet("commit", flag.ExitOnError)
		fs.IntVar(&commArgs.cacheRF, "cache-rf", 2, "number of cache providers to dispatch to")
		fs.IntVar(&commArgs.storageRF, "storage-rf", 2, "number of storage providers to start deals with")
		fs.DurationVar(&commArgs.duration, "duration", 24*time.Hour*time.Duration(180), "duration we need the content stored for")
		fs.BoolVar(&commArgs.cacheOnly, "cache-only", false, "only dispatch content for caching")
		// MaxStoragePrice is our price ceiling to filter out bad storage miners who charge too much
		fs.Uint64Var(&commArgs.maxPrice, "max-storage-price", uint64(20_000_000_000), "maximum price per byte our node is willing to pay for storage")
		fs.BoolVar(&commArgs.verified, "verified", false, "verified deals should be cheaper but require a data cap associated with the client address used")
		return fs
	})(),
}

func runCommit(ctx context.Context, args []string) error {
	ref := ""
	if len(args) > 0 {
		ref = args[0]
	}

	c, cc, ctx, cancel := connect(ctx)
	defer cancel()

	crc := make(chan *node.CommResult, 1)
	cc.SetNotifyCallback(func(n node.Notify) {
		if cr := n.CommResult; cr != nil {
			crc <- cr
		}
	})
	go receive(ctx, cc, c)

	cc.Commit(&node.CommArgs{
		Ref:       ref,
		CacheOnly: commArgs.cacheOnly,
		CacheRF:   commArgs.cacheRF,
		StorageRF: commArgs.storageRF,
		Duration:  commArgs.duration,
		MaxPrice:  commArgs.maxPrice,
		Verified:  commArgs.verified,
	})
	received := 0
	for {
		select {
		case cr := <-crc:
			if cr.Err != "" {
				return errors.New(cr.Err)
			}
			if cr.Capacity > 0 {
				fmt.Printf("%s left before batch can be stored\n", filecoin.SizeStr(filecoin.NewInt(cr.Capacity)))
				return nil
			}
			if len(cr.Miners) > 0 {
				fmt.Printf("Started storage deals with %s\n", cr.Miners)
				if commArgs.cacheRF > 0 {
					// Wait for the result of our cache dispatch
					fmt.Printf("Dispatching to caches...\n")
					continue
				}
			}
			if len(cr.Caches) > 0 {
				fmt.Printf("Cached by %s\n", cr.Caches)
				received += len(cr.Caches)
			}
			if received == commArgs.cacheRF {
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// TODO: use a separate quote command if getting a quote is useful
func runQuote(ctx context.Context, c net.Conn, cc *node.CommandClient, ref string) (map[string]bool, error) {
	qrc := make(chan *node.QuoteResult, 1)
	cc.SetNotifyCallback(func(n node.Notify) {
		if qr := n.QuoteResult; qr != nil {
			qrc <- qr
		}
	})
	go receive(ctx, cc, c)

	fmt.Printf("Calculating storage price...\n")

	cc.Quote(&node.QuoteArgs{
		Ref:       ref,
		Duration:  commArgs.duration,
		StorageRF: commArgs.storageRF,
		MaxPrice:  commArgs.maxPrice,
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
