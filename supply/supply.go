package supply

import (
	"fmt"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p-core/host"
	peer "github.com/libp2p/go-libp2p-core/peer"
)

// ErrNoPeers when no peers are available to get or send supply to
var ErrNoPeers = fmt.Errorf("no peers available for supply")

// Manager exposes methods to manage the blocks we can serve as a provider
type Manager interface {
	// Dispatch a Request to n providers to cache our content, may expose the n as a param if need be
	Dispatch(Request) (*Response, error)
	ProviderPeersForContent(cid.Cid) ([]peer.ID, error)
}

// Supply keeps track of the content we store and provide on the network
// its role is to always seek and supply new and more efficient content to store
type Supply struct {
	h       host.Host
	dt      datatransfer.Manager
	net     *Network
	man     *Manifest
	regions []Region
}

// PRecord is a provider <> cid mapping for recording who is storing what content
type PRecord struct {
	Provider   peer.ID
	PayloadCID cid.Cid
}

// New instance of the SupplyManager
func New(
	h host.Host,
	dt datatransfer.Manager,
	regions []Region,
) *Supply {
	// We wrap it all in our Supply object
	s := &Supply{
		h:       h,
		dt:      dt,
		regions: regions,
	}

	// TODO: validate AddRequest
	s.dt.RegisterVoucherType(&Request{}, &UnifiedRequestValidator{})

	s.man = NewManifest(h, dt)
	// Connect to incoming supply messages form peers
	s.net = NewNetwork(h, regions)
	// Set the manifest to handle our messages
	s.net.SetDelegate(s.man)

	return s
}

// ProviderPeersForContent gets the known providers for a given content id
func (s *Supply) ProviderPeersForContent(c cid.Cid) ([]peer.ID, error) {
	return nil, nil
}

// Dispatch requests to the network until we have propagated the content to enough peers
func (s *Supply) Dispatch(r Request) (*Response, error) {
	res := &Response{
		recordChan: make(chan *PRecord),
	}

	// listen for datatransfer events to identify the peers who pulled the content
	res.unsub = s.dt.SubscribeToEvents(func(event datatransfer.Event, chState datatransfer.ChannelState) {
		if chState.Status() == datatransfer.Completed {
			root := chState.BaseCID()
			if root != r.PayloadCID {
				return
			}
			// The recipient is the provider who received our content
			rec := chState.Recipient()
			res.recordChan <- &PRecord{
				Provider:   rec,
				PayloadCID: root,
			}
		}
	})

	// Select the providers we want to send to
	providers, err := s.selectProviders()
	if err != nil {
		return res, err
	}
	s.sendAllRequests(r, providers)
	return res, nil
}

func (s *Supply) selectProviders() ([]peer.ID, error) {
	var peers []peer.ID
	// Get the current connected peers
	for _, pconn := range s.h.Network().Conns() {
		pid := pconn.RemotePeer()
		// Make sure we don't add ourselves
		if pid != s.h.ID() {
			// Make sure our peer supports the retrieval dispatch protocol
			var protos []string
			for _, p := range protoRegions(RequestProtocol, s.regions) {
				protos = append(protos, string(p))
			}
			supported, err := s.h.Peerstore().SupportsProtocols(
				pid,
				protos...,
			)
			if err != nil || len(supported) == 0 {
				continue
			}
			peers = append(peers, pid)
		}
	}

	if len(peers) == 0 {
		return nil, ErrNoPeers
	}
	// TODO: Allow configurating the amount of peers we want to notify
	max := 6
	// If we have less peers we adjust accordingly
	if len(peers) > max {
		peers = peers[:max]
	}
	return peers, nil
}

func (s *Supply) sendAllRequests(r Request, peers []peer.ID) {
	for _, p := range peers {
		stream, err := s.net.NewRequestStream(p)
		if err != nil {
			fmt.Println("Unable to create new request stream", err)
			continue
		}
		err = stream.WriteRequest(r)
		if err != nil {
			fmt.Println("Unable to send addRequest:", err)
			continue
		}
	}
}
