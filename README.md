<h1 align="center"> 
	<br>
	  	🐸
	<br>
	<br>
	go-hop-exchange
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
- Upload and retrieve directly from a Filecoin sotrage miner if no secondary providers cache the content (Coming Soon)

## Background

To speed up data retrieval from Filecoin, a secondary market allows clients to publish their content ids to a network of providers
in order to retrieve it faster and more often at a cheaper price. This does not guarrantee data availability and so should be used
in addition to a regular storage deal. You can think of this as the CDN layer of Filecoin. This library is still very experimental 
and more at the prototype stage so feel free to open an issue if you have any suggestion or would like to contribute!

## Install

As a cli:

clone the repo then run:
```
$ make install
```

As a library:

```
$ go get github.com/myelnet/go-hop-exchange
```

As an IPFS plugin:

[Please follow the instructions in the plugin repo](https://github.com/myelnet/go-ipfs-hop-plugin)

## CLI Usage

### `hop start`

Starts an ipfs daemon ready to provide content

### `hop add /absolute/path/to/file`

Chunks a file with the unixfs dag and adds it to the local blockstore

- `-dispatch`: Dispatch the content to any available cache providers

### `hop get <cid>`

Retrieves the content from the network

- `-out <export-path>`: Write the file to the given path in addition to the blockstore

Content can also be retrieved from the browser by going to `localhost:2001/ipfs/<cid>`.

### `hop ping`

Check if the daemon is running and get a list of connected providers

- `hop ping <peer-id>`: ping the given provider to get latency and verify connectivity.

## Library Usage

1. Import the package.

```go
package main

import (
	hop "github.com/myelnet/go-hop-exchange"
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

exch, err := hop.NewExchange(
		ctx,
		hop.WithBlockstore(bstore),
		hop.WithPubSub(ps),
		hop.WithHost(host),
		hop.WithDatastore(ds),
		hop.WithGraphSync(gs),
		hop.WithRepoPath("ipfs-repo-path"),
		hop.WithKeystore(ks),
		hop.WithFilecoinAPI(
			"wss://filecoin.infura.io",
			http.Header{
				"Authorisation": []string{"Basic <mytoken>"},
			},
		)
	)

blocks := bserv.New(bstore, exch)
dag := dag.NewDAGService(n.blocks)

```
`WithFilecoinAPI` is optional and if not provided, the node only supports free transfers
and will charge 0 price per byte to client requests for content it serves. This is mostly
for testing purposes.

3. Start a new sessions for a content id to find the providers and sync the blocks to the blockstore

```go
var root cid.Cid

session, err := exch.Session(ctx, root)

err = session.SyncBlocks(ctx)

select {
case err := <-session.Done():
case <-ctx.Done():
}
```

4. Clients can anounce a new deal they made so the content is propagated to providers

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
- Do one thing well: there are many problems to solve in the decentralized storage space. This package only focuses on
  routing and retrieving content from peers in the most optimal way possible.
- KISS: Keep it simple, stupid. We take a naive approach to everything and try not to reinvent the wheel. Filecoin is already complex enough as it is.
