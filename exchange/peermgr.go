package exchange

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	cborutil "github.com/filecoin-project/go-cbor-util"
	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-eventbus"
	"github.com/libp2p/go-libp2p-core/event"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/rs/zerolog/log"
)

// PeerManager all the methods to maintain an optimal list of peers
type PeerManager interface {
	HeyReceiver
	LatencyRecorder
}

// Peer contains information recorded while interacted with a peer
type Peer struct {
	Regions []RegionCode
	Latency time.Duration
}

// PeerMgr is in charge of maintaining an optimal network of peers to coordinate with
type PeerMgr struct {
	h       host.Host
	regions map[RegionCode]Region
	emitter event.Emitter
	idx     *Index

	mu    sync.Mutex
	peers map[peer.ID]Peer
}

// HeyEvt is emitted when a Hey is received and accessible via the libp2p event bus subscription
type HeyEvt struct {
	Peer      peer.ID
	IndexRoot *cid.Cid // nil index root means empty index i.e. brand new node
}

// NewPeerMgr prepares a new PeerMgr instance
func NewPeerMgr(h host.Host, idx *Index, regions []Region) *PeerMgr {
	reg := make(map[RegionCode]Region, len(regions))
	for _, r := range regions {
		reg[r.Code] = r
	}

	emitter, err := h.EventBus().Emitter(new(HeyEvt))
	if err != nil {
		log.Error().Err(err).Msg("failed to create emitter event")
	}

	pm := &PeerMgr{
		h:       h,
		regions: reg,
		idx:     idx,
		peers:   make(map[peer.ID]Peer),
		emitter: emitter,
	}

	h.Network().Notify(&network.NotifyBundle{
		DisconnectedF: func(_ network.Network, c network.Conn) {
			pm.mu.Lock()
			defer pm.mu.Unlock()
			if _, ok := pm.peers[c.RemotePeer()]; ok {
				delete(pm.peers, c.RemotePeer())
			}
		},
	})

	return pm
}

func (pm *PeerMgr) Run(ctx context.Context) error {
	pm.h.SetStreamHandler(HeyProtocol, pm.handleStream)

	sub, err := pm.h.EventBus().Subscribe(new(event.EvtPeerIdentificationCompleted), eventbus.BufSize(1024))
	if err != nil {
		return fmt.Errorf("failed to subscribe to event bus: %w", err)
	}

	go func() {
		for evt := range sub.Out() {
			pic := evt.(event.EvtPeerIdentificationCompleted)
			go func() {
				if err := pm.sendHey(ctx, pic.Peer); err != nil {
					return
				}
			}()
		}
	}()
	return nil
}

// Peers returns n active peers for a given list of regions and peers to ignore
func (pm *PeerMgr) Peers(n int, rl []Region, ignore map[peer.ID]bool) []peer.ID {
	var peers []peer.ID
	if n == 0 {
		return peers
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for _, r := range rl {
		for p, v := range pm.peers {
			if ignore[p] {
				continue
			}
			for _, rc := range v.Regions {
				if rc == r.Code {
					peers = append(peers, p)
				}
			}
			// Check if we have enough peers and return
			if len(peers) == n {
				return peers
			}
		}
	}
	return peers
}

// handleStream is the multistream handler for the Hey protocol, it reads a Hey message and handles it
func (pm *PeerMgr) handleStream(s network.Stream) {
	var hmsg Hey
	if err := cborutil.ReadCborRPC(s, &hmsg); err != nil {
		connErr := s.Conn().Close()
		if connErr != nil {
			log.Error().Err(connErr).Msg("could not close stream connection")
		}
		log.Error().Err(err).Msg("failed to read CBOR Hey msg")
		return
	}

	pm.receive(s.Conn().RemotePeer(), hmsg)

	// We send back the seed to measure roundrip time
	go func() {
		defer s.Close()
		buf := make([]byte, 32)

		_, err := s.Write(buf)
		if err != nil {
			log.Error().Err(err).Msg("could not write bytes")
		}
	}()
}

// Receive a new greeting from peer
func (pm *PeerMgr) receive(p peer.ID, h Hey) {
	for _, r := range h.Regions {
		// We only save peers who are in the same region as us
		if reg, ok := pm.regions[r]; ok {
			err := pm.emitter.Emit(HeyEvt{
				Peer:      p,
				IndexRoot: h.IndexRoot,
			})
			if err != nil {
				log.Error().Err(err).Msg("failed to emit event")
			}

			// These peers should be trimmed last when the number of connections overflows
			pm.h.ConnManager().TagPeer(p, reg.Name, 10)
			pm.mu.Lock()
			pm.peers[p] = Peer{
				Regions: h.Regions,
			}
			pm.mu.Unlock()
		}
	}
}

// sendHey message to a given peer
func (pm *PeerMgr) sendHey(ctx context.Context, pid peer.ID) error {
	s, err := pm.h.NewStream(ctx, pid, HeyProtocol)
	if err != nil {
		return err
	}

	hmsg := pm.getHey()

	start := time.Now()
	if err := cborutil.WriteCborRPC(s, &hmsg); err != nil {
		return err
	}
	go func() {
		defer s.Close()

		s.SetReadDeadline(time.Now().Add(10 * time.Second))

		buf := make([]byte, 32)
		_, err := io.ReadFull(s, buf)
		if err != nil {
			log.Error().Err(err).Msg("failed to read pong msg")
		}
		now := time.Now()
		lat := now.Sub(start)

		pm.recordLatency(pid, lat)
	}()
	return nil
}

// getHey formats a new Hey message
func (pm *PeerMgr) getHey() Hey {
	regions := make([]RegionCode, len(pm.regions))
	i := 0
	for _, rg := range pm.regions {
		regions[i] = rg.Code
		i++
	}
	h := Hey{
		Regions: regions,
	}

	idxr := pm.idx.Root()
	if idxr != cid.Undef {
		h.IndexRoot = &idxr
	}
	return h
}

// RecordLatency for a given peer
func (pm *PeerMgr) recordLatency(p peer.ID, t time.Duration) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	peer, ok := pm.peers[p]
	if !ok {
		return errors.New("no peer given ID")
	}
	peer.Latency = t
	pm.peers[p] = peer
	return nil
}
