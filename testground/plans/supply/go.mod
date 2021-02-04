module github.com/myelnet/go-hop-exchange/testground/plans/supply

go 1.14

require (
	github.com/filecoin-project/go-data-transfer v1.2.8
	github.com/ipfs/go-blockservice v0.1.4
	github.com/ipfs/go-cid v0.0.7
	github.com/ipfs/go-datastore v0.4.5
	github.com/ipfs/go-graphsync v0.6.0
	github.com/ipfs/go-ipfs-blockstore v1.0.3
	github.com/ipfs/go-ipfs-chunker v0.0.5
	github.com/ipfs/go-ipfs-files v0.0.8
	github.com/ipfs/go-ipld-format v0.2.0
	github.com/ipfs/go-merkledag v0.3.2
	github.com/ipfs/go-unixfs v0.2.4
	github.com/ipfs/testground/sdk/runtime v0.4.0
	github.com/libp2p/go-libp2p v0.13.0
	github.com/libp2p/go-libp2p-core v0.8.0
	github.com/libp2p/go-libp2p-pubsub v0.4.1
	github.com/myelnet/go-hop-exchange v0.0.0-20210204120303-ad17216580c9
	github.com/testground/sdk-go v0.2.7
)

replace github.com/myelnet/go-hop-exchange => ../../../.
