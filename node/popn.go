package node

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-commp-utils/writer"
	"github.com/filecoin-project/go-multistore"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/ipfs/go-blockservice"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
	badgerds "github.com/ipfs/go-ds-badger"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	chunk "github.com/ipfs/go-ipfs-chunker"
	offline "github.com/ipfs/go-ipfs-exchange-offline"
	files "github.com/ipfs/go-ipfs-files"
	keystore "github.com/ipfs/go-ipfs-keystore"
	ipldformat "github.com/ipfs/go-ipld-format"
	"github.com/ipfs/go-merkledag"
	"github.com/ipfs/go-path"
	"github.com/ipfs/go-unixfs/importer/balanced"
	"github.com/ipfs/go-unixfs/importer/helpers"
	"github.com/ipld/go-car"
	"github.com/ipld/go-ipld-prime"
	"github.com/libp2p/go-libp2p"
	connmgr "github.com/libp2p/go-libp2p-connmgr"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/routing"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/p2p/net/conngater"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
	"github.com/myelnet/pop/build"
	"github.com/myelnet/pop/exchange"
	"github.com/myelnet/pop/filecoin"
	"github.com/myelnet/pop/filecoin/storage"
	"github.com/myelnet/pop/internal/utils"
	"github.com/myelnet/pop/retrieval/client"
	"github.com/myelnet/pop/retrieval/deal"
	sel "github.com/myelnet/pop/selectors"
	"github.com/myelnet/pop/wallet"
	"github.com/rs/zerolog/log"
)

// KLibp2pHost is the keystore key used for storing the host private key
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
	// Capacity is the maxium storage capacity dedicated to the exchange
	Capacity uint64
	// ReplInterval defines how often the node attempts to find new content from connected peers
	ReplInterval time.Duration
}

// RemoteStorer is the interface used to store content on decentralized storage networks (Filecoin)
type RemoteStorer interface {
	Start(context.Context) error
	Store(context.Context, storage.Params) (*storage.Receipt, error)
	GetMarketQuote(context.Context, storage.QuoteParams) (*storage.Quote, error)
	PeerInfo(context.Context, address.Address) (*peer.AddrInfo, error)
}

