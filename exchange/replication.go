package exchange

import (
	"bufio"
	"context"
	"fmt"
	"sync"
	"time"

	cborutil "github.com/filecoin-project/go-cbor-util"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-multistore"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-graphsync/storeutil"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	cbor "github.com/ipfs/go-ipld-cbor"
	"github.com/ipld/go-ipld-prime"
	"github.com/jpillora/backoff"
	"github.com/libp2p/go-eventbus"
	"github.com/libp2p/go-libp2p-core/event"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/mux"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
	"github.com/myelnet/pop/internal/utils"
	sel "github.com/myelnet/pop/selectors"
	"github.com/rs/zerolog/log"
)

//go:generate cbor-gen-for Request

// PopRequestProtocolID is the protocol for requesting caches to store new content
const PopRequestProtocolID = protocol.ID("/myel/pop/request/1.0")

// Request describes the content to pull
type Request struct {
	Method     Method
	PayloadCID cid.Cid
	Size       uint64
}

// Type defines Request as a datatransfer voucher for pulling the data from the request
func (Request) Type() datatransfer.TypeIdentifier {
	return "ReplicationRequestVoucher"
}

// Method is the replication request method
type Method uint64

const (
	// Dispatch is an initial request from a content plublisher
	Dispatch Method = iota
	// FetchIndex is a request from one content provider to another to retrieve their index
	FetchIndex
)

// IndexEvt is emitted when a new index is loaded in the replication service
type IndexEvt struct {
	Root cid.Cid
}

// RequestStream allows reading and writing CBOR encoded messages to a stream
type RequestStream struct {
	p   peer.ID
	rw  mux.MuxedStream
	buf *bufio.Reader
}

// ReadRequest reads and decodes a CBOR encoded Request message from a stream buffer
func (rs *RequestStream) ReadRequest() (Request, error) {
	var m Request
	if err := m.UnmarshalCBOR(rs.buf); err != nil {
		return Request{}, err
	}
	return m, nil
}

// WriteRequest encodes and writes a Request message to a stream
func (rs *RequestStream) WriteRequest(m Request) error {
	return cborutil.WriteCborRPC(rs.rw, &m)
}

// Close the stream
func (rs *RequestStream) Close() error {
	return rs.rw.Close()
}

// OtherPeer returns the peer ID of the peer at the other end of the stream
func (rs *RequestStream) OtherPeer() peer.ID {
	return rs.p
}

// RoutedRetriever is a generic interface providing a method to find and retrieve content on the exchange
type RoutedRetriever interface {
	FindAndRetrieve(context.Context, cid.Cid) error
}

// Replication manages the network replication scheme, it keeps track of read and write requests
// and decides whether to join a replication scheme or not
type Replication struct {
	h         host.Host
	dt        datatransfer.Manager
	ms        *multistore.MultiStore
	bs        blockstore.Blockstore
	pm        *PeerMgr
	idx       *Index
	rgs       []Region
	reqProtos []protocol.ID
	emitter   event.Emitter
	indexRcvd chan struct{}
	interval  time.Duration
	rtv       RoutedRetriever

	pmu   sync.Mutex
	pulls map[cid.Cid]*peer.Set

	smu    sync.Mutex
	stores map[cid.Cid]*multistore.Store
}

// NewReplication starts the exchange replication management system
func NewReplication(h host.Host, idx *Index, dt datatransfer.Manager, rtv RoutedRetriever, opts Options) (*Replication, error) {
	pm := NewPeerMgr(h, idx, opts.Regions)
	r := &Replication{
		h:         h,
		pm:        pm,
		dt:        dt,
		rgs:       opts.Regions,
		idx:       idx,
		rtv:       rtv,
		ms:        opts.MultiStore,
		bs:        opts.Blockstore,
		interval:  opts.ReplInterval,
		reqProtos: []protocol.ID{PopRequestProtocolID},
		pulls:     make(map[cid.Cid]*peer.Set),
		indexRcvd: make(chan struct{}),
		stores:    make(map[cid.Cid]*multistore.Store),
	}
	h.SetStreamHandler(PopRequestProtocolID, r.handleRequest)

	err := r.dt.RegisterVoucherType(&Request{}, r)
	if err != nil {
		return nil, fmt.Errorf("failed to register voucher type: %v", err)
	}

	err = r.dt.RegisterTransportConfigurer(&Request{}, TransportConfigurer(r.idx, r, h.ID()))
	if err != nil {
		return nil, fmt.Errorf("failed to register transport configurer: %v", err)
	}

	emitter, err := h.EventBus().Emitter(new(IndexEvt))
	if err != nil {
		return nil, fmt.Errorf("failed to create emitter event: %v", err)
	}
	r.emitter = emitter

	return r, nil
}

