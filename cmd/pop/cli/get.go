package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/myelnet/pop/node"
	"github.com/peterbourgon/ff/v2/ffcli"
)

var getArgs struct {
	selector string
	output   string
	timeout  int
	verbose  bool
	miner    string
}

var getCmd = &ffcli.Command{
	Name:       "get",
	ShortUsage: "get <cid>",
	ShortHelp:  "Retrieve content from the network",
	LongHelp: strings.TrimSpace(`

The 'pop get' command retrieves blocks with a given root cid and an optional selector
(defaults retrieves all the linked blocks). Passing an output flag with a path will write the
data to disk. Adding a miner flag will fallback to miner if content is not available on the secondary market.

`),
	Exec: runGet,
	FlagSet: (func() *flag.FlagSet {
		fs := flag.NewFlagSet("get", flag.ExitOnError)
		fs.StringVar(&getArgs.selector, "selector", "all", "select blocks to retrieve for a root cid")
		fs.StringVar(&getArgs.output, "output", "", "write the file to the path")
		fs.IntVar(&getArgs.timeout, "timeout", 60, "timeout before the request should be cancelled by the node (in minutes)")
		fs.BoolVar(&getArgs.verbose, "verbose", false, "print the state transitions")
		fs.StringVar(&getArgs.miner, "miner", "", "ask storage miner and use as fallback if network does not have the content")
		return fs
	})(),
}

func runGet(ctx context.Context, args []string) error {
	c, cc, ctx, cancel := connect(ctx)
	defer cancel()

	grc := make(chan *node.GetResult)
	cc.SetNotifyCallback(func(n node.Notify) {
		if gr := n.GetResult; gr != nil {
			grc <- gr
		}
	})
	go receive(ctx, cc, c)

	cc.Get(&node.GetArgs{
		Cid:     args[0],
		Timeout: getArgs.timeout,
		Sel:     getArgs.selector,
		Out:     getArgs.output,
		Verbose: getArgs.verbose,
		Miner:   getArgs.miner,
	})

	for {
		select {
		case gr := <-grc:
			if gr.Err != "" {
				return errors.New(gr.Err)
			}
			if gr.DealID != "" {
				fmt.Printf("Started retrieval deal %s for a total of %s (%s/b)\n", gr.DealID, gr.TotalPrice, gr.PricePerByte)
				continue
			}

			fmt.Printf("Completed transfer")
			fmt.Printf("%fs %fs %fs", gr.DiscLatSeconds, gr.TransLatSeconds, gr.DiscLatSeconds+gr.TransLatSeconds)
			return nil
		case <-ctx.Done():
			return fmt.Errorf("Get operation timed out")
		}
	}
}
