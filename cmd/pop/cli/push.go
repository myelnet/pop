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
	noCache   bool
	cacheOnly bool
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
		fs.BoolVar(&pushArgs.noCache, "no-cache", false, "prevents node from dispatching content to cache providers")
		fs.BoolVar(&pushArgs.cacheOnly, "cache-only", false, "only dispatch content for caching")
		return fs
	})(),
}

func runPush(ctx context.Context, args []string) error {
	if pushArgs.noCache && pushArgs.cacheOnly {
		return errors.New("no-cache and cache-only are incompatible")
	}
	c, cc, ctx, cancel := connect(ctx)
	defer cancel()

	prc := make(chan *node.PushResult, 1)
	cc.SetNotifyCallback(func(n node.Notify) {
		if pr := n.PushResult; pr != nil {
			prc <- pr
		}
	})
	go receive(ctx, cc, c)

	ref := ""
	if len(args) > 0 {
		ref = args[0]
	}
	cc.Push(&node.PushArgs{
		Ref:       ref,
		NoCache:   pushArgs.noCache,
		CacheOnly: pushArgs.cacheOnly,
		CacheRF:   pushArgs.cacheRF,
		StorageRF: pushArgs.storageRF,
		Duration:  pushArgs.duration,
	})
	for {
		select {
		case pr := <-prc:
			if pr.Err != "" {
				return errors.New(pr.Err)
			}
			if len(pr.Miners) > 0 {
				fmt.Printf("Started storage deals with %s\n", pr.Miners)
				if !pushArgs.noCache && pushArgs.cacheRF > 0 {
					// Wait for the result of our cache dispatch
					fmt.Printf("Dispatching to caches...\n")
					continue
				}
			}
			if len(pr.Caches) > 0 {
				fmt.Printf("Cached by %s\n", pr.Caches)
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
