package exchange

import (
	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p-core/peer"
)

// HeyProtocol identifies the supply greeter protocol
const HeyProtocol = "/myel/pop/hey/1.0"

//go:generate cbor-gen-for Hey

// Hey is the greeting message which takes in network info
type Hey struct {
	Regions   []RegionCode
	IndexRoot *cid.Cid // If the node has an empty index the root will be nil
}

// HeyEvt is emitted when a Hey is received and accessible via the libp2p event bus subscription
type HeyEvt struct {
	Peer      peer.ID
	IndexRoot *cid.Cid // nil index root means empty index i.e. brand new node
}
