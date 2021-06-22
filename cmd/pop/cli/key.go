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

var keyCmd = &ffcli.Command{
	Name:      "key",
	ShortHelp: "Manage your keys",
	LongHelp: strings.TrimSpace(`

Manage your keys

`),
	Exec: runKey,
	FlagSet: (func() *flag.FlagSet {
		fs := flag.NewFlagSet("key", flag.ExitOnError)
		fs.StringVar(&startArgs.Bootstrap, "address", "", "your FIL address you want to export")
		fs.StringVar(&startArgs.FilEndpoint, "output-path", "", "path where your address will be exported")
		return fs
	})(),
}

func runKey(ctx context.Context, args []string) error {
	c, cc, ctx, cancel := connect(ctx)
	defer cancel()

	keyResults := make(chan *node.KeyResult, 1)
	cc.SetNotifyCallback(func(n node.Notify) {
		if sr := n.KeyResult; sr != nil {
			keyResults <- sr
		}
	})
	go receive(ctx, cc, c)

	cc.Key(&node.KeyArgs{})
	select {
	case kr := <-keyResults:
		if kr.Err != "" {
			return errors.New(kr.Err)
		}

		if kr.Address == "" {
			fmt.Printf("Missing Key.\n")
			return nil
		}

		fmt.Printf("Fil Key : %s\n", kr.Address)
		return nil

	case <-ctx.Done():
		return ctx.Err()
	}
}
