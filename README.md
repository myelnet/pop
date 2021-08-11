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

####Install dependencies:
Since CGO is required, you will need GCC

#### Mac
```commandline
$ brew install gcc make
```

#### Linux
```commandline
$ sudo apt install gcc make
```

Clone the repo.

run:
```commandline
$ make all
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

## Library Usage

See [go docs](https://pkg.go.dev/github.com/myelnet/pop/exchange).
