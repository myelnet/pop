package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/myelnet/pop/node"
	"github.com/peterbourgon/ff/v2/ffcli"
)

var pingCmd = &ffcli.Command{
	Name:       "ping",
	ShortUsage: "ping <peer-id?>",
	ShortHelp:  "Ping the local daemon or a given peer",
	LongHelp: strings.TrimSpace(`

The 'pop ping' command is a multipurpose ping request used mostly for debugging.
It can be used to check info about the local running daemon, a connected provider or even a storage miner.

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

	var addr string
	if len(args) > 0 {
		addr = args[0]
	}
	cc.Ping(addr)
	timer := time.NewTimer(10 * time.Second)
	select {
	case <-timer.C:
		fmt.Printf("timeout waiting for ping reply\n")
	case pr := <-prc:
		timer.Stop()

		anyPong = true
		if pr.Err != "" {
			return fmt.Errorf(pr.Err)
		}
		fmt.Printf(`
PeerID         %s
Addresses      %s
Peers          %s
Latency (s)    %f
Version        %s
		`, pr.ID, pr.Addrs, pr.Peers, pr.LatencySeconds, pr.Version)

	case <-ctx.Done():
		return ctx.Err()
	}
	if !anyPong {
		return fmt.Errorf("no reply")
	}
	return nil
}
