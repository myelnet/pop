package exchange

import (
	"time"

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

// HeyReceiver is the interface the HeyService expects to receive the hey messages
type HeyReceiver interface {
	receive(peer.ID, Hey)
}

// HeyGetter is the interface they HeyService expects to provide a Hey message to send
type HeyGetter interface {
	getHey() Hey
}

// LatencyRecorder is an interface to record RTT latency measured during the hey operation
type LatencyRecorder interface {
	recordLatency(peer.ID, time.Duration) error
}
