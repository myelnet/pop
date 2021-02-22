package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/myelnet/go-hop-exchange/node"
	"github.com/peterbourgon/ff/v2/ffcli"
)

var addArgs struct {
	dispatch bool
}

var addCmd = &ffcli.Command{
	Name:       "add",
	ShortUsage: "add <file-path>",
	ShortHelp:  "Add a file to the ipfs blockstore",
	LongHelp: strings.TrimSpace(`

The 'hop add' command opens a given file, chunks it, links it as an ipld graph and 
stores the blocks in the block store.

`),
	Exec: runAdd,
	FlagSet: (func() *flag.FlagSet {
		fs := flag.NewFlagSet("add", flag.ExitOnError)
		fs.BoolVar(&addArgs.dispatch, "dispatch", false, "dispatch the blocks to edge nodes")
		return fs
	})(),
}

func runAdd(ctx context.Context, args []string) error {
	c, cc, ctx, cancel := connect(ctx)
	defer cancel()

	arc := make(chan *node.AddResult, 1)
	cc.SetNotifyCallback(func(n node.Notify) {
		if ar := n.AddResult; ar != nil {
			arc <- ar
		}
	})
	go receive(ctx, cc, c)

	cc.Add(&node.AddArgs{
		Path:     args[0],
		Dispatch: addArgs.dispatch,
	})
	for {
		select {
		case ar := <-arc:
			if ar.Err != "" {
				return errors.New(ar.Err)
			}
			if ar.Cid != "" {
				fmt.Printf("%v\n", ar.Cid)
				if addArgs.dispatch {
					// Let's wait for any feedback from the dispatch
					continue
				}

			}
			if ar.Cache != "" {
				fmt.Printf("cached by peer %s\n", ar.Cache)
				// TODO: wait for a given amount of caches to receive the content
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
