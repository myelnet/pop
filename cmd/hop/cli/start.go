package cli

import (
	"context"
	"encoding/base64"
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
	temp        bool
	peer        string
	filEndpoint string
	filToken    string
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
			"/ip4/3.22.169.56/tcp/4001/ipfs/12D3KooWQzS81gjFLMEoa9cvrEMAP3564CP1p8Ce5ZZvV9nsy9Uz",
			"bootstrap peer to discover others",
		)
		fs.StringVar(&startArgs.filEndpoint, "fil-endpoint", "", "endpoint to reach a filecoin api")
		fs.StringVar(&startArgs.filToken, "fil-token", "", "token to authorize filecoin api access")

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

	var filToken string
	if startArgs.filToken != "" {
		filToken = base64.StdEncoding.EncodeToString([]byte(startArgs.filToken))
	}

	opts := node.Options{
		RepoPath:       rpath,
		SocketPath:     "hopd.sock",
		BootstrapPeers: []string{startArgs.peer},
		FilEndpoint:    startArgs.filEndpoint,
		FilToken:       filToken,
	}

	err = node.Run(ctx, opts)
	if err != nil && err != context.Canceled {
		log.Error().Err(err).Msg("node.Run")
		return err
	}
	return nil
}
