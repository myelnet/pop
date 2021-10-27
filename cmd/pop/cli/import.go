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

var importArgs struct {
	cacheRF int
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
		return fs
	})(),
}

func runImport(ctx context.Context, args []string) error {
	c, cc, ctx, cancel := connect(ctx)
	defer cancel()

	irc := make(chan *node.ImportResult, 1)
	cc.SetNotifyCallback(func(n node.Notify) {
		if ir := n.ImportResult; ir != nil {
			irc <- ir
		}
	})
	go receive(ctx, cc, c)

	cc.Import(&node.ImportArgs{Path: args[0], CacheRF: importArgs.cacheRF})
	select {
	case ir := <-irc:
		if ir.Err != "" {
			return errors.New(ir.Err)
		}

		fmt.Println("Imported car with roots", ir.Roots)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
