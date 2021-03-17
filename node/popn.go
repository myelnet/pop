package node

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-multistore"
	"github.com/ipfs/go-blockservice"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
	badgerds "github.com/ipfs/go-ds-badger"
	"github.com/ipfs/go-graphsync"
	gsimpl "github.com/ipfs/go-graphsync/impl"
	gsnet "github.com/ipfs/go-graphsync/network"
	"github.com/ipfs/go-graphsync/storeutil"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	offline "github.com/ipfs/go-ipfs-exchange-offline"
	files "github.com/ipfs/go-ipfs-files"
	keystore "github.com/ipfs/go-ipfs-keystore"
	ipldformat "github.com/ipfs/go-ipld-format"
	"github.com/ipfs/go-merkledag"
	"github.com/ipfs/go-path"
	"github.com/libp2p/go-libp2p"
	connmgr "github.com/libp2p/go-libp2p-connmgr"
	ci "github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/routing"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	peerstore "github.com/libp2p/go-libp2p-peerstore"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/p2p/net/conngater"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
	ma "github.com/multiformats/go-multiaddr"
	mh "github.com/multiformats/go-multihash"
	"github.com/myelnet/pop"
	"github.com/myelnet/pop/filecoin"
	"github.com/myelnet/pop/filecoin/storage"
	"github.com/myelnet/pop/retrieval/client"
	"github.com/myelnet/pop/retrieval/deal"
	"github.com/myelnet/pop/supply"
	"github.com/myelnet/pop/wallet"
	"github.com/rs/zerolog/log"
)

const DefaultHashFunction = uint64(mh.BLAKE2B_MIN + 31)
const unixfsLinksPerLevel = 1024
const KLibp2pHost = "libp2p-host"

// ErrFilecoinRPCOffline is returned when the node is running without a provided filecoin api endpoint + token
var ErrFilecoinRPCOffline = errors.New("filecoin RPC is offline")

// ErrAllDealsFailed is returned when all storage deals failed to get started
var ErrAllDealsFailed = errors.New("all deals failed")

// ErrNoDAGForPacking is returned when no DAGs are staged in the index before packing
var ErrNoDAGForPacking = errors.New("no DAG for packing")

// ErrDAGNotPacked is returned when dags have not been packed and the node attempts to start a storage deal
var ErrDAGNotPacked = errors.New("DAG not packed")

// ErrNodeNotFound is returned when we cannot find the node in the given root
var ErrNodeNotFound = errors.New("node not found")

// ErrQuoteNotFound is returned when we are trying to store but couldn't get a quote
var ErrQuoteNotFound = errors.New("quote not found")

// ErrInvalidPeer is returned when trying to ping a peer with invalid peer ID or address
var ErrInvalidPeer = errors.New("invalid peer ID or address")

// Options determines configurations for the IPFS node
type Options struct {
	// RepoPath is the file system path to use to persist our datastore
	RepoPath string
	// SocketPath is the unix socket path to listen on
	SocketPath string
	// BootstrapPeers is a peer address to connect to for discovering other peers
	BootstrapPeers []string
	// FilEndpoint is the websocket url for accessing a remote filecoin api
	FilEndpoint string
	// FilToken is the authorization token to access the filecoin api
	FilToken string
	// PrivKey is a hex encoded private key to use for default address
	PrivKey string
	// Regions is a list of regions a provider chooses to support.
	// Nothing prevents providers from participating in regions outside of their geographic location however they may get less deals since the latency is likely to be higher
	Regions []string
}

// RemoteStorer is the interface used to store content on decentralized storage networks (Filecoin)
type RemoteStorer interface {
	Start(context.Context) error
	Store(context.Context, storage.Params) (*storage.Receipt, error)
	GetMarketQuote(context.Context, storage.QuoteParams) (*storage.Quote, error)
}

type node struct {
	host host.Host
	ds   datastore.Batching
	bs   blockstore.Blockstore
	ms   *multistore.MultiStore
	dag  ipldformat.DAGService
	gs   graphsync.GraphExchange
	ps   *pubsub.PubSub
	exch *pop.Exchange
	rs   RemoteStorer

	mu     sync.Mutex
	notify func(Notify)

	qmu    sync.Mutex // mutex for the storage quote
	sQuote *storage.Quote
}