// Start initiates listeners to update our scheme if new peers join
func (r *Replication) Start(ctx context.Context) error {
	sub, err := r.h.EventBus().Subscribe(new(HeyEvt), eventbus.BufSize(16))
	if err != nil {
		return err
	}
	// Any time we receive a new index, check if any refs should be added to our supply
	// if interval is 0 the feature is deactivated
	if r.interval > 0 {
		go r.refreshIndex(ctx)
		go r.pumpIndexes(ctx, sub)
	}
	if err := r.pm.Run(ctx); err != nil {
		return err
	}
	return nil
}

// pumpIndexes iterates over a subscription to new Hey msg received when connecting with other provider peers
// it keeps index roots into a queue and iteratively fetches them. We could potentially fetch them in parallel
// but we ideally don't want this to be a burden on the node resources so we take it easy
func (r *Replication) pumpIndexes(ctx context.Context, sub event.Subscription) {
	var q []HeyEvt
	var fetchDone chan fetchResult
	for {
		select {
		case <-ctx.Done():
			return
		case evt := <-sub.Out():
			hevt := evt.(HeyEvt)
			if hevt.IndexRoot != nil {
				if fetchDone == nil {
					fetchDone = make(chan fetchResult, 1)
					go func() {
						err := r.fetchIndex(ctx, hevt)
						fetchDone <- fetchResult{*hevt.IndexRoot, err}
					}()
					continue
				}
				q = append(q, hevt)
			}
			// We can probably ignore errors
		case res := <-fetchDone:
			if res.err == nil {
				go func(rt cid.Cid) {
					store := r.GetStore(rt)
					err := r.idx.LoadInterest(rt, cbor.NewCborStore(store.Bstore))
					if err != nil {
						log.Error().Err(err).Msg("failed to load interest")
						return
					}
				}(res.root)
			}

			if len(q) > 0 {
				fetchDone = make(chan fetchResult, 1)
				go func(hvt HeyEvt) {
					err := r.fetchIndex(ctx, hvt)
					fetchDone <- fetchResult{*hvt.IndexRoot, err}
				}(q[0])
				q = q[1:]
			}

		}
	}
}

