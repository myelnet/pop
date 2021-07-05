package cli

import (
	"context"
	"fmt"
	"github.com/myelnet/pop/node"
	"github.com/peterbourgon/ff/v3/ffcli"
)

var offCmd = &ffcli.Command{
	Name:       "off",
	ShortUsage: "off",
	ShortHelp:  "Gracefully shutdown the Pop daemon",
	LongHelp:   "The 'pop off' command gracefully shutdown the Pop daemon.",
	Exec:       runOff,
}

func runOff(ctx context.Context, args []string) error {
	c, cc, ctx, cancel := connect(ctx)
	defer cancel()

	prc := make(chan *node.OffResult, 1)
	cc.SetNotifyCallback(func(n node.Notify) {
		if pr := n.OffResult; pr != nil {
			prc <- pr
		}
	})
	go receive(ctx, cc, c)

	cc.Off()

	select {
	case <-prc:
		fmt.Println("Successfully sent Graceful Shutdown command")

	case <-ctx.Done():
		return ctx.Err()
	}

	return nil
}
