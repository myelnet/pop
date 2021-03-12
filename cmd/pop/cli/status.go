package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/myelnet/pop/node"
	"github.com/peterbourgon/ff/v2/ffcli"
	"github.com/rs/zerolog/log"
)

var statusCmd = &ffcli.Command{
	Name:      "status",
	ShortHelp: "Print the state of the working DAG",
	LongHelp: strings.TrimSpace(`

The 'pop status' command prints all the files that have been added to the blockstore. Files that have
been chunked and staged in the blockstore but not yet committed into a Car to be pushed to the network.

`),
	Exec: runStatus,
}

func runStatus(ctx context.Context, args []string) error {
	c, cc, ctx, cancel := connect(ctx)
	defer cancel()

	src := make(chan *node.StatusResult, 1)
	cc.SetNotifyCallback(func(n node.Notify) {
		if sr := n.StatusResult; sr != nil {
			src <- sr
		}
	})
	go receive(ctx, cc, c)

	cc.Status(&node.StatusArgs{})
	select {
	case sr := <-src:
		if sr.Err != "" {
			return errors.New(sr.Err)
		}
		if sr.Output == "" {
			log.Info().Msg("nothing to commit, workdag clean")
			return nil
		}
		log.Info().Msg(fmt.Sprintf("\n%s", sr.Output))
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
