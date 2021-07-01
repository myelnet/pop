package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/myelnet/pop/node"
	"github.com/peterbourgon/ff/v3/ffcli"
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

	filePath := args[0]
	isAbsPath := filepath.IsAbs(filePath)
	if !isAbsPath {
		// if path is relative, convert it to absolute
		mydir, err := os.Getwd()
		if err != nil {
			return err
		}
		filePath = filepath.Join(mydir, filePath)
	}

	cc.Put(&node.PutArgs{
		Path:      filePath,
		ChunkSize: putArgs.chunkSize,
	})

	buf := bytes.NewBuffer(nil)
	w := new(tabwriter.Writer)
	w.Init(buf, 0, 4, 2, ' ', 0)

	i := 1

loop:
	for {
		select {
		case pr := <-prc:
			if pr.Err != "" {
				return errors.New(pr.Err)
			}

			if i == 1 {
				fmt.Printf("==> Put in transaction with root %s\n", pr.RootCid)
				fmt.Printf("--\n")
			}

			fmt.Fprintf(w, "%s\t%s\n", pr.Key, pr.Size)

			if i == pr.Len {
				fmt.Fprintf(w, "--\t\n")
				fmt.Fprintf(w, "Total size\t%s\n", pr.TotalSize)
				break loop
			}
			i++
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	w.Flush()
	fmt.Printf("%s\n", buf.String())
	return nil
}