type node struct {
	host host.Host
	ds   datastore.Batching
	bs   blockstore.Blockstore
	ms   *multistore.MultiStore
	dag  ipldformat.DAGService
	exch *exchange.Exchange
	rs   RemoteStorer

	mu     sync.Mutex
	notify func(Notify)

	// cache the last storage quote
	qmu    sync.Mutex
	sQuote *storage.Quote

	// keep track of an ongoing transaction
	txmu sync.Mutex
	tx   *exchange.Tx
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
	priv, err := utils.Libp2pKey(ks)
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
		// user-agent is sent along the identify protocol
		libp2p.UserAgent("pop-"+build.Version),
	)
	if err != nil {
		return nil, err
	}

	// Convert region names to region structs
	regions := exchange.ParseRegions(opts.Regions)

	eopts := exchange.Options{
		Blockstore:          nd.bs,
		MultiStore:          nd.ms,
		Keystore:            ks,
		RepoPath:            opts.RepoPath,
		FilecoinRPCEndpoint: opts.FilEndpoint,
		FilecoinRPCHeader: http.Header{
			"Authorization": []string{opts.FilToken},
		},
		Regions:      regions,
		Capacity:     opts.Capacity,
		ReplInterval: opts.ReplInterval,
	}

	nd.exch, err = exchange.New(ctx, nd.host, nd.ds, eopts)
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
		nd.exch,
	)
	if err != nil {
		return nil, err
	}
	err = nd.rs.Start(ctx)
	if err != nil {
		return nil, err
	}
	// start connecting with peers
	go utils.Bootstrap(ctx, nd.host, opts.BootstrapPeers)

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
			ID:      nd.host.ID().String(),
			Addrs:   addrs,
			Peers:   pstr,
			Version: build.Version,
		}})
		return
	}

	addr, err := address.NewFromString(who)
	if err == nil {
		info, err := nd.rs.PeerInfo(ctx, addr)
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
		var v string
		agent, _ := nd.host.Peerstore().Get(pi.ID, "AgentVersion")
		vparts := strings.Split(agent.(string), "-")
		if len(vparts) == 3 {
			v = fmt.Sprintf("%s-%s", vparts[1], vparts[2])
		}
		nd.send(Notify{PingResult: &PingResult{
			ID:             pi.ID.String(),
			Addrs:          strs,
			LatencySeconds: res.RTT.Seconds(),
			Version:        v,
		}})
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Put a file into a new or pending transaction
func (nd *node) Put(ctx context.Context, args *PutArgs) {
	sendErr := func(err error) {
		nd.send(Notify{
			PutResult: &PutResult{
				Err: err.Error(),
			},
		})
	}

	nd.txmu.Lock()
	defer nd.txmu.Unlock()
	if nd.tx == nil {
		nd.tx = nd.exch.Tx(ctx)
	}
	nd.tx.SetChunkSize(int64(args.ChunkSize))
	err := nd.tx.PutFile(args.Path)
	if err != nil {
		sendErr(err)
		return
	}
	status, err := nd.tx.Status()
	if err != nil {
		sendErr(err)
		return
	}
	froot := status[exchange.KeyFromPath(args.Path)].Value
	// We could get the size from the index entry but DAGStat gives more feedback into
	// how the file actually got chunked
	stats, err := utils.Stat(ctx, nd.tx.Store(), froot, sel.All())
	if err != nil {
		log.Error().Err(err).Msg("record not found")
	}
	nd.send(Notify{
		PutResult: &PutResult{
			Cid:       froot.String(),
			Size:      filecoin.SizeStr(filecoin.NewInt(uint64(stats.Size))),
			NumBlocks: stats.NumBlocks,
			Root:      nd.tx.Root().String(),
		}})
}

// Status prints the current transaction status. It shows which files have been added but not yet committed
// to the network
func (nd *node) Status(ctx context.Context, args *StatusArgs) {
	sendErr := func(err error) {
		nd.send(Notify{
			StatusResult: &StatusResult{
				Err: err.Error(),
			},
		})
	}
	nd.txmu.Lock()
	defer nd.txmu.Unlock()
	if nd.tx != nil {
		s, err := nd.tx.Status()
		if err != nil {
			sendErr(err)
			return
		}

		nd.send(Notify{
			StatusResult: &StatusResult{
				RootCid: nd.tx.Root().String(),
				Entries: s.String(),
			},
		})
		return
	}
	sendErr(errors.New("no pending transaction"))
}

// getRef is an internal function to find a ref with a given string cid
// it is used when quoting the commit storage price or pushing to storage providers
func (nd *node) getRef(cstr string) (*exchange.DataRef, error) {
	// Select the commit with the matching CID
	// TODO: should prob error out if we don't find it
	if cstr != "" {
		ccid, err := cid.Parse(cstr)
		if err != nil {
			return nil, err
		}
		ref, err := nd.exch.Index().PeekRef(ccid)
		if err != nil {
			return nil, err
		}
		return ref, nil
	}

	nd.txmu.Lock()
	defer nd.txmu.Unlock()
	if nd.tx != nil {
		return nd.tx.Ref(), nil
	}

	return nil, ErrDAGNotPacked
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
	com, err := nd.getRef(args.Ref)
	if err != nil {
		sendErr(err)
		return
	}
	store, err := nd.ms.Get(com.StoreID)
	if err != nil {
		sendErr(err)
		return
	}
	piece, err := nd.archive(ctx, store.DAG, com.PayloadCID)
	if err != nil {
		sendErr(err)
		return
	}

	quote, err := nd.rs.GetMarketQuote(ctx, storage.QuoteParams{
		PieceSize: uint64(piece.PieceSize),
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

// Commit a content transaction for storage
func (nd *node) Commit(ctx context.Context, args *CommArgs) {
	sendErr := func(err error) {
		nd.send(Notify{
			CommResult: &CommResult{
				Err: err.Error(),
			},
		})
	}
	nd.txmu.Lock()
	nd.tx.SetCacheRF(args.CacheRF)
	err := nd.tx.Commit()
	if err != nil {
		sendErr(err)
		return
	}
	ref := nd.tx.Ref()
	nd.tx.WatchDispatch(func(r exchange.PRecord) {
		nd.send(Notify{
			CommResult: &CommResult{
				Caches: []string{
					r.Provider.String(),
				},
			},
		})
	})
	nd.tx.Close()
	nd.tx = nil
	nd.txmu.Unlock()

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
			ref.PayloadCID,
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
		var cr CommResult
		for _, m := range rcpt.Miners {
			cr.Miners = append(cr.Miners, m.String())
		}
		for _, d := range rcpt.DealRefs {
			cr.Deals = append(cr.Deals, d.String())
		}
		nd.send(Notify{
			CommResult: &cr,
		})
	}
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
	// Check if we're trying to get from an ongoing transaction
	nd.txmu.Lock()
	if nd.tx != nil && nd.tx.Root() == root {
		f, err := nd.tx.GetFile(segs[0])
		if err != nil {
			sendErr(err)
			return
		}
		if args.Out != "" {
			err = files.WriteTo(f, args.Out)
			if err != nil {
				sendErr(err)
				return
			}
		}
		nd.send(Notify{
			GetResult: &GetResult{
				Local: true,
			},
		})
		return
	}
	nd.txmu.Unlock()

	// Only support a single segment for now
	args.Key = segs[0]
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
	// Check our supply if we may already have it
	f, err := nd.exch.Tx(ctx, exchange.WithRoot(c)).GetFile(args.Key)
	if err == nil && args.Out != "" {
		if err != nil {
			return err
		}
		err = files.WriteTo(f, args.Out)
		if err != nil {
			return err
		}
	}
	if err == nil {
		nd.send(Notify{
			GetResult: &GetResult{
				Local: true,
			}})
		return nil
	}

	var strategy exchange.SelectionStrategy
	switch args.Strategy {
	case "SelectFirst":
		strategy = exchange.SelectFirst
	case "SelectCheapest":
		strategy = exchange.SelectCheapest(5, 4*time.Second)
	case "SelectFirstLowerThan":
		strategy = exchange.SelectFirstLowerThan(abi.NewTokenAmount(5))
	default:
		return errors.New("unknown strategy")
	}

	start := time.Now()

	tx := nd.exch.Tx(ctx, exchange.WithRoot(c), exchange.WithStrategy(strategy), exchange.WithTriage())
	defer tx.Close()
	var sl ipld.Node
	if args.Key != "" {
		sl = sel.Key(args.Key)
	} else {
		sl = sel.All()
	}
	if err := tx.Query(sl); err != nil {
		return err
	}
	// We can query a specific miner on top of gossip
	if args.Miner != "" {
		miner, err := address.NewFromString(args.Miner)
		if err != nil {
			return err
		}
		info, err := nd.rs.PeerInfo(ctx, miner)
		if err != nil {
			// Maybe fall back to a discovery session?
			return err
		}
		err = tx.QueryFrom(*info, args.Key)
		if err != nil {
			// Maybe we shouldn't fail here, the transfer could still work with other peers
			return err
		}
	}

	// Triage waits until we select the first offer it might not mean the first
	// offer that we receive depending on the strategy used
	selection, err := tx.Triage()
	if err != nil {
		return err
	}
	now := time.Now()
	discDuration := now.Sub(start)
	resp := selection.Offer.Response

	// TODO: accept all by default but we should be able to pass flag to provide
	// confirmation before retrieving
	selection.Incline()

	var dref exchange.DealRef
	select {
	case dref = <-tx.Ongoing():
	case <-ctx.Done():
		return ctx.Err()
	}

	nd.send(Notify{
		GetResult: &GetResult{
			DealID:       dref.ID.String(),
			TotalPrice:   filecoin.FIL(resp.PieceRetrievalPrice()).Short(),
			PricePerByte: filecoin.FIL(resp.MinPricePerByte).Short(),
			UnsealPrice:  filecoin.FIL(resp.UnsealPrice).Short(),
			PieceSize:    filecoin.SizeStr(filecoin.NewInt(resp.Size)),
		},
	})

	select {
	case res := <-tx.Done():
		if res.Err != nil {
			return res.Err
		}
		tx.Close()
		end := time.Now()
		transDuration := end.Sub(start) - discDuration
		if args.Out != "" {
			f, err := tx.GetFile(args.Key)
			if err != nil {
				return err
			}
			err = files.WriteTo(f, args.Out)
			if err != nil {
				return err
			}
		}
		// Register new blocks in our supply by default
		err = nd.exch.Index().SetRef(&exchange.DataRef{
			PayloadCID:  c,
			StoreID:     tx.StoreID(),
			PayloadSize: int64(res.Size),
		})
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

// List returns all the roots for the content stored by this node
func (nd *node) List(ctx context.Context, args *ListArgs) {
	list, err := nd.exch.Index().ListRefs()
	if err != nil {
		nd.send(Notify{
			ListResult: &ListResult{
				Err: err.Error(),
			},
		})
		return
	}
	if len(list) == 0 {
		nd.send(Notify{
			ListResult: &ListResult{
				Err: "no refs stored",
			},
		})
		return
	}
	for i, ref := range list {
		nd.send(Notify{
			ListResult: &ListResult{
				Root: ref.PayloadCID.String(),
				Size: ref.PayloadSize,
				Freq: ref.Freq,
				Last: i == len(list)-1,
			},
		})
	}
}

// Add a buffer into the node global DAG. These DAGs can eventually be put into transactions.
func (nd *node) Add(ctx context.Context, dag ipldformat.DAGService, buf io.Reader) (cid.Cid, error) {
	bufferedDS := ipldformat.NewBufferedDAG(ctx, dag)

	prefix, err := merkledag.PrefixForCidVersion(1)
	if err != nil {
		return cid.Undef, err
	}
	prefix.MhType = exchange.DefaultHashFunction

	params := helpers.DagBuilderParams{
		Maxlinks:   1024,
		RawLeaves:  true,
		CidBuilder: prefix,
		Dagserv:    bufferedDS,
	}

	db, err := params.New(chunk.NewSizeSplitter(buf, int64(128000)))
	if err != nil {
		return cid.Undef, err
	}

	n, err := balanced.Layout(db)
	if err != nil {
		return cid.Undef, err
	}

	err = bufferedDS.Commit()
	if err != nil {
		return cid.Undef, err
	}

	return n.Cid(), nil
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

// PieceRef contains Filecoin metadata about a storage piece
type PieceRef struct {
	CID         cid.Cid
	PayloadSize int64
	PieceSize   abi.PaddedPieceSize
}

// archive a DAG into a CAR
func (nd *node) archive(ctx context.Context, DAG ipldformat.DAGService, root cid.Cid) (*PieceRef, error) {
	wr := &writer.Writer{}
	bw := bufio.NewWriterSize(wr, int(writer.CommPBuf))

	err := car.WriteCar(ctx, DAG, []cid.Cid{root}, wr)
	if err != nil {
		return nil, err
	}

	if err := bw.Flush(); err != nil {
		return nil, err
	}

	dataCIDSize, err := wr.Sum()
	if err != nil {
		return nil, err
	}

	return &PieceRef{
		CID:         dataCIDSize.PieceCID,
		PayloadSize: dataCIDSize.PayloadSize,
		PieceSize:   dataCIDSize.PieceSize,
	}, nil
}