// New puts together all the components of the ipfs node
func New(ctx context.Context, opts Options) (*node, error) {
	var err error
	nd := &node{}

	dsopts := badgerds.DefaultOptions
	dsopts.SyncWrites = false
	dsopts.Truncate = true

	nd.ds, err = badgerds.NewDatastore(filepath.Join(opts.RepoPath, "datastore"), &dsopts)
	if err != nil {
		return nil, err
	}

	nd.bs = blockstore.NewBlockstore(nd.ds)

	nd.ms, err = multistore.NewMultiDstore(nd.ds)
	if err != nil {
		return nil, err
	}

	nd.dag = merkledag.NewDAGService(blockservice.New(nd.bs, offline.Exchange(nd.bs)))

	ks, err := keystore.NewFSKeystore(filepath.Join(opts.RepoPath, "keystore"))
	if err != nil {
		return nil, err
	}

	priv, err := Libp2pKey(ks)
	if err != nil {
		return nil, err
	}

	gater, err := conngater.NewBasicConnectionGater(nd.ds)
	if err != nil {
		return nil, err
	}

	nd.host, err = libp2p.New(
		ctx,
		libp2p.Identity(priv),
		libp2p.ConnectionManager(connmgr.NewConnManager(
			20,             // Lowwater
			60,             // HighWater,
			20*time.Second, // GracePeriod
		)),
		libp2p.ConnectionGater(gater),
		libp2p.DisableRelay(),
		// Attempt to open ports using uPNP for NATed hosts.
		libp2p.NATPortMap(),
		libp2p.EnableNATService(),
		// Let this host use the DHT to find other hosts
		libp2p.Routing(func(h host.Host) (routing.PeerRouting, error) {
			return dht.New(ctx, h)
		}),
	)
	if err != nil {
		return nil, err
	}

	nd.ps, err = pubsub.NewGossipSub(ctx, nd.host)
	if err != nil {
		return nil, err
	}

	nd.gs = gsimpl.New(ctx,
		gsnet.NewFromLibp2pHost(nd.host),
		storeutil.LoaderForBlockstore(nd.bs),
		storeutil.StorerForBlockstore(nd.bs),
	)

	// Convert region names to region structs
	var regions []supply.Region
	for _, rstring := range opts.Regions {
		if r := supply.Regions[rstring]; r.Name != "" {
			regions = append(regions, r)
			continue
		}
		// We also support custom regions if users want their own provider subnet
		regions = append(regions, supply.Region{
			Name: rstring,
			Code: supply.CustomRegion,
		})
	}

	settings := pop.Settings{
		Datastore:  nd.ds,
		Blockstore: nd.bs,
		MultiStore: nd.ms,
		Host:       nd.host,
		PubSub:     nd.ps,
		GraphSync:  nd.gs,
		RepoPath:   opts.RepoPath,
		// TODO: secure keystore
		Keystore:            ks,
		FilecoinRPCEndpoint: opts.FilEndpoint,
		FilecoinRPCHeader: http.Header{
			"Authorization": []string{opts.FilToken},
		},
		Regions: regions,
	}

	nd.exch, err = pop.NewExchange(ctx, settings)
	if err != nil {
		return nil, err
	}
	if opts.PrivKey != "" {
		nd.importAddress(opts.PrivKey)
	}

	nd.rs, err = storage.New(
		nd.host,
		nd.bs,
		nd.ms,
		namespace.Wrap(nd.ds, datastore.NewKey("/storage/client")),
		nd.exch.DataTransfer(),
		nd.exch.Wallet(),
		nd.exch.FilecoinAPI(),
		nd.exch.Supply(),
	)
	if err != nil {
		return nil, err
	}
	err = nd.rs.Start(ctx)
	if err != nil {
		return nil, err
	}
	// start connecting with peers
	go nd.bootstrap(ctx, opts.BootstrapPeers)

	return nd, nil

}

// send hits out notify callback if we attached one
func (nd *node) send(n Notify) {
	nd.mu.Lock()
	notify := nd.notify
	nd.mu.Unlock()

	if notify != nil {
		notify(n)
	} else {
		log.Info().Interface("notif", n).Msg("nil notify callback; dropping")
	}
}

