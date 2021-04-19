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

var putArgs struct {
	chunkSize int
}

var putCmd = &ffcli.Command{
	Name:       "put",
	ShortUsage: "put <file-path>",
	ShortHelp:  "Put a file into an exchange transaction for storage",
	LongHelp: strings.TrimSpace(`

The 'pop put' command opens a given file, chunks it, links it as an ipld DAG and 
stores the blocks in the block store. The DAG is then staged in a pending or new storage transaction.

`),
	Exec: runPut,
	FlagSet: (func() *flag.FlagSet {
		fs := flag.NewFlagSet("put", flag.ExitOnError)
		fs.IntVar(&putArgs.chunkSize, "chunk-size", 1024, "chunk size in bytes")
		return fs
	})(),
}

func runPut(ctx context.Context, args []string) error {
	c, cc, ctx, cancel := connect(ctx)
	defer cancel()

	prc := make(chan *node.PutResult, 1)
	cc.SetNotifyCallback(func(n node.Notify) {
		if pr := n.PutResult; pr != nil {
			prc <- pr
		}
	})
	go receive(ctx, cc, c)

	cc.Put(&node.PutArgs{
		Path:      args[0],
		ChunkSize: putArgs.chunkSize,
	})
	select {
	case pr := <-prc:
		if pr.Err != "" {
			return errors.New(pr.Err)
		}
		fmt.Printf("==> Put new file in tx with root %s\n", pr.Root)
		fmt.Printf("%s  %s  %s  %d blk\n", args[0], pr.Cid, pr.Size, pr.NumBlocks)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
