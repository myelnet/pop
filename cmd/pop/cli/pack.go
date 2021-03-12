package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/myelnet/pop/node"
	"github.com/peterbourgon/ff/v2/ffcli"
)

var packCmd = &ffcli.Command{
	Name:      "pack",
	ShortHelp: "Pack the current index into a DAG archive",
	LongHelp: strings.TrimSpace(`

The 'pop pack' command creates a single DAG with the current index of staged DAGs. 
It archives it into a CAR file ready for storage.

`),
	Exec: runCommit,
}

func runCommit(ctx context.Context, args []string) error {
	c, cc, ctx, cancel := connect(ctx)
	defer cancel()

	prc := make(chan *node.PackResult, 1)
	cc.SetNotifyCallback(func(n node.Notify) {
		if pr := n.PackResult; pr != nil {
			prc <- pr
		}
	})
	go receive(ctx, cc, c)

	cc.Pack(&node.PackArgs{})
	select {
	case pr := <-prc:
		if pr.Err != "" {
			return errors.New(pr.Err)
		}
		fmt.Printf(pr.Output)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
