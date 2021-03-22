package main

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/testground/sdk-go/runtime"
	tgsync "github.com/testground/sdk-go/sync"
	"golang.org/x/sync/errgroup"
)

// A Topology filters the set of all nodes
type Topology interface {
	SelectPeers(local peer.ID, remote []peer.AddrInfo) []peer.AddrInfo
}

// RandomTopology selects a subset of the total nodes at random
type RandomTopology struct {
	// Count is the number of total peers to return
	Count int
}

func (t RandomTopology) SelectPeers(in []peer.AddrInfo) []peer.AddrInfo {
	if len(in) == 0 || t.Count == 0 {
		return []peer.AddrInfo{}
	}

	n := t.Count
	if n > len(in) {
		n = len(in)
	}

	indices := rand.Perm(len(in))
	out := make([]peer.AddrInfo, n)
	for i := 0; i < n; i++ {
		out[i] = in[indices[i]]
	}
	return out
}

var peersTopic = tgsync.NewTopic("peers", new(peer.AddrInfo))

func waitForPeers(ctx context.Context, runenv *runtime.RunEnv, client tgsync.Client, local peer.ID) ([]peer.AddrInfo, error) {

	peersCh := make(chan *peer.AddrInfo)

	peers := make([]peer.AddrInfo, 0, runenv.TestInstanceCount)

	sctx, scancel := context.WithCancel(ctx)
	defer scancel()

	_ = client.MustSubscribe(sctx, peersTopic, peersCh)

	for i := 0; i < runenv.TestInstanceCount; i++ {
		select {
		case ai := <-peersCh:
			if ai.ID == local {
				continue
			}
			peers = append(peers, *ai)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	runenv.RecordMessage("received info from %d peers", len(peers))
	return peers, nil
}

func connectTopology(ctx context.Context, runenv *runtime.RunEnv, peers []peer.AddrInfo, h host.Host) error {
	tryConnect := func(ctx context.Context, ai peer.AddrInfo, attempts int) error {
		var err error
		for i := 1; i <= attempts; i++ {
			runenv.RecordMessage("dialling peer %s (attempt %d)", ai.ID, i)
			if err = h.Connect(ctx, ai); err == nil {
				return nil
			} else {
				runenv.RecordMessage("failed to dial peer %v (attempt %d), err: %s", ai.ID, i, err)
			}
			select {
			case <-time.After(time.Duration(rand.Intn(3000))*time.Millisecond + 2*time.Second):
			case <-ctx.Done():
				return fmt.Errorf("error while dialing peer %v, attempts made: %d: %w", ai.Addrs, i, ctx.Err())
			}
		}
		return fmt.Errorf("failed while dialing peer %v, attempts: %d: %w", ai.Addrs, attempts, err)
	}
	errgrp, ctx := errgroup.WithContext(ctx)
	for _, p := range peers {
		p := p
		errgrp.Go(func() error {
			return tryConnect(ctx, p, 3)
		})
	}
	return errgrp.Wait()
}
