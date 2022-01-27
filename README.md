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

> Run a point-of-presence within [Myel](https://www.myel.network/), the community powered content delivery network.


## Technical Highlights

- Libp2p, IPFS and Filecoin protocols for data transfer.
- Content is routed via [Gossipsub](https://github.com/libp2p/specs/tree/master/pubsub/gossipsub).
- New content to cache is directly replicated by connected peers.
- Payments to retrieve content are made via [Filecoin payment channels](https://spec.filecoin.io/systems/filecoin_token/payment_channels/).  

## Background

Our mission is to build a community powered content delivery network that is resilient 🦾, scalable 🌏, and peer-to-peer ↔️ to suit the long-term needs of Web3 applications.

We're currently using [Filecoin](https://filecoin.io/) building blocks and are aspiring to make this library as interoperable as possible with existing Web3 backends such as IPFS.

This library is still experimental so feel free to open an issue if you have any suggestion or would like to contribute!

## Install

As a CLI:

```console
$ go install github.com/myel/pop/cmd/pop@latest
```

As a library:

```console
$ go get github.com/myelnet/pop/exchange
```

## CLI Usage

Run any command with `-h` flag for more details.

```console
$ pop
USAGE
  pop subcommand [flags]

This CLI is still under active development. Commands and flags will
change until a first stable release. To get started run 'pop start'.

SUBCOMMANDS
  start   Starts a POP daemon
  off     Gracefully shuts down the Pop daemon
  ping    Ping the local daemon or a given peer
  put     Put a file into an exchange transaction for storage
  status  Print the state of any ongoing transaction
  commit  Commit a DAG transaction
  get     Retrieve content from the network
  list    List all content indexed in this pop
  wallet  Manage your wallet

FLAGS
  -log info  Set logging mode
```

### Metrics Collection

`pop` nodes can push statistics measuring the performance of retrievals to an
[InfluxDB v2](https://www.influxdata.com/) database if certain
environment variables are set.
Set these variables as follows:

```bash
export INFLUXDB_URL=<INSERT InfluxDB ENDPOINT>
export INFLUXDB_TOKEN=<INSERT TOKEN>
export INFLUXDB_ORG=<INSERT ORG>
export INFLUXDB_BUCKET=<INSERT BUCKET>
```

### Auto-updating

`pop` supports auto-updating via GitHub webhooks.

To activate it pass the flag `-upgrade-secret` to `pop start` such that it matches the [GitHub webhook secret](https://docs.github.com/en/developers/webhooks-and-events/webhooks/securing-your-webhooks) set for your server. Make sure you run as sudo, or that your current user has authorization to modify your `pop` binary.

Your `pop` will then automatically download and install new releases.


## Deployment

You can deploy a cluster of nodes on AWS using kubernetes, as detailed in `infra/k8s`.

## Library Usage

See [go docs](https://pkg.go.dev/github.com/myelnet/pop/exchange).