// Ping the node for sanity check more than anything
func (nd *node) Ping(ctx context.Context, who string) {
	sendErr := func(err error) {
		nd.send(Notify{PingResult: &PingResult{
			Err: err.Error(),
		}})
	}
	// Ping local node if no address is passed
	if who == "" {
		peers := nd.connPeers()
		var pstr []string
		for _, p := range peers {
			pstr = append(pstr, p.String())
		}
		var addrs []string
		for _, a := range nd.host.Addrs() {
			addrs = append(addrs, a.String())
		}
		nd.send(Notify{PingResult: &PingResult{
			ID:    nd.host.ID().String(),
			Addrs: addrs,
			Peers: pstr,
		}})
		return
	}

	addr, err := address.NewFromString(who)
	if err == nil {
		info, err := nd.exch.StoragePeerInfo(ctx, addr)
		if err != nil {
			sendErr(err)
			return
		}
		err = nd.ping(ctx, *info)
		if err != nil {
			sendErr(err)
		}
		return
	}
	pid, err := peer.Decode(who)
	if err == nil {
		err = nd.ping(ctx, nd.host.Peerstore().PeerInfo(pid))
		if err != nil {
			sendErr(err)
		}
		return
	}
	sendErr(ErrInvalidPeer)
}

func (nd *node) ping(ctx context.Context, pi peer.AddrInfo) error {
	strs := make([]string, 0, len(pi.Addrs))
	for _, a := range pi.Addrs {
		strs = append(strs, a.String())
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	pings := ping.Ping(ctx, nd.host, pi.ID)

	select {
	case res := <-pings:
		if res.Error != nil {
			return res.Error
		}
		nd.send(Notify{PingResult: &PingResult{
			ID:             pi.ID.String(),
			Addrs:          strs,
			LatencySeconds: res.RTT.Seconds(),
		}})
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Add a file to the Workdag
func (nd *node) Add(ctx context.Context, args *AddArgs) {

	sendErr := func(err error) {
		nd.send(Notify{
			AddResult: &AddResult{
				Err: err.Error(),
			},
		})
	}

	w, err := NewWorkdag(nd.ms, nd.ds)
	if err != nil {
		sendErr(err)
		return
	}
	root, err := w.Add(ctx, AddOptions{
		Path:      args.Path,
		ChunkSize: int64(args.ChunkSize),
	})
	if err != nil {
		sendErr(err)
		return
	}
	// We could get the size from the index entry but DAGStat gives more feedback into
	// how the file actually got chunked
	stats, err := pop.DAGStat(ctx, w.Store().Bstore, root, pop.AllSelector())
	if err != nil {
		log.Error().Err(err).Msg("record not found")
	}
	nd.send(Notify{
		AddResult: &AddResult{
			Cid:       root.String(),
			Size:      filecoin.SizeStr(filecoin.NewInt(uint64(stats.Size))),
			NumBlocks: stats.NumBlocks,
		}})
}

// Status prints the current workdag index. It shows which files have been added but not yet committed
// and pushed to the network
func (nd *node) Status(ctx context.Context, args *StatusArgs) {
	sendErr := func(err error) {
		nd.send(Notify{
			StatusResult: &StatusResult{
				Err: err.Error(),
			},
		})
	}

	w, err := NewWorkdag(nd.ms, nd.ds)
	if err != nil {
		sendErr(err)
		return
	}
	s, err := w.Status()
	if err != nil {
		sendErr(err)
		return
	}

	nd.send(Notify{
		StatusResult: &StatusResult{
			Output: s.String(),
		},
	})
}

// Pack packages multiple unix FS dags into an archive for storage
// it also registers it in our supply meaning from now on we can provide to
// any peer trying to retrieve it
func (nd *node) Pack(ctx context.Context, args *PackArgs) {
	sendErr := func(err error) {
		nd.send(Notify{
			PackResult: &PackResult{
				Err: err.Error(),
			},
		})
	}
	w, err := NewWorkdag(nd.ms, nd.ds)
	if err != nil {
		sendErr(err)
		return
	}
	status, err := w.Status()
	if err != nil {
		sendErr(err)
		return
	}
	if len(status) == 0 {
		sendErr(ErrNoDAGForPacking)
		return
	}

	ref, err := w.Commit(ctx, CommitOptions{})
	if err != nil {
		sendErr(err)
		return
	}
	err = nd.exch.Supply().Register(ref.PayloadCID, uint64(ref.PayloadSize), ref.StoreID)
	if err != nil {
		sendErr(err)
		return
	}
	nd.send(Notify{
		PackResult: &PackResult{
			DataCID:   ref.PayloadCID.String(),
			DataSize:  ref.PayloadSize,
			PieceCID:  ref.PieceCID.String(),
			PieceSize: int64(ref.PieceSize),
		},
	})
}

// getCommit is an internal function to select a commit with a given string cid
// it is used when quoting the commit storage price or pushing to storage providers
func (nd *node) getCommit(cstr string) (*DataRef, error) {
	w, err := NewWorkdag(nd.ms, nd.ds)
	if err != nil {
		return nil, err
	}
	idx, err := w.Index()
	if err != nil {
		return nil, err
	}
	if len(idx.Commits) == 0 {
		return nil, ErrDAGNotPacked
	}
	com := idx.Commits[len(idx.Commits)-1]
	// Select the commit with the matching CID
	// TODO: should prob error out if we don't find it
	if cstr != "" {
		ccid, err := cid.Parse(cstr)
		if err != nil {
			return nil, err
		}
		for _, c := range idx.Commits {
			if ccid.Equals(c.PayloadCID) {
				com = c
			}
		}
	}

	return com, nil
}

// Quote returns an estimation of market price for storing a commit on Filecoin
func (nd *node) Quote(ctx context.Context, args *QuoteArgs) {
	sendErr := func(err error) {
		nd.send(Notify{
			QuoteResult: &QuoteResult{
				Err: err.Error(),
			},
		})
	}
	if !nd.exch.IsFilecoinOnline() {
		sendErr(ErrFilecoinRPCOffline)
		return
	}
	com, err := nd.getCommit(args.Ref)
	if err != nil {
		sendErr(err)
		return
	}
	quote, err := nd.rs.GetMarketQuote(ctx, storage.QuoteParams{
		PieceSize: uint64(com.PieceSize),
		Duration:  args.Duration,
		RF:        args.StorageRF,
		MaxPrice:  args.MaxPrice,
	})
	nd.qmu.Lock()
	nd.sQuote = quote
	nd.qmu.Unlock()

	if err != nil {
		sendErr(err)
		return
	}
	quotes := make(map[string]string)
	for _, m := range quote.Miners {
		addr := m.Info.Address
		quotes[addr.String()] = quote.Prices[addr].String()
	}

	nd.send(Notify{
		QuoteResult: &QuoteResult{
			Ref:    com.PayloadCID.String(),
			Quotes: quotes,
		},
	})
}

// Push deploys a committed DAG archive for storage
func (nd *node) Push(ctx context.Context, args *PushArgs) {
	sendErr := func(err error) {
		nd.send(Notify{
			PushResult: &PushResult{
				Err: err.Error(),
			},
		})
	}
	com, err := nd.getCommit(args.Ref)
	if err != nil {
		sendErr(err)
		return
	}

	if !args.CacheOnly && args.StorageRF > 0 {
		if !nd.exch.IsFilecoinOnline() {
			sendErr(ErrFilecoinRPCOffline)
			return
		}

		nd.qmu.Lock()
		if nd.sQuote == nil {
			nd.qmu.Unlock()
			sendErr(ErrQuoteNotFound)
			return
		}
		quote := nd.sQuote
		nd.qmu.Unlock()

		var miners []storage.Miner
		for _, m := range quote.Miners {
			addr := m.Info.Address
			if args.Miners[addr.String()] {
				miners = append(miners, m)
			}
		}

		rcpt, err := nd.rs.Store(ctx, storage.NewParams(
			com.PayloadCID,
			args.Duration,
			nd.exch.Wallet().DefaultAddress(),
			miners,
		))
		if err != nil {
			sendErr(err)
			return
		}
		if len(rcpt.DealRefs) == 0 {
			sendErr(ErrAllDealsFailed)
			return
		}
		var pr PushResult
		for _, m := range rcpt.Miners {
			pr.Miners = append(pr.Miners, m.String())
		}
		for _, d := range rcpt.DealRefs {
			pr.Deals = append(pr.Deals, d.String())
		}
		nd.send(Notify{
			PushResult: &pr,
		})
	}

	if !args.NoCache && args.CacheRF > 0 {
		// TODO: adjust timeout?
		ctx, cancel := context.WithTimeout(ctx, 1*time.Hour)
		defer cancel()

		res, err := nd.exch.Supply().Dispatch(
			supply.Request{
				PayloadCID: com.PayloadCID,
				Size:       uint64(com.PayloadSize),
			},
			supply.DispatchOptions{
				StoreID: com.StoreID,
			})
		defer res.Close()
		if err != nil {
			sendErr(err)
			return
		}
		for {
			// Right now we only wait for 1 peer to receive the content but we could wait for
			// more peers, the question is when to stop as we don't know exactly how many will retrieve
			rec, err := res.Next(ctx)
			nd.send(Notify{
				PushResult: &PushResult{
					Caches: []string{
						rec.Provider.String(),
					},
				},
			})
			if err != nil {
				sendErr(ctx.Err())
			}
			return
		}
	}
	// We shouldn't end up in this state as it's the command client role to
	// validate we won't but just in case we return an empty result
	nd.send(Notify{
		PushResult: &PushResult{},
	})
}

// Get sends a request for content with the given arguments. It also sends feedback to any open cli
// connections
func (nd *node) Get(ctx context.Context, args *GetArgs) {
	sendErr := func(err error) {
		nd.send(Notify{
			GetResult: &GetResult{
				Err: err.Error(),
			}})
	}
	p := path.FromString(args.Cid)
	// /<cid>/path/file.ext => cid, ["path", file.ext"]
	root, segs, err := path.SplitAbsPath(p)
	if err != nil {
		sendErr(err)
		return
	}
	// Check our supply if we may already have it
	sID, err := nd.exch.Supply().GetStoreID(root)
	if err == nil && args.Out != "" {
		err := nd.export(ctx, root, segs[0], args.Out, sID)
		if err != nil {
			sendErr(err)
			return
		}
	}
	if err == nil {
		nd.send(Notify{
			GetResult: &GetResult{
				Local: true,
			}})
		return
	}
	// Only support a single segment for now
	args.Segments = segs
	// Log progress
	if args.Verbose {
		unsub := nd.exch.Retrieval().Client().SubscribeToEvents(
			func(event client.Event, state deal.ClientState) {
				log.Info().
					Str("event", client.Events[event]).
					Str("status", deal.Statuses[state.Status]).
					Uint64("bytes received", state.TotalReceived).
					Msg("Retrieving")
			},
		)
		defer unsub()
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(args.Timeout)*time.Minute)
	defer cancel()
	err = nd.get(ctx, root, args)
	if err != nil {
		sendErr(err)
	}
}

// get is a synchronous content retrieval operation which can be called by a CLI request or HTTP
func (nd *node) get(ctx context.Context, c cid.Cid, args *GetArgs) error {
	// TODO handle different predefined selectors

	start := time.Now()

	session, err := nd.exch.NewSession(ctx, c)
	if err != nil {
		return err
	}
	var offer *deal.Offer
	var discDuration time.Duration
	if args.Miner != "" {
		miner, err := address.NewFromString(args.Miner)
		if err != nil {
			return err
		}
		info, err := nd.exch.StoragePeerInfo(ctx, miner)
		if err != nil {
			// Maybe fall back to a discovery session?
			return err
		}

		offer, err = session.QueryMiner(ctx, info.ID)
		if err != nil {
			return err
		}
		now := time.Now()
		discDuration = now.Sub(start)
	}
	if offer == nil {
		// Gossip discovery shouldn't last more than 5 seconds
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		offer, err = session.QueryGossip(ctx)
		if err != nil {
			return err
		}
		now := time.Now()
		discDuration = now.Sub(start)
	}

	err = session.SyncBlocks(ctx, offer)
	if err != nil {
		return err
	}

	did, err := session.DealID()
	if err != nil {
		return err
	}

	nd.send(Notify{
		GetResult: &GetResult{
			DealID:       did.String(),
			TotalPrice:   filecoin.FIL(offer.Response.PieceRetrievalPrice()).Short(),
			PricePerByte: filecoin.FIL(offer.Response.MinPricePerByte).Short(),
			UnsealPrice:  filecoin.FIL(offer.Response.UnsealPrice).Short(),
			PieceSize:    filecoin.SizeStr(filecoin.NewInt(offer.Response.Size)),
		},
	})

	select {
	case err := <-session.Done():
		if err != nil {
			return err
		}
		end := time.Now()
		transDuration := end.Sub(start) - discDuration
		if args.Out != "" {
			err := nd.export(ctx, c, args.Segments[0], args.Out, session.StoreID())
			if err != nil {
				return err
			}
		}
		// Register new blocks in our supply by default
		err = nd.exch.Supply().Register(c, offer.Response.Size, session.StoreID())
		if err != nil {
			return err
		}
		nd.send(Notify{
			GetResult: &GetResult{
				DiscLatSeconds:  discDuration.Seconds(),
				TransLatSeconds: transDuration.Seconds(),
			},
		})
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// extractFile from an archive
func (nd *node) extractFile(ctx context.Context, root cid.Cid, name string, sid multistore.StoreID) (files.Node, error) {
	w, err := NewWorkdag(nd.ms, nd.ds)
	if err != nil {
		return nil, err
	}

	fls, err := w.Unpack(ctx, root, sid)
	if err != nil {
		return nil, err
	}

	// We only support flat structures for now
	// would rather not add mfs into the mix
	file, ok := fls[name]
	// This won't be necessary once we support selectors
	// for now we have to retrieve a whole archive to access a single file
	if !ok {
		return nil, ErrNodeNotFound
	}
	return file, nil
}

// export extracts a given file from an archive and writes it to a given path
func (nd *node) export(ctx context.Context, root cid.Cid, name, out string, sid multistore.StoreID) error {
	file, err := nd.extractFile(ctx, root, name, sid)
	if err != nil {
		return err
	}
	err = files.WriteTo(file, out)
	if err != nil {
		return err
	}
	return nil
}

// bootstrap connects to a list of provided peer addresses, libp2p then uses dht discovery
// to connect with all the peers the node is aware of
func (nd *node) bootstrap(ctx context.Context, bpeers []string) error {
	var peers []peer.AddrInfo
	for _, addrStr := range bpeers {
		addr, err := ma.NewMultiaddr(addrStr)
		if err != nil {
			continue
		}
		addrInfo, err := peer.AddrInfoFromP2pAddr(addr)
		if err != nil {
			continue
		}
		peers = append(peers, *addrInfo)
	}

	var wg sync.WaitGroup
	peerInfos := make(map[peer.ID]*peerstore.PeerInfo, len(peers))
	for _, pii := range peers {
		pi, ok := peerInfos[pii.ID]
		if !ok {
			pi = &peerstore.PeerInfo{ID: pii.ID}
			peerInfos[pi.ID] = pi
		}
		pi.Addrs = append(pi.Addrs, pii.Addrs...)
	}

	wg.Add(len(peerInfos))
	for _, peerInfo := range peerInfos {
		go func(peerInfo *peerstore.PeerInfo) {
			defer wg.Done()
			err := nd.host.Connect(ctx, *peerInfo)
			if err != nil {
				fmt.Printf("failed to connect to %s: %s\n", peerInfo.ID, err)
			}
		}(peerInfo)
	}
	wg.Wait()
	return nil
}

// connPeers returns a list of connected peer IDs
func (nd *node) connPeers() []peer.ID {
	conns := nd.host.Network().Conns()
	var out []peer.ID
	for _, c := range conns {
		pid := c.RemotePeer()
		out = append(out, pid)
	}
	return out
}

// importAddress from a hex encoded private key to use as default on the exchange instead of
// the auto generated one. This is mostly for development and will be reworked into a nicer command
// eventually
func (nd *node) importAddress(pk string) {
	var iki wallet.KeyInfo
	data, err := hex.DecodeString(pk)
	if err != nil {
		log.Error().Err(err).Msg("hex.DecodeString(opts.PrivKey)")
	}
	if err := json.Unmarshal(data, &iki); err != nil {
		log.Error().Err(err).Msg("json.Unmarshal(PrivKey)")
	}

	addr, err := nd.exch.Wallet().ImportKey(context.TODO(), &iki)
	if err != nil {
		log.Error().Err(err).Msg("Wallet.ImportKey")
	} else {
		fmt.Printf("==> Imported private key for %s.\n", addr.String())
		err := nd.exch.Wallet().SetDefaultAddress(addr)
		if err != nil {
			log.Error().Err(err).Msg("Wallet.SetDefaultAddress")
		}
	}
}

// Libp2pKey gets a libp2p host private key from the keystore if available or generates a new one
func Libp2pKey(ks keystore.Keystore) (ci.PrivKey, error) {
	k, err := ks.Get(KLibp2pHost)
	if err == nil {
		return k, nil
	}
	if !errors.Is(err, keystore.ErrNoSuchKey) {
		return nil, err
	}
	pk, _, err := ci.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := ks.Put(KLibp2pHost, pk); err != nil {
		return nil, err
	}
	return pk, nil
}
