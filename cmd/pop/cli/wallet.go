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

var listKeys = &ffcli.Command{
	Name:       "list",
	ShortUsage: "wallet list",
	ShortHelp:  "List all the addresses you have",
	Exec:       runListKeys,
}

var export = &ffcli.Command{
	Name:       "export",
	ShortUsage: "wallet export <address> </your/path>",
	ShortHelp:  "Export your private key",
	Exec:       runExport,
}

var pay = &ffcli.Command{
	Name:       "pay",
	ShortUsage: "wallet pay <from> <to> <amount>",
	ShortHelp:  "Make a transaction in FIL",
	Exec:       runPay,
}

var walletCmd = &ffcli.Command{
	Name:      "wallet",
	ShortHelp: "Manage your wallet",
	LongHelp: strings.TrimSpace(`

The 'pop wallet' command is a multipurpose wallet command used for managing your private key & FIL address.
You can list or export your addresses, as well as paying to a FIL address.

`),
	Exec: func(context.Context, []string) error {
		return flag.ErrHelp
	},
	FlagSet:     flag.NewFlagSet("wallet", flag.ExitOnError),
	Subcommands: []*ffcli.Command{listKeys, export, pay},
}

func runListKeys(ctx context.Context, args []string) error {
	c, cc, ctx, cancel := connect(ctx)
	defer cancel()

	keyResults := make(chan *node.WalletResult, 1)
	cc.SetNotifyCallback(func(n node.Notify) {
		if sr := n.WalletResult; sr != nil {
			keyResults <- sr
		}
	})
	go receive(ctx, cc, c)

	cc.WalletListKeys(&node.WalletListArgs{})

	select {
	case kr := <-keyResults:
		if kr.Err != "" {
			return errors.New(kr.Err)
		}

		keys := strings.Replace(strings.Join(kr.Addresses, "\n ==> "), kr.DefaultAddress, kr.DefaultAddress+" (default)", 1)

		fmt.Printf("List of all your keys : \n ==> %s \n", keys)
		return nil

	case <-ctx.Done():
		return ctx.Err()
	}
}

func runExport(ctx context.Context, args []string) error {
	if len(args) < 2 {
		return errors.New("incorrect number of args, see usage")
	}

	address := args[0]
	outputPath := args[1]

	c, cc, ctx, cancel := connect(ctx)
	defer cancel()

	keyResults := make(chan *node.WalletResult, 1)
	cc.SetNotifyCallback(func(n node.Notify) {
		if sr := n.WalletResult; sr != nil {
			keyResults <- sr
		}
	})
	go receive(ctx, cc, c)

	cc.WalletExport(&node.WalletExportArgs{
		Address:    address,
		OutputPath: outputPath,
	})

	select {
	case kr := <-keyResults:
		if kr.Err != "" {
			return errors.New(kr.Err)
		}

		fmt.Printf("Successfully exported key %s to %s\n", address, outputPath)
		return nil

	case <-ctx.Done():
		return ctx.Err()
	}
}

func runPay(ctx context.Context, args []string) error {
	if len(args) < 3 {
		return errors.New("incorrect number of args, see usage")
	}

	from := args[0]
	to := args[1]
	amount := args[2]

	c, cc, ctx, cancel := connect(ctx)
	defer cancel()

	keyResults := make(chan *node.WalletResult, 1)
	cc.SetNotifyCallback(func(n node.Notify) {
		if sr := n.WalletResult; sr != nil {
			keyResults <- sr
		}
	})
	go receive(ctx, cc, c)

	cc.WalletPay(&node.WalletPayArgs{
		From:   from,
		To:     to,
		Amount: amount,
	})

	select {
	case kr := <-keyResults:
		if kr.Err != "" {
			return errors.New(kr.Err)
		}

		fmt.Printf("Successfully paid %s from %s to %s\n", amount, from, to)
		return nil

	case <-ctx.Done():
		return ctx.Err()
	}
}
