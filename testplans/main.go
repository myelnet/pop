package main

import "github.com/testground/sdk-go/run"

func main() {
	run.InvokeMap(testcases)
}

var testcases = map[string]interface{}{
	"routing_gossip":        run.InitializedTestCaseFn(runGossip),
	"replication_dispatch":  run.InitializedTestCaseFn(runDispatch),
	"replication_bootstrap": run.InitializedTestCaseFn(runBootstrapSupply),
}
