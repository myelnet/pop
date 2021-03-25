<h1 align="center"> 
	<br>
	  	🍿
	<br>
	<br>
	pop
	<br>
	<br>
	<br>
</h1>

> An IPFS bytes exchange to improve speed and reliability of Filecoin retrievals without
> heavy hardware requirements or compromising on decentralization

## Highlights

- IPFS exchange interface like Bitswap
- Use IPFS while providing content for retrievals on Filecoin (YES, that means you will earn FIL when we launch on mainnet!)
- New content to cache is dispatched via Gossipsub and stored by available providers
- Currently gossip based content routing though will be pluggable with other solutions
- Simple API abstracting away Filecoin deal operations
- Upload and retrieve directly from a Filecoin storage miner if no secondary providers cache the content (Coming Soon)

## Background

To speed up data retrieval from Filecoin, a secondary market allows clients to publish their content ids to a network of providers
in order to retrieve it faster and more often at a cheaper price. This does not guarantee data availability and so should be used
in addition to a regular storage deal. You can think of this as the CDN layer of Filecoin. This library is still very experimental 
and more at the prototype stage so feel free to open an issue if you have any suggestion or would like to contribute!

## Install

As a CLI:

Install dependencies:

```commandline
brew install go bzr jq pkg-config rustup hwloc
```

Clone the repo. You may need to include submodules:

```commandline
git submodule update --init --recursive
```

run:
```commandline
$ make
```

As a library:

```commandline
$ go get github.com/myelnet/pop
```

## CLI Usage

Run any command with `-h` flag for more details.

```
USAGE
  pop subcommand [flags]

This CLI is still under active development. Commands and flags will
change in the future.

SUBCOMMANDS
  start   Starts an IPFS daemon
  ping    Ping the local daemon or a given peer
  add     Add a file to the working DAG
  status  Print the state of the working DAG
  pack    Pack the current index into a DAG archive
  push    Push a DAG archive to storage
  get     Retrieve content from the network
```

## Library Usage

1. Import the package.

```go
package main

import (
	"github.com/myelnet/pop"
)
```

2. Initialize a blockstore, graphsync, libp2p host and gossipsub subscription.

```go
var ctx context.Context
var bstore blockstore.Blockstore
var ps *pubsub.PubSub
var host libp2p.Host
var ds datastore.Batching
var gs graphsync.GraphExchange
var ks keystore.Keystore

exch, err := pop.NewExchange(
		ctx,
		pop.Settings{
			Blockstore: bstore,
			PubSub: ps,
			Host: host,
			Datastore: ds,
			GraphSync: gs,
			RepoPath: "ipfs-repo-path",
			Keystore: ks,
			FilecoinRPCEndpoint: "wss://filecoin.infura.io",
			FilecoinRPCHeader: http.Header{
				  "Authorisation": []string{"Basic <mytoken>"},
			},
			Regions: []supply.Region{
				  supply.Regions["Global"],
			},
		)
	)
```
FilecoinRPCEndpoint is optional and if not provided, the node only supports free transfers
and will charge 0 price per byte to client requests for content it serves. This is mostly
for testing purposes or for providing on private regions.

3. Start a new sessions for a content id to find the providers and sync the blocks to the blockstore

```go
var root cid.Cid

session, err := exch.Session(ctx, root)

// query providers in the regions we joined
offer, err := session.QueryGossip(ctx)

// or query a storage miner directly
offer, err := session.QueryMiner(ctx, peerID)

err = session.SyncBlocks(ctx, offer)

select {
case err := <-session.Done():
case <-ctx.Done():
}
```

4. Clients can announce a new deal they made so the content is propagated to providers

```go
var ctx context.Context
var root cid.Cid

err := exch.Dispatch(ctx, root)
```

5. We're also exposing convenience methods to transfer funds or import keys to the underlying wallet

```go
var ctx context.Context
var to address.Address

from, err := exch.Wallet().DefaultAddress() 

err = exch.Wallet().Transfer(ctx, from, to, "12.5")
```

## Design principles

- Composable: Hop is highly modular and can be combined with any ipfs, data transfer, Filecoin or other exchange systems.
- Lightweight: we try to limit the size of the build as we are aiming to bring this exchange to mobile devices. We do not import core implementations such as go-ipfs or lotus directly but rely on shared packages.
- Do one thing well: there are many problems to solve in the decentralized storage space. This package only focuses on routing and retrieving content from peers in the most optimal way possible.
- KISS: Keep it simple, stupid. We take a naive approach to everything and try not to reinvent the wheel. Filecoin is already complex enough as it is.
