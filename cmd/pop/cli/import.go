package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/myelnet/pop/node"
	"github.com/peterbourgon/ff/v3/ffcli"
)

var importArgs struct {
	cacheRF    int
	attempts   int
	backoffMin time.Duration
	peers      string
}

var importCmd = &ffcli.Command{
	Name:       "import",
	ShortUsage: "import <path>",
	ShortHelp:  "Import a CAR file to the blockstore",
	LongHelp: strings.TrimSpace(`

The 'pop import <path-to-car>' directly imports archived DAGs to the blockstore.

`),
	Exec: runImport,
	FlagSet: (func() *flag.FlagSet {
		fs := flag.NewFlagSet("import", flag.ExitOnError)
		fs.IntVar(&importArgs.cacheRF, "cache-rf", 0, "number of providers to replicate the content to")
		fs.IntVar(&importArgs.attempts, "attempts", 0, "number of attempts until we reach the desired replication factor. 0 will be ignored")
		fs.DurationVar(&importArgs.backoffMin, "backoff-min", 3*time.Minute, "minimum delay to wait before trying again")
		fs.StringVar(&importArgs.peers, "peers", "", "list of comma separated peer ids to include in the replication set")
		return fs
	})(),
}

func runImport(ctx context.Context, args []string) error {
	c, cc, ctx, cancel := connect(ctx)
	defer cancel()

	irc := make(chan *node.ImportResult, 6)
	cc.SetNotifyCallback(func(n node.Notify) {
		if ir := n.ImportResult; ir != nil {
			irc <- ir
		}
	})
	go receive(ctx, cc, c)

	cc.Import(&node.ImportArgs{
		Path:       args[0],
		CacheRF:    importArgs.cacheRF,
		Attempts:   importArgs.attempts,
		BackoffMin: importArgs.backoffMin,
		Peers:      strings.Split(importArgs.peers, ","),
	})

	for {
		select {
		case ir := <-irc:
			if ir.Err != "" {
				return errors.New(ir.Err)
			}

			if len(ir.Caches) > 0 {
				fmt.Printf("Cached by %s\n", ir.Caches)
			}

			if len(ir.Roots) > 0 {
				fmt.Println("Imported car with roots", ir.Roots)
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