// refreshIndex is a long running process that regularly inspects received indexes
// and if usage is high enough retrieves the content at market price
func (r *Replication) refreshIndex(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			refs, err := r.idx.Interesting()
			if err != nil || len(refs) == 0 {
				continue
			}
			log.Info().Str("hostId", r.h.ID().String()).Int("refs", len(refs)).Msg("tick")

			for ref := range refs {
				// let's get it
				err := r.rtv.FindAndRetrieve(ctx, ref.PayloadCID)
				if err != nil {
					continue
				}
				err = r.idx.DropInterest(ref.PayloadCID)
				if err != nil {
					log.Debug().Err(err).Str("RefPayloadCID", ref.PayloadCID.String()).Msg("DropInterest error")
				}

				err = r.emitter.Emit(IndexEvt{
					Root: ref.PayloadCID,
				})
				if err != nil {
					log.Error().Err(err).Str("RefPayloadCID", ref.PayloadCID.String()).Msg("emitter error")
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

// fetchResult associates the root of the index fetched and a possible error
type fetchResult struct {
	root cid.Cid
	err  error
}

// fetchIndex handles the data transfer for retrieving the index of a given peer announced in a Hey
// msg. It blocks until the transfer is completed or fails.
func (r *Replication) fetchIndex(ctx context.Context, hvt HeyEvt) error {
	rcid := *hvt.IndexRoot
	req := Request{
		Method:     FetchIndex,
		PayloadCID: rcid,
	}

	if err := r.AddStore(rcid, r.ms.Next()); err != nil {
		return err
	}

	chid, err := r.dt.OpenPullDataChannel(ctx, hvt.Peer, &req, rcid, sel.Hamt())
	if err != nil {
		return err
	}

	for {
		state, err := r.dt.ChannelState(ctx, chid)
		if err != nil {
			return err
		}
		switch state.Status() {
		case datatransfer.Failed:
			return fmt.Errorf("data transfer failed: %s", state.Message())
		case datatransfer.Cancelled:
			return fmt.Errorf("data transfer cancelled: %s", state.Message())
		case datatransfer.Completed:
			return nil
		}
	}
}

// AddStore assigns a store for a given root cid and store ID
func (r *Replication) AddStore(k cid.Cid, sid multistore.StoreID) error {
	store, err := r.ms.Get(sid)
	if err != nil {
		return err
	}
	r.smu.Lock()
	r.stores[k] = store
	r.smu.Unlock()
	return nil
}

// GetStore returns the store used for a given root index
func (r *Replication) GetStore(k cid.Cid) *multistore.Store {
	r.smu.Lock()
	defer r.smu.Unlock()
	s, ok := r.stores[k]
	if !ok {
		// If no store can be found we return the global store as a last resort
		return &multistore.Store{
			Loader: storeutil.LoaderForBlockstore(r.bs),
			Bstore: r.bs,
		}
	}
	return s
}

// RmStore cleans up the store when it is not needed anymore
func (r *Replication) RmStore(k cid.Cid) {
	r.smu.Lock()
	defer r.smu.Unlock()
	delete(r.stores, k)
}

// balanceIndex checks if any content in the interest list is more popular than content in the supply
// in which case it will try to retrieve it from the network and insert it in there

// NewRequestStream opens a multi stream with the given peer and sets up the interface to write requests to it
func (r *Replication) NewRequestStream(dest peer.ID) (*RequestStream, error) {
	s, err := OpenStream(context.Background(), r.h, dest, r.reqProtos)
	if err != nil {
		return nil, err
	}
	buf := bufio.NewReaderSize(s, 16)
	return &RequestStream{p: dest, rw: s, buf: buf}, nil
}

func (r *Replication) handleRequest(s network.Stream) {
	p := s.Conn().RemotePeer()
	buffered := bufio.NewReaderSize(s, 16)
	rs := &RequestStream{p, s, buffered}
	defer rs.Close()
	req, err := rs.ReadRequest()
	if err != nil {
		log.Error().Err(err).Msg("error when reading stream request")
		return
	}

	// Only the dispatch method is streamed directly at this time
	switch req.Method {
	case Dispatch:
		// TODO: validate request

		// Check if we may already have this content
		// TODO: create RefExists method
		_, err := r.idx.GetRef(req.PayloadCID)
		if err == nil {
			return
		}

		// Create a new store to receive our new blocks
		// It will be automatically picked up in the TransportConfigurer
		sid := r.ms.Next()
		if err := r.AddStore(req.PayloadCID, sid); err != nil {
			log.Error().Err(err).Msg("error when creating new store")
			return
		}

		ctx := context.Background()
		chid, err := r.dt.OpenPullDataChannel(ctx, p, &req, req.PayloadCID, sel.All())
		if err != nil {
			log.Error().Err(err).Msg("error when opening channel data channel")
			return
		}

		for {
			state, err := r.dt.ChannelState(ctx, chid)
			if err != nil {
				log.Error().Err(err).Msg("error when fetching channel state")
				return
			}

			switch state.Status() {
			case datatransfer.Failed, datatransfer.Cancelled:
				err = r.idx.DropRef(state.BaseCID())
				if err != nil {
					log.Error().Err(err).Msg("error when droping ref")
				}
				return

			case datatransfer.Completed:
				store := r.GetStore(req.PayloadCID)

				keys, err := utils.MapLoadableKeys(ctx, req.PayloadCID, store.Loader)
				if err != nil {
					log.Debug().Err(err).Msg("error when loading keys")
				}

				ref := &DataRef{
					PayloadCID:  req.PayloadCID,
					PayloadSize: int64(req.Size),
					Keys:        keys.AsMap(),
				}

				err = r.idx.SetRef(ref)
				if err != nil {
					log.Error().Err(err).Msg("error when setting ref")
				}

				if err := utils.MigrateBlocks(ctx, store.Bstore, r.bs); err != nil {
					log.Error().Err(err).Msg("error when migrating blocks")
				}

				if err := r.ms.Delete(sid); err != nil {
					log.Error().Err(err).Msg("error when deleting store")
				}
				return
			}
		}
	}
}

// PRecord is a provider <> cid mapping for recording who is storing what content
type PRecord struct {
	Provider   peer.ID
	PayloadCID cid.Cid
}

// DispatchOptions exposes parameters to affect the duration of a Dispatch operation
type DispatchOptions struct {
	BackoffMin     time.Duration
	BackoffAttemps int
	RF             int
	StoreID        multistore.StoreID
}

// DefaultDispatchOptions provides useful defaults
// We can change these if the content requires a long transfer time
var DefaultDispatchOptions = DispatchOptions{
	BackoffMin:     5 * time.Second,
	BackoffAttemps: 4,
	RF:             6,
}

// Dispatch to the network until we have propagated the content to enough peers
func (r *Replication) Dispatch(root cid.Cid, size uint64, opt DispatchOptions) (chan PRecord, error) {
	if err := r.AddStore(root, opt.StoreID); err != nil {
		return nil, err
	}

	req := Request{
		Method:     Dispatch,
		PayloadCID: root,
		Size:       size,
	}
	resChan := make(chan PRecord, opt.RF)
	out := make(chan PRecord, opt.RF)
	// listen for datatransfer events to identify the peers who pulled the content
	unsub := r.dt.SubscribeToEvents(func(event datatransfer.Event, chState datatransfer.ChannelState) {
		root := chState.BaseCID()
		if root != req.PayloadCID {
			return
		}

		if chState.Status() == datatransfer.Failed || chState.Status() == datatransfer.Cancelled {
			log.Error().Str("root", root.String()).Msg("transfer failed for content")
		}

		if chState.Status() == datatransfer.Completed {
			// The recipient is the provider who received our content
			rec := chState.Recipient()
			resChan <- PRecord{
				Provider:   rec,
				PayloadCID: root,
			}
		}
	})
	go func() {
		defer func() {
			unsub()
			close(out)

		}()
		// The peers we already sent requests to
		rcv := make(map[peer.ID]bool)
		// Set the parameters for backing off after each try
		b := backoff.Backoff{
			Min: opt.BackoffMin,
			Max: 60 * time.Minute,
			// Factor: 2 (default)
		}
		// The number of confirmations we received so far
		n := 0

	requests:
		for {
			// Give up after 6 attempts. Maybe should make this customizable for servers that can afford it
			if int(b.Attempt()) > opt.BackoffAttemps {
				return
			}
			// Select the providers we want to send to minus those we already confirmed
			// received the requests
			providers := r.pm.Peers(opt.RF-n, r.rgs, rcv)

			// Authorize the transfer
			for _, p := range providers {
				r.AuthorizePull(req.PayloadCID, p)
				rcv[p] = true
			}
			if len(providers) > 0 {
				// sendAllRequests
				r.sendAllRequests(req, providers)
			}

			delay := b.Duration()
			timer := time.NewTimer(delay)
			for {
				select {
				case <-timer.C:
					continue requests

				case r := <-resChan:
					// forward the confirmations to the Response channel
					out <- r
					// increment our results count
					n++
					if n == opt.RF {
						return
					}
				}
			}
		}
	}()
	return out, nil
}

func (r *Replication) sendAllRequests(req Request, peers []peer.ID) {
	for _, p := range peers {
		stream, err := r.NewRequestStream(p)
		if err != nil {
			continue
		}
		err = stream.WriteRequest(req)
		stream.Close()
		if err != nil {
			continue
		}
	}
}

// AuthorizePull adds a peer to a set giving authorization to pull content without payment
// We assume that this authorizes the peer to pull as many links from the root CID as they can
// It runs on the client side to authorize caches
func (r *Replication) AuthorizePull(k cid.Cid, p peer.ID) {
	r.pmu.Lock()
	defer r.pmu.Unlock()
	if set, ok := r.pulls[k]; ok {
		set.Add(p)
		return
	}
	set := peer.NewSet()
	set.Add(p)
	r.pulls[k] = set
}

// ValidatePush returns a stubbed result for a push validation
func (r *Replication) ValidatePush(
	isRestart bool,
	chid datatransfer.ChannelID,
	sender peer.ID,
	voucher datatransfer.Voucher,
	baseCid cid.Cid,
	selector ipld.Node) (datatransfer.VoucherResult, error) {
	return nil, fmt.Errorf("no pushed accepted")
}

// ValidatePull returns a stubbed result for a pull validation
func (r *Replication) ValidatePull(
	isRestart bool,
	chid datatransfer.ChannelID,
	receiver peer.ID,
	voucher datatransfer.Voucher,
	baseCid cid.Cid,
	selector ipld.Node) (datatransfer.VoucherResult, error) {

	request, ok := voucher.(*Request)
	if !ok {
		return nil, fmt.Errorf("bad voucher")
	}
	// TODO: For now fetching someone's index it authorized by default
	// we need some permission system
	if request.Method == FetchIndex {
		return nil, nil
	}

	r.pmu.Lock()
	defer r.pmu.Unlock()
	set, ok := r.pulls[baseCid]
	if !ok {
		return nil, fmt.Errorf("unknown CID")
	}
	if !set.Contains(receiver) {
		return nil, fmt.Errorf("not authorized")
	}
	return nil, nil
}

// StoreConfigurableTransport defines the methods needed to
// configure a data transfer transport use a unique store for a given request
type StoreConfigurableTransport interface {
	UseStore(datatransfer.ChannelID, ipld.Loader, ipld.Storer) error
}

// IdxStoreGetter returns the store used for retrieving a given index root
type IdxStoreGetter interface {
	GetStore(cid.Cid) *multistore.Store
}

// TransportConfigurer configurers the graphsync transport to use a custom blockstore per content
func TransportConfigurer(idx *Index, isg IdxStoreGetter, pid peer.ID) datatransfer.TransportConfigurer {
	return func(channelID datatransfer.ChannelID, voucher datatransfer.Voucher, transport datatransfer.Transport) {
		warn := func(err error) {
			log.Error().Err(err).Msg("attempting to configure data store")
		}
		request, ok := voucher.(*Request)
		if !ok {
			return
		}
		gsTransport, ok := transport.(StoreConfigurableTransport)
		if !ok {
			return
		}
		// When initiating both FetchIndex and Dispatch transfers we've already assigned a store
		// with the root CID so we just need to get it
		if (request.Method == FetchIndex && channelID.Initiator == pid) || request.Method == Dispatch {
			// When we're fetching a new index we store it in a new store
			store := isg.GetStore(request.PayloadCID)
			err := gsTransport.UseStore(channelID, store.Loader, store.Storer)
			if err != nil {
				warn(err)
			}
			return
		}
		// Someone is retrieving our index, it should be loaded from the index's blockstore
		if request.Method == FetchIndex {
			loader := storeutil.LoaderForBlockstore(idx.Bstore())
			storer := storeutil.StorerForBlockstore(idx.Bstore())
			err := gsTransport.UseStore(channelID, loader, storer)
			if err != nil {
				warn(err)
			}
			return
		}
	}
}
