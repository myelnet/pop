package supply

import (
	"context"
	"fmt"
	"strconv"

	"github.com/filecoin-project/go-address"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-multistore"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/ipld/go-ipld-prime"
	"github.com/libp2p/go-libp2p-core/host"
	peer "github.com/libp2p/go-libp2p-core/peer"
)

// ErrNoPeers when no peers are available to get or send supply to
var ErrNoPeers = fmt.Errorf("no peers available for supply")

// Manager exposes methods to manage the blocks we can serve as a provider
type Manager interface {
	Register(cid.Cid, uint64, multistore.StoreID) error
	// Dispatch a Request to n providers to cache our content, may expose the n as a param if need be
	Dispatch(Request, DispatchOptions) (*Response, error)
	GetStoreID(cid.Cid) (multistore.StoreID, error)
	GetStore(cid.Cid) (*multistore.Store, error)
	RemoveContent(cid.Cid) error
	ListMiners(context.Context) ([]address.Address, error)
}

// DispatchOptions encapsulates some options about how we want our content to be
// dispatched
type DispatchOptions struct {
	StoreID multistore.StoreID
}

// Supply keeps track of the content we store and provide on the network
// its role is to always seek and supply new and more efficient content to store
type Supply struct {
	h       host.Host
	dt      datatransfer.Manager
	ms      *multistore.MultiStore
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
	ds datastore.Batching,
	ms *multistore.MultiStore,
	regions []Region,
) *Supply {
	// We wrap it all in our Supply object
	s := &Supply{
		h:       h,
		dt:      dt,
		ms:      ms,
		regions: regions,
	}

	// TODO: validate AddRequest
	s.dt.RegisterVoucherType(&Request{}, &UnifiedRequestValidator{})

	// switch store based on voucher fields
	s.dt.RegisterTransportConfigurer(&Request{}, TransportConfigurer(s))

	s.man = NewManifest(h, dt, ds, ms)
	// Connect to incoming supply messages form peers
	s.net = NewNetwork(h, regions)
	// Set the manifest to handle our messages
	s.net.SetDelegate(s.man)

	return s
}

// Register a new content record in our supply
func (s *Supply) Register(key cid.Cid, size uint64, sid multistore.StoreID) error {
	// Store a record of the content in our supply
	return s.man.PutRecord(key, &ContentRecord{Labels: map[string]string{
		KStoreID: fmt.Sprintf("%d", sid),
		KSize:    fmt.Sprintf("%d", size),
	}})
}

// Dispatch requests to the network until we have propagated the content to enough peers
// it also tells the exchange we are providing this content in our supply
func (s *Supply) Dispatch(r Request, opts DispatchOptions) (*Response, error) {
	// Store a record of the content in our supply
	err := s.Register(r.PayloadCID, r.Size, opts.StoreID)
	if err != nil {
		return nil, err
	}
	res := &Response{
		recordChan: make(chan PRecord),
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
			res.recordChan <- PRecord{
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

// GetStoreID returns the StoreID of the store which has the given content
func (s *Supply) GetStoreID(id cid.Cid) (multistore.StoreID, error) {
	rec, err := s.man.GetRecord(id)
	if err != nil {
		return 0, err
	}
	sid, ok := rec.Labels[KStoreID]
	if !ok {
		return 0, fmt.Errorf("storeID not found")
	}
	storeID, err := strconv.ParseUint(sid, 10, 64)
	if err != nil {
		return 0, err
	}
	return multistore.StoreID(storeID), nil
}

// GetStore returns the correct multistore associated with a data CID
func (s *Supply) GetStore(id cid.Cid) (*multistore.Store, error) {
	storeID, err := s.GetStoreID(id)
	if err != nil {
		return nil, err
	}
	store, err := s.ms.Get(storeID)
	if err != nil {
		return nil, err
	}
	return store, nil
}

// RemoveContent removes all content linked to a root CID by completed dropping the store
func (s *Supply) RemoveContent(root cid.Cid) error {
	storeID, err := s.GetStoreID(root)
	if err != nil {
		return err
	}
	err = s.ms.Delete(storeID)
	if err != nil {
		return err
	}
	return s.man.RemoveRecord(root)
}

// ListMiners returns a list of miners based on the regions this supply is part of
// We keep a context as this could also query a remote service or API
func (s *Supply) ListMiners(ctx context.Context) ([]address.Address, error) {
	var strList []string
	for _, r := range s.regions {
		// Global region is already a list of miners in all regions
		if r.Name == "Global" {
			strList = r.StorageMiners
			break
		}
		strList = append(strList, r.StorageMiners...)
	}
	var addrList []address.Address
	for _, s := range strList {
		addr, err := address.NewFromString(s)
		if err != nil {
			return addrList, err
		}
		addrList = append(addrList, addr)
	}
	return addrList, nil
}

// StoreConfigurableTransport defines the methods needed to
// configure a data transfer transport use a unique store for a given request
type StoreConfigurableTransport interface {
	UseStore(datatransfer.ChannelID, ipld.Loader, ipld.Storer) error
}

// TransportConfigurer configurers the graphsync transport to use a custom blockstore per content
func TransportConfigurer(s *Supply) datatransfer.TransportConfigurer {
	return func(channelID datatransfer.ChannelID, voucher datatransfer.Voucher, transport datatransfer.Transport) {
		warn := func(err error) {
			fmt.Println("attempting to configure data store:", err)
		}
		request, ok := voucher.(*Request)
		if !ok {
			return
		}
		gsTransport, ok := transport.(StoreConfigurableTransport)
		if !ok {
			return
		}
		store, err := s.GetStore(request.PayloadCID)
		if err != nil {
			warn(err)
			return
		}
		err = gsTransport.UseStore(channelID, store.Loader, store.Storer)
		if err != nil {
			warn(err)
		}
	}
}
