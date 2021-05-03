package main

import (
	"context"
	"math/rand"
	"time"

	"github.com/testground/sdk-go/network"
	"github.com/testground/sdk-go/runtime"
)

func durationParam(runenv *runtime.RunEnv, name string) time.Duration {
	d, err := time.ParseDuration(runenv.StringParam(name))
	if err != nil {
		panic(err)
	}
	return d
}

func shapeTraffic(ctx context.Context, runenv *runtime.RunEnv, netclient *network.Client) error {
	minl := durationParam(runenv, "min_latency")
	maxl := durationParam(runenv, "max_latency")
	l := minl + time.Duration(rand.Float64()*float64(maxl-minl))

	minj := durationParam(runenv, "min_jitter")
	maxj := durationParam(runenv, "max_jitter")
	j := minj + time.Duration(rand.Float64()*float64(maxj-minj))

	minbw := runenv.IntParam("min_bandwidth")
	maxbw := runenv.IntParam("max_bandwidth")
	bw := minbw + rand.Intn(maxbw-minbw)

	config := &network.Config{
		Network: "default",
		Enable:  true,
		Default: network.LinkShape{
			Latency:   l,
			Bandwidth: uint64(bw),
			Jitter:    j,
		},
		CallbackState: "network-configured",
	}
	<-time.After(time.Duration(rand.Intn(1000)) * time.Millisecond)
	err := netclient.ConfigureNetwork(ctx, config)
	if err != nil {
		return err
	}
	runenv.RecordMessage("egress: %d latency (%d jitter) and %db bandwidth", l, j, bw)
	return nil
}
