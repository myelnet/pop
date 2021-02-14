package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/myelnet/go-hop-exchange/node"
	"github.com/peterbourgon/ff/v2/ffcli"
	"github.com/rs/zerolog/log"
)

var (
	ErrTokenNotFound = errors.New("no token found")
	ErrNoTokenOnOS   = errors.New("no token on " + runtime.GOOS)
)

// Run runs the CLI. The args do not include the binary name.
func Run(args []string) error {
	if len(args) == 1 && (args[0] == "-V" || args[0] == "--version") {
		args = []string{"version"}
	}

	rootfs := flag.NewFlagSet("hop", flag.ExitOnError)

	rootCmd := &ffcli.Command{
		Name:       "hop",
		ShortUsage: "hop subcommand [flags]",
		ShortHelp:  "The easiest, fastes way to exchange content with IPFS.",
		LongHelp: strings.TrimSpace(`
This CLI is still under active development. Commands and flags will
change in the future.
`),
		Subcommands: []*ffcli.Command{
			pingCmd,
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

func connect(ctx context.Context) (net.Conn, *node.CommandClient, context.Context, context.CancelFunc) {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to get home dir")
	}
	c, err := net.Dial("unix", filepath.Join(home, "hopd.sock"))
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect")
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
