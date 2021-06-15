package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/myelnet/pop/filecoin"
	"github.com/myelnet/pop/node"
	"github.com/peterbourgon/ff/v3/ffcli"
)

var listCmd = &ffcli.Command{
	Name:      "list",
	ShortHelp: "List all content indexed in this pop",
	LongHelp: strings.TrimSpace(`

The 'pop list' command prints root CIDs for all the indexed content currently provided by this pop. Content is
indexed by DAG root so usage frequencies is compiled by root too.

`),
	Exec: runList,
}

func runList(ctx context.Context, args []string) error {
	c, cc, ctx, cancel := connect(ctx)
	defer cancel()

	lrc := make(chan *node.ListResult)
	cc.SetNotifyCallback(func(n node.Notify) {
		if lr := n.ListResult; lr != nil {
			lrc <- lr
			if lr.Last {
				close(lrc)
			}
		}
	})
	go receive(ctx, cc, c)

	cc.List(&node.ListArgs{})
	for ref := range lrc {
		if ref.Err != "" {
			return errors.New(ref.Err)
		}
		fmt.Printf("Tx %s %s %d\n", ref.Root, filecoin.SizeStr(filecoin.NewInt(uint64(ref.Size))), ref.Freq)
	}
	return nil
}
