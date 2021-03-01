package cli

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
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
	temp         bool
	peer         string
	filEndpoint  string
	filToken     string
	filTokenType string
	privKeyPath  string
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
			"/ip4/3.14.73.230/tcp/4001/ipfs/12D3KooWQtnktGLsDc3fgHW4vrsCVR15oC1Vn6Wy6Moi65pL6q2a",
			"bootstrap peer to discover others",
		)
		fs.StringVar(&startArgs.filEndpoint, "fil-endpoint", "", "endpoint to reach a filecoin api")
		fs.StringVar(&startArgs.filToken, "fil-token", "", "token to authorize filecoin api access")
		fs.StringVar(&startArgs.privKeyPath, "privkey", "", "path to private key to use by default")
		fs.StringVar(&startArgs.filTokenType, "fil-token-type", "Bearer", "auth token type")

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
		// Basic auth requires base64 encoding and Infura api provides unencoded strings
		if startArgs.filTokenType == "Basic" {
			filToken = base64.StdEncoding.EncodeToString([]byte(startArgs.filToken))
		} else {
			filToken = startArgs.filToken
		}
		filToken = fmt.Sprintf("%s %s", startArgs.filTokenType, filToken)
	}

	var privKey string
	if startArgs.privKeyPath != "" {
		fdata, err := ioutil.ReadFile(startArgs.privKeyPath)
		if err != nil {
			log.Error().Err(err).Msg("failed to read private key")
		} else {
			privKey = strings.TrimSpace(string(fdata))
		}
	}

	opts := node.Options{
		RepoPath:       rpath,
		SocketPath:     "hopd.sock",
		BootstrapPeers: []string{startArgs.peer},
		FilEndpoint:    startArgs.filEndpoint,
		FilToken:       filToken,
		PrivKey:        privKey,
	}

	err = node.Run(ctx, opts)
	if err != nil && err != context.Canceled {
		log.Error().Err(err).Msg("node.Run")
		return err
	}
	return nil
}
