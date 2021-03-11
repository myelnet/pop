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
	"github.com/rs/zerolog/log"
)

var pushArgs struct {
	cacheNum int
	storeNum int
	duration time.Duration
}

var pushCmd = &ffcli.Command{
	Name:       "push",
	ShortUsage: "push <archive-cid>",
	ShortHelp:  "Push a DAG archive to storage",
	LongHelp: strings.TrimSpace(`

The 'hop push' command deploys a DAG archive previously generated using 'hop commit' on the Filecoin storage
with a default level of cashing. By default it will attempt multiple storage deals for 1 month with caching in the initial regions.

`),
	Exec: runPush,
	FlagSet: (func() *flag.FlagSet {
		fs := flag.NewFlagSet("push", flag.ExitOnError)
		fs.IntVar(&pushArgs.cacheNum, "caches", 6, "number of cache providers to dispatch to")
		fs.IntVar(&pushArgs.storeNum, "stores", 6, "number of storage providers to start deals with")
		fs.DurationVar(&pushArgs.duration, "duration", 720*time.Hour, "duration we need the content stored for")
		return fs
	})(),
}

func runPush(ctx context.Context, args []string) error {
	c, cc, ctx, cancel := connect(ctx)
	defer cancel()

	prc := make(chan *node.PushResult, 1)
	cc.SetNotifyCallback(func(n node.Notify) {
		if pr := n.PushResult; pr != nil {
			prc <- pr
		}
	})
	go receive(ctx, cc, c)

	cc.Push(&node.PushArgs{
		//	Commit:   args[0],
		CacheNum: pushArgs.cacheNum,
		StoreNum: pushArgs.storeNum,
		Duration: pushArgs.duration,
	})
	for {
		select {
		case pr := <-prc:
			if pr.Err != "" {
				return errors.New(pr.Err)
			}
			log.Info().Msg(fmt.Sprintf("\n%s", pr.Output))
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
