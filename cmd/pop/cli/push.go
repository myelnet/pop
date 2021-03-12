package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/myelnet/pop/node"
	"github.com/peterbourgon/ff/v2/ffcli"
)

var pushArgs struct {
	cacheRF   int
	storageRF int
	duration  time.Duration
}

var pushCmd = &ffcli.Command{
	Name:       "push",
	ShortUsage: "push <archive-cid>",
	ShortHelp:  "Push a DAG archive to storage",
	LongHelp: strings.TrimSpace(`

The 'pop push' command deploys a DAG archive previously generated using 'pop commit' on the Filecoin storage
with a default level of cashing. By default it will attempt multiple storage deals for 6 months with caching in the initial regions. Passing no commit CID will result in selecting the last generated commit.

`),
	Exec: runPush,
	FlagSet: (func() *flag.FlagSet {
		fs := flag.NewFlagSet("push", flag.ExitOnError)
		fs.IntVar(&pushArgs.cacheRF, "cache-rf", 6, "number of cache providers to dispatch to")
		fs.IntVar(&pushArgs.storageRF, "storage-rf", 6, "number of storage providers to start deals with")
		fs.DurationVar(&pushArgs.duration, "duration", 24*time.Hour*time.Duration(180), "duration we need the content stored for")
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

	com := ""
	if len(args) > 0 {
		com = args[0]
	}
	cc.Push(&node.PushArgs{
		Commit:    com,
		CacheRF:   pushArgs.cacheRF,
		StorageRF: pushArgs.storageRF,
		Duration:  pushArgs.duration,
	})
	select {
	case pr := <-prc:
		if pr.Err != "" {
			return errors.New(pr.Err)
		}
		fmt.Printf("\n%s", pr.Output)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
