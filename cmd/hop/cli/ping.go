package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/myelnet/go-hop-exchange/node"
	"github.com/peterbourgon/ff/v2/ffcli"
	"github.com/rs/zerolog/log"
)

var pingCmd = &ffcli.Command{
	Name:       "ping",
	ShortUsage: "ping",
	ShortHelp:  "Ping the daemon to see if it's active",
	LongHelp: strings.TrimSpace(`
Here is some more information about this command

`),
	Exec: runPing,
}

func runPing(ctx context.Context, args []string) error {
	c, cc, ctx, cancel := connect(ctx)
	defer cancel()

	prc := make(chan *node.PingResult, 1)
	cc.SetNotifyCallback(func(n node.Notify) {

		if pr := n.PingResult; pr != nil {
			prc <- pr
		}
	})
	go receive(ctx, cc, c)

	anyPong := false
	cc.Ping("any")
	timer := time.NewTimer(5 * time.Second)
	select {
	case <-timer.C:
		fmt.Printf("timeout waiting for ping reply\n")
	case pr := <-prc:
		timer.Stop()

		anyPong = true
		log.Info().Interface("addrs", pr.ListenAddr).Msg("pong")

		time.Sleep(time.Second)

	case <-ctx.Done():
		return ctx.Err()
	}
	if !anyPong {
		return fmt.Errorf("no reply")
	}
	return nil
}
