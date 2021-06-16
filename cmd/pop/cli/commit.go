package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/myelnet/pop/node"
	"github.com/peterbourgon/ff/v3/ffcli"
)

var commArgs struct {
	cacheOnly bool
	cacheRF   int
	storageRF int
}

var commCmd = &ffcli.Command{
	Name:       "commit",
	ShortUsage: "commit",
	ShortHelp:  "Commit a DAG transaction",
	LongHelp: strings.TrimSpace(`

The 'pop commit' command deploys a DAG archive initialized with one or multiple 'put' on the Filecoin storage
with a given level of cashing. By default it will attempt multiple storage deals for 6 months with caching in the initial regions.

`),
	Exec: runCommit,
	FlagSet: (func() *flag.FlagSet {
		fs := flag.NewFlagSet("commit", flag.ExitOnError)
		fs.IntVar(&commArgs.cacheRF, "cache-rf", 2, "number of cache providers to dispatch to")
		return fs
	})(),
}

func runCommit(ctx context.Context, args []string) error {
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
		CacheRF: commArgs.cacheRF,
	})
	for {
		select {
		case cr := <-crc:
			if cr.Err != "" {
				return errors.New(cr.Err)
			}
			if len(cr.Caches) > 0 {
				fmt.Printf("Cached by %s\n", cr.Caches)
			}
			if cr.Ref != "" {
				fmt.Printf("==> Committed transaction %s (%s)\n", cr.Ref, cr.Size)
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
