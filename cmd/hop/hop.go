package main

import (
	"fmt"
	"os"

	"github.com/myelnet/go-hop-exchange/cmd/hop/cli"
)

func main() {
	if err := cli.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
