package main

import (
	"context"
	"flag"
	"io/ioutil"
	"os"
	"os/signal"
	"syscall"

	"github.com/myelnet/go-hop-exchange/node"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

var args struct {
	temp bool
}

func main() {

	flag.BoolVar(&args.temp, "temp", true, "create a temporary datastore for testing")

	flag.Parse()

	if err := run(); err != nil {
		os.Exit(1)
	}
}

func run() error {
	var err error
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	rpath := ""
	if args.temp {
		rpath, err = ioutil.TempDir("", "repo")
		if err != nil {
			return err
		}
	}

	ctx, cancel := context.WithCancel(context.Background())

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
		RepoPath: rpath,
	}

	err = node.Run(ctx, opts)
	if err != nil && err != context.Canceled {
		log.Error().Err(err).Msg("node.Run")
		return err
	}
	return nil
}
