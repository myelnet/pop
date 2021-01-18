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

> An IPFS bytes exchange to allow any IPFS node to become a Filecoin retrieval provider
> and retrieve content from Filecoin

## Highlights

- IPFS exchange interface like Bitswap
- Turn any IPFS node into a Filecoin retrieval provider (YES, that means you will earn FIL when we launch on mainnet!)
- New content is dispatched via Gossipsub and stored if enough space if available
- IPFS Plugin to wrap the default Bitswap implementation and fetch blocks from Filecoin if not available on the public IPFS network
- Does NOT allow for uploading to Filecoin (we will provide a separate plugin for that in the future, in the meantime you can use the Myel app)

## Background

To speed up data retrieval from Filecoin, a secondary market allows clients to publish their content ids to a network of providers
in order to retrieve it faster and more often at a cheaper price. This does not guarrantee data availability and so should be used
in addition to a regular storage deal. You can think of this as the CDN layer of Filecoin. This library is still very experimental 
and more at the prototype stage so feel free to open an issue if you have any suggestion or would like to contribute!

## Install

As a library:

```
$ go get github.com/myelnet/go-hop-exchange
```

As an IPFS plugin:

[Please follow the instructions in the plugin repo](https://github.com/myelnet/go-ipfs-hop-plugin)

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
var nodeAddr address.Address
var ds datastore.Batching
var gs graphsync.GraphExchange

exch, err := hop.NewExchange(
		ctx,
		hop.WithBlockstore(bstore),
		hop.WithPubSub(ps),
		hop.WithHost(host),
		hop.WithDatastore(ds),
		hop.WithGraphSync(gs),
		hop.WithRepoPath("ipfs-repo-path"),
	)

blocks := bserv.New(bstore, exch)
dag := dag.NewDAGService(n.blocks)

```

3. When getting from the DAG it will automatically query the network

```go
var dag ipld.DAGService
var ctx context.Context
var root cid.Cid

node, err := dag.Get(ctx, root)
```

4. Clients can anounce a new deal they made so the content is propagated to providers

```go
var ctx context.Context
var root cid.Cid

err := exch.Announce(ctx, root)
```

## Design principles

- Composable: Hop is highly modular and can be combined with any ipfs, data transfer, Filecoin or other exchange systems.
- Lightweight: we try to limit the size of the build as we are aiming to bring this exchange to mobile devices. We do not import core implementations such as go-ipfs or lotus directly but rely on shared packages.
- Do one thing well: there are many problems to solve in the decentralized storage space. This package only focuses on
  routing and retrieving content from peers in the most optimal way possible.
