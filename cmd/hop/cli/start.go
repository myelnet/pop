package cli

import (
	"context"
	"flag"
	"io/ioutil"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/myelnet/go-hop-exchange/node"
	"github.com/peterbourgon/ff/v2/ffcli"
	"github.com/rs/zerolog/log"
)

var startArgs struct {
	temp bool
	peer string
}

var startCmd = &ffcli.Command{
	Name:       "start",
	ShortUsage: "start",
	ShortHelp:  "Starts an IPFS daemon",
	LongHelp: strings.TrimSpace(`

The 'hop start' command starts an IPFS daemon service.

`),
	Exec: runStart,
	FlagSet: (func() *flag.FlagSet {
		fs := flag.NewFlagSet("start", flag.ExitOnError)
		fs.BoolVar(&startArgs.temp, "temp", true, "create a temporary datastore for testing")
		fs.StringVar(
			&startArgs.peer,
			"peer",
			"/ip4/3.22.169.56/tcp/4001/ipfs/12D3KooWBUvfXFNJiAisGo1N8Jx8HBbMPKkZtCNepyXZKKz8Z1Qs",
			"bootstrap peer to discover others",
		)

		return fs
	})(),
}

func runStart(ctx context.Context, args []string) error {
	var err error

	rpath := ""
	if startArgs.temp {
		rpath, err = ioutil.TempDir("", "repo")
		if err != nil {
			return err
		}
	}

	ctx, cancel := context.WithCancel(ctx)

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, syscall.SIGINT, syscall.SIGTERM)

	signal.Ignore(syscall.SIGPIPE)
	go func() {
		select {
		case s := <-interrupt:
			log.Info().Str("signal", s.String()).Msg("shutting down")
			cancel()
		case <-ctx.Done():
		}
	}()

	opts := node.Options{
		RepoPath:      rpath,
		SocketPath:    "hopd.sock",
		BootstrapPeer: startArgs.peer,
	}

	err = node.Run(ctx, opts)
	if err != nil && err != context.Canceled {
		log.Error().Err(err).Msg("node.Run")
		return err
	}
	return nil
}
