package main

import (
	"log"
	"os"

	"github.com/rs/zerolog"
	"github.com/testground/sdk-go/run"
)

func main() {
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	run.InvokeMap(testcases)
}

var testcases = map[string]interface{}{
	"routing_gossip":        run.InitializedTestCaseFn(runGossip),
	"replication_dispatch":  run.InitializedTestCaseFn(runDispatch),
	"replication_bootstrap": run.InitializedTestCaseFn(runBootstrapSupply),
}
