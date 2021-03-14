package cli

import (
	"context"
	"flag"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/myelnet/pop/node"
	"github.com/peterbourgon/ff/v2/ffcli"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Run runs the CLI. The args do not include the binary name.
func Run(args []string) error {
	if len(args) == 1 && (args[0] == "-V" || args[0] == "--version") {
		args = []string{"version"}
	}
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	rootfs := flag.NewFlagSet("pop", flag.ExitOnError)

	rootCmd := &ffcli.Command{
		Name:       "pop",
		ShortUsage: "pop subcommand [flags]",
		ShortHelp:  "Content delivery network for the web3.0.",
		LongHelp: strings.TrimSpace(`
This CLI is still under active development. Commands and flags will
change in the future. To get started run 'pop init' to create a new repo for
storing your data.
`),
		Subcommands: []*ffcli.Command{
			initCmd,
			startCmd,
			pingCmd,
			addCmd,
			statusCmd,
			packCmd,
			quoteCmd,
			pushCmd,
			getCmd,
		},
		FlagSet: rootfs,
		Exec:    func(context.Context, []string) error { return flag.ErrHelp },
	}

	if err := rootCmd.Parse(args); err != nil {
		return err
	}

	err := rootCmd.Run(context.Background())
	if err == flag.ErrHelp {
		return nil
	}
	return err
}

func connect(ctx context.Context) (net.Conn, *node.CommandClient, context.Context, context.CancelFunc) {
	c, err := node.SocketConnect()
	if err != nil {
		log.Fatal().Msg("Unable to connect")
	}

	clientToServer := func(b []byte) {
		node.WriteMsg(c, b)
	}

	ctx, cancel := context.WithCancel(ctx)

	go func() {
		interrupt := make(chan os.Signal, 1)
		signal.Notify(interrupt, syscall.SIGINT, syscall.SIGTERM)
		<-interrupt
		c.Close()
		cancel()
	}()

	cc := node.NewCommandClient(clientToServer)
	return c, cc, ctx, cancel
}

// receive backend messages on conn and push them into cc.
func receive(ctx context.Context, cc *node.CommandClient, conn net.Conn) {
	defer conn.Close()
	for ctx.Err() == nil {
		msg, err := node.ReadMsg(conn)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Error().Err(err).Msg("ReadMsg")
			break
		}
		cc.GotNotifyMsg(msg)
	}
}
