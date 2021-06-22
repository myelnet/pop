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

var exportArgs struct {
	Address    string
	OutputPath string
}

var payArgs struct {
	From   string
	To     string
	Amount string
}

var export = &ffcli.Command{
	Name:       "export",
	ShortUsage: "wallet export",
	ShortHelp:  "",
	FlagSet: (func() *flag.FlagSet {
		fs := flag.NewFlagSet("wallet export", flag.ExitOnError)
		fs.StringVar(&exportArgs.Address, "address", "", "the FIL address you want to export")
		fs.StringVar(&exportArgs.OutputPath, "output-path", "", "path where your private key will be exported")
		return fs
	})(),
	Exec: func(ctx context.Context, args []string) error {
		return runExport(ctx)
	},
}

var pay = &ffcli.Command{
	Name:       "pay",
	ShortUsage: "wallet pay",
	ShortHelp:  "",
	FlagSet: (func() *flag.FlagSet {
		fs := flag.NewFlagSet("wallet pay", flag.ExitOnError)

		return fs
	})(),
	Exec: func(ctx context.Context, args []string) error {
		return runPay(ctx)
	},
}

var walletCmd = &ffcli.Command{
	Name:      "wallet",
	ShortHelp: "Manage your keys",
	LongHelp:  strings.TrimSpace(` Manage your keys`),
	Exec: func(context.Context, []string) error {
		return flag.ErrHelp
	},
	FlagSet:     flag.NewFlagSet("wallet", flag.ExitOnError),
	Subcommands: []*ffcli.Command{export, pay},
}

func runExport(ctx context.Context) error {
	c, cc, ctx, cancel := connect(ctx)
	defer cancel()

	keyResults := make(chan *node.WalletResult, 1)
	cc.SetNotifyCallback(func(n node.Notify) {
		if sr := n.WalletResult; sr != nil {
			keyResults <- sr
		}
	})
	go receive(ctx, cc, c)

	cc.Wallet(&node.KeyArgs{
		Address:    exportArgs.Address,
		OutputPath: exportArgs.OutputPath,
	})

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

func runPay(ctx context.Context) error {
	fmt.Println(">>>>> IN PAY")
	return nil
}
