package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/docker/go-units"
	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/ipfs/go-blockservice"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	badgerds "github.com/ipfs/go-ds-badger"
	"github.com/ipfs/go-graphsync/storeutil"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	chunk "github.com/ipfs/go-ipfs-chunker"
	offline "github.com/ipfs/go-ipfs-exchange-offline"
	files "github.com/ipfs/go-ipfs-files"
	keystore "github.com/ipfs/go-ipfs-keystore"
	cbor "github.com/ipfs/go-ipld-cbor"
	ipldformat "github.com/ipfs/go-ipld-format"
	"github.com/ipfs/go-merkledag"
	"github.com/ipfs/go-path"
	"github.com/ipfs/go-unixfs/importer/balanced"
	"github.com/ipfs/go-unixfs/importer/helpers"
	"github.com/ipfs/go-unixfs/importer/trickle"
	"github.com/libp2p/go-libp2p"
	connmgr "github.com/libp2p/go-libp2p-connmgr"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/routing"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/p2p/net/conngater"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
	tcp "github.com/libp2p/go-tcp-transport"
	websocket "github.com/libp2p/go-ws-transport"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/myelnet/go-multistore"
	"github.com/myelnet/pop/build"
	"github.com/myelnet/pop/exchange"
	"github.com/myelnet/pop/filecoin"
	"github.com/myelnet/pop/internal/utils"
	"github.com/myelnet/pop/metrics"
	"github.com/myelnet/pop/retrieval/client"
	"github.com/myelnet/pop/retrieval/deal"
	"github.com/myelnet/pop/retrieval/provider"
	sel "github.com/myelnet/pop/selectors"
	"github.com/myelnet/pop/wallet"
	"github.com/rs/zerolog/log"
)

// DhtPrefix sets a Pop prefix to be attached to the DHT protocols.
// For example: /pop/kad/1.0.0 instead of /ipfs/kad/1.0.0
const DhtPrefix = "/pop"

// KContentBatch is the keystore used for storing the root CID of the HAMT used to aggregate content for storage
const KContentBatch = "content-batch"

var (
	// ErrFilecoinRPCOffline is returned when the node is running without a provided filecoin api endpoint + token
	ErrFilecoinRPCOffline = errors.New("filecoin RPC is offline")

	// ErrAllDealsFailed is returned when all storage deals failed to get started
	ErrAllDealsFailed = errors.New("all deals failed")

	// ErrNoTx is returned when no transaction is staged and we attempt to commit
	ErrNoTx = errors.New("no tx to commit")

	// ErrNodeNotFound is returned when we cannot find the node in the given root
	ErrNodeNotFound = errors.New("node not found")

	// ErrQuoteNotFound is returned when we are trying to store but couldn't get a quote
	ErrQuoteNotFound = errors.New("quote not found")

	// ErrInvalidPeer is returned when trying to ping a peer with invalid peer ID or address
	ErrInvalidPeer = errors.New("invalid peer ID or address")
)

var (
	// DefaultChunkSize is the default size a chunk
	DefaultChunkSize int64 = 128_000

	// DefaultChunker is the default chunker of the DAG builder
	DefaultChunker = func(buf io.ReadSeeker) chunk.Splitter {
		return chunk.NewSizeSplitter(buf, DefaultChunkSize)
	}

	// DefaultLayout is the default layout of the DAG builder
	DefaultLayout = balanced.Layout
)

// Options determines configurations for the IPFS node
type Options struct {
	// RepoPath is the file system path to use to persist our datastore
	RepoPath string
	// UseInflux is flag determining wether we are pushing pushing statistics to InfluxDB.
	Metrics *metrics.Config
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
	// MaxPPB is the maximum price per byte
	MaxPPB int64
	// Regions is a list of regions a provider chooses to support.
	// Nothing prevents providers from participating in regions outside of their geographic location however they may get less deals since the latency is likely to be higher
	Regions []string
	// Capacity is the maximum storage capacity dedicated to the exchange
	Capacity uint64
	// ReplInterval defines how often the node attempts to find new content from connected peers
	ReplInterval time.Duration
	// Domains is a list of DNS names we can establish TLS handshakes with
	Domains []string
	// CancelFunc is used for gracefully shutting down the node
	CancelFunc context.CancelFunc
}

type node struct {
	host    host.Host
	ds      datastore.Batching
	bs      blockstore.Blockstore
	ms      *multistore.MultiStore
	is      cbor.IpldStore
	dag     ipldformat.DAGService
	exch    *exchange.Exchange
	metrics metrics.MetricsRecorder

	// opts keeps all the node params set when starting the node
	opts Options

	mu     sync.Mutex
	notify func(Notify)

	// keep track of an ongoing transaction
	txmu sync.Mutex
	tx   *exchange.Tx

	// Save context cancelFunc for graceful node shutdown
	cancelFunc context.CancelFunc
}

// New puts together all the components of the ipfs node
func New(ctx context.Context, opts Options) (*node, error) {
	var err error
	nd := &node{
		opts: opts,
	}

	dsopts := badgerds.DefaultOptions
	dsopts.SyncWrites = false
	dsopts.Truncate = true

	nd.ds, err = badgerds.NewDatastore(filepath.Join(opts.RepoPath, "datastore"), &dsopts)
	if err != nil {
		return nil, err
	}

	nd.ms, err = multistore.NewMultiDstore(nd.ds)
	if err != nil {
		return nil, err
	}

	nd.bs = blockstore.NewBlockstore(nd.ds)

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
		libp2p.ListenAddrStrings(
			"/ip4/0.0.0.0/tcp/41504",
			"/ip4/0.0.0.0/tcp/41505/ws",
		),
		// Explicitly declare transports
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Transport(websocket.New),
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
			return dht.New(ctx, h, dht.ProtocolPrefix(DhtPrefix))
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
		RepoPath:            opts.RepoPath,
		FilecoinRPCEndpoint: opts.FilEndpoint,
		Regions:             regions,
		Capacity:            opts.Capacity,
		ReplInterval:        opts.ReplInterval,
	}
	if opts.FilToken != "" {
		eopts.FilecoinRPCHeader = http.Header{
			"Authorization": []string{opts.FilToken},
		}
	}

	if opts.ReplInterval > 0 {
		fmt.Printf("==> Enabled periodic replication every %fs\n", opts.ReplInterval.Seconds())
	}

	if eopts.FilecoinRPCEndpoint != "" {
		eopts.FilecoinAPI, err = filecoin.NewLotusRPC(ctx, eopts.FilecoinRPCEndpoint, eopts.FilecoinRPCHeader)
		if err != nil {
			log.Error().Err(err).Msg("failed to connect with Lotus RPC")
		}
	}

	eopts.Wallet = wallet.NewFromKeystore(
		ks,
		wallet.WithFilAPI(eopts.FilecoinAPI),
	)

	var addr address.Address
	if eopts.Wallet.DefaultAddress() == address.Undef && opts.PrivKey == "" {
		addr, err = eopts.Wallet.NewKey(ctx, wallet.KTSecp256k1)
		if err != nil {
			return nil, err
		}
		fmt.Printf("==> Generated new FIL address: %s\n", addr)
	}

	if opts.Metrics != nil {
		eopts.WatchQueriesFunc = func(q deal.Query) {
			nd.metrics.Record(
				"routing-query",
				map[string]string{},
				map[string]interface{}{"content": q.PayloadCID.String()})
		}
	}

	nd.exch, err = exchange.New(ctx, nd.host, nd.ds, eopts)
	if err != nil {
		return nil, err
	}

	if opts.PrivKey != "" {
		err = nd.importPrivateKey(ctx, opts.PrivKey)
		if err != nil {
			return nil, err
		}
		fmt.Printf("==> Imported private keys: %s\n", nd.exch.Wallet().DefaultAddress())

	} else if addr.Empty() {
		fmt.Printf("==> Loaded default FIL address: %s\n", nd.exch.Wallet().DefaultAddress())
	}

	// set Max Price Per Byte
	fmt.Printf("==> Set default Max Price Per Byte (MaxPPB) at %d attoFIL\n", nd.opts.MaxPPB)

	nd.cancelFunc = opts.CancelFunc

	// start connecting with peers
	go utils.Bootstrap(ctx, nd.host, opts.BootstrapPeers)

	// remove unwanted blocks that might be in the blockstore but are removed from the index
	err = nd.exch.Index().CleanBlockStore(ctx)
	if err != nil {
		return nil, err
	}

	nd.metrics = metrics.New(opts.Metrics)

	// subscribe and log provider events
	nd.exch.Retrieval().Provider().SubscribeToEvents(func(event provider.Event, state deal.ProviderState) {
		tags := make(map[string]string)
		tags["requester"] = state.Receiver.String()
		tags["responder"] = nd.host.ID().String()

		values := make(map[string]interface{})
		values["event"] = provider.Events[event]
		values["status"] = deal.Statuses[state.Status]
		values["content"] = state.PayloadCID.String()

		nd.metrics.Record("retrieval", tags, values)
	})

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

// Off shutdown the node gracefully
func (nd *node) Off(ctx context.Context) {
	nd.send(Notify{OffResult: &OffResult{}})
	fmt.Println("==> Shut down pop daemon")

	nd.cancelFunc()
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
		info, err := nd.filMinerInfo(ctx, addr)
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

func (nd *node) filMinerInfo(ctx context.Context, addr address.Address) (*peer.AddrInfo, error) {
	miner, err := nd.exch.FilecoinAPI().StateMinerInfo(ctx, addr, filecoin.EmptyTSK)
	if err != nil {
		return nil, err
	}
	multiaddrs := make([]ma.Multiaddr, 0, len(miner.Multiaddrs))
	for _, a := range miner.Multiaddrs {
		maddr, err := ma.NewMultiaddrBytes(a)
		if err != nil {
			return nil, err
		}
		multiaddrs = append(multiaddrs, maddr)
	}
	if miner.PeerId == nil {
		return nil, fmt.Errorf("no peer id available")
	}
	if len(miner.Multiaddrs) == 0 {
		return nil, fmt.Errorf("no peer address available")
	}
	pi := peer.AddrInfo{
		ID:    *miner.PeerId,
		Addrs: multiaddrs,
	}
	return &pi, nil
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
		nd.tx = nd.exch.Tx(ctx, exchange.WithCodec(args.Codec))
	}

	fstat, err := os.Stat(args.Path)
	if err != nil {
		sendErr(err)
		return
	}

	fnd, err := files.NewSerialFile(args.Path, false, fstat)
	if err != nil {
		sendErr(err)
		return
	}

	added := make(map[string]bool)
	err = nd.addRecursive(ctx, args.Path, fnd, added)
	if err != nil {
		sendErr(err)
		return
	}

	entries, err := nd.tx.Status()
	if err != nil {
		sendErr(err)
		return
	}

	var totalSize int64
	// only notify about entries added by this operation
	for k := range added {
		e := entries[k]
		totalSize += e.Size
		nd.send(Notify{
			PutResult: &PutResult{
				Key:  k,
				Cid:  e.Value.String(),
				Size: filecoin.SizeStr(filecoin.NewInt(uint64(e.Size))),
				// NumBlocks: stats.NumBlocks, TODO: should Entry contain the number of blocks?
				RootCid:   nd.tx.Root().String(),
				TotalSize: filecoin.SizeStr(filecoin.NewInt(uint64(totalSize))),
				Len:       len(added),
			}})
	}
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
	sendErr(ErrNoTx)
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
	if nd.tx == nil {
		nd.txmu.Unlock()
		sendErr(ErrNoTx)
		return
	}
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
	if err := nd.exch.Index().SetRef(ref); err != nil {
		sendErr(err)
		return
	}

	nd.tx.Close()
	nd.tx = nil
	nd.txmu.Unlock()

	// Run the garbage collector to remove tagged Refs
	err = nd.exch.Index().GC()
	if err != nil {
		sendErr(err)
		return
	}

	nd.send(Notify{CommResult: &CommResult{
		Size: filecoin.SizeStr(filecoin.NewInt(uint64(ref.PayloadSize))),
		Ref:  ref.PayloadCID.String(),
	}})
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

	if args.MaxPPB == -1 {
		// if maxppb is set at -1, force MaxPPB at 0
		args.MaxPPB = 0
	} else if args.MaxPPB == 0 {
		// if maxppb is set at 0, use default node's value
		args.MaxPPB = nd.opts.MaxPPB
	}

	// Check if we're trying to get from an ongoing transaction
	nd.txmu.Lock()
	if nd.tx != nil && nd.tx.Root() == root {
		if args.Out != "" {
			f, err := nd.tx.GetFile(segs[0])
			if err != nil {
				sendErr(err)
				return
			}
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

	// Check our supply if we may already have it from a different tx
	tx := nd.exch.Tx(ctx, exchange.WithRoot(root))
	local := tx.IsLocal(args.Key)
	if !local {
		// The content is not available locally so we must load it
		results, err := nd.Load(ctx, args)
		if err != nil {
			sendErr(err)
			return
		}
		for res := range results {
			log.Info().Str("status", res.Status).Msg("transfer progress")
			if args.Verbose || res.Status != "" {
				nd.send(Notify{
					GetResult: &res,
				})
			}
		}
	} else {
		nd.send(Notify{
			GetResult: &GetResult{
				Local: local,
			},
		})
	}

	if args.Out != "" {
		f, err := tx.GetFile(args.Key)
		if err != nil {
			sendErr(err)
			return
		}
		err = files.WriteTo(f, args.Out)
		if err != nil {
			sendErr(err)
			return
		}
	}
}

// Load is an RPC method that retrieves a given CID and key to the local blockstore.
// It sends feedback events to a result channel that it returns.
func (nd *node) Load(ctx context.Context, args *GetArgs) (chan GetResult, error) {
	results := make(chan GetResult)

	sendErr := func(err error) {
		select {
		case results <- GetResult{
			Err: err.Error(),
		}:
		default:
		}
	}

	go func() {
		defer close(results)

		p := path.FromString(args.Cid)
		root, segs, err := path.SplitAbsPath(p)
		if err != nil {
			sendErr(err)
			return
		}

		if len(segs) > 0 {
			args.Key = segs[0]
		}

		// default to SelectFirst
		strategy := exchange.SelectFirst
		if args.Strategy != "" {
			switch args.Strategy {
			case "SelectFirst":
				strategy = exchange.SelectFirst
			case "SelectCheapest":
				strategy = exchange.SelectCheapest(5, 4*time.Second)
			case "SelectFirstLowerThan":
				strategy = exchange.SelectFirstLowerThan(abi.NewTokenAmount(args.MaxPPB))
			default:
				sendErr(errors.New("unknown strategy"))
			}
		}
		if args.Strategy == "" && args.MaxPPB > 0 {
			log.Info().Int64("maxppb", args.MaxPPB).Msg("using SelectFirstLowerThan strategy")
			strategy = exchange.SelectFirstLowerThan(abi.NewTokenAmount(args.MaxPPB))
		}

		unsub := nd.exch.Retrieval().Client().SubscribeToEvents(
			func(event client.Event, state deal.ClientState) {
				select {
				case results <- GetResult{
					TotalFunds:    filecoin.FIL(state.TotalFunds).Short(),
					TotalSpent:    filecoin.FIL(state.FundsSpent).Short(),
					Status:        deal.Statuses[state.Status],
					TotalReceived: int64(state.TotalReceived),
				}:
				default:
				}
			},
		)
		defer unsub()

		log.Info().Str("key", args.Key).Msg("starting query")

		start := time.Now()

		tx := nd.exch.Tx(ctx, exchange.WithRoot(root), exchange.WithStrategy(strategy), exchange.WithTriage())
		defer tx.Close()

		err = tx.Query(args.Key)
		if err != nil {
			sendErr(err)
			return
		}
		// We can query a specific miner on top of gossip
		// that offer will be at the top of the list if we receive it
		if args.Miner != "" {
			miner, err := address.NewFromString(args.Miner)
			if err != nil {
				sendErr(err)
			}
			info, err := nd.filMinerInfo(ctx, miner)
			if err != nil {
				sendErr(err)
			}
			offer, err := tx.QueryOffer(*info, sel.All())
			if err != nil {
				// We shouldn't fail here, the transfer could still work with other peers
				log.Error().Err(err).Str("id", info.ID.String()).Msg("querying from peer")
			} else {
				tx.ApplyOffer(offer)
			}
		}

		log.Info().Msg("waiting for triage")

		// The selection comes back for ALL the content
		selection, err := tx.Triage()
		if err != nil {
			sendErr(err)
			return
		}
		offer := selection.Offer
		funds := nd.exch.Deals().GetFundsForCid(root)
		// if no funds have been loaded we will be loading funds for the whole dag
		if funds.IsZero() {
			funds = offer.RetrievalPrice()
		}

		ainf, _ := offer.AddrInfo()

		log.Info().Str("peer", ainf.ID.String()).Msg("selected an offer")

		results <- GetResult{
			Size:         int64(offer.Size),
			Status:       "DealStatusSelectedOffer",
			TotalFunds:   filecoin.FIL(funds).String(),
			UnsealPrice:  filecoin.FIL(offer.UnsealPrice).Short(),
			PricePerByte: filecoin.FIL(offer.MinPricePerByte).Short(),
		}

		selection.Exec()

		now := time.Now()
		discDuration := now.Sub(start)

		var dref exchange.DealRef
		for {
			select {
			case dref = <-tx.Ongoing():
				results <- GetResult{
					Status: "NewDeal",
					DealID: dref.ID.String(),
				}
				log.Info().Uint64("id", uint64(dref.ID)).Msg("started new deal")
			case res := <-tx.Done():
				log.Info().Str("spent", filecoin.FIL(res.Spent).String()).Msg("finished transfer")
				if res.Err != nil {
					log.Error().Err(res.Err).Msg("transfer failed")
					sendErr(res.Err)
					return
				}

				ref := tx.Ref()
				err = nd.exch.Index().SetRef(ref)
				if err == exchange.ErrRefAlreadyExists {
					if err := nd.exch.Index().UpdateRef(ref); err != nil {
						log.Error().Err(err).Msg("updating ref")
					}
				} else if err != nil {
					sendErr(err)
					return
				}

				end := time.Now()
				transDuration := end.Sub(start) - discDuration

				select {
				case results <- GetResult{
					Status:          "Completed",
					DiscLatSeconds:  discDuration.Seconds(),
					TransLatSeconds: transDuration.Seconds(),
				}:
				case <-ctx.Done():
					sendErr(ctx.Err())
					return
				}

				if res.PayCh != address.Undef {
					err := tx.Close()
					if err != nil {
						log.Error().Err(err).Msg("closing tx")
					}
					mk, err := utils.MapMissingKeys(root, storeutil.LinkSystemForBlockstore(nd.bs))
					if err != nil {
						log.Error().Err(err).Msg("getting missing keys")
					}
					funds := nd.exch.Deals().GetFundsForCid(root)
					// If we fetched all the keys we can remove the offer
					// TODO: when blocks are properly deduplicated we can check if paych
					// available funds are 0.
					if len(mk) == 0 || funds.IsZero() {
						err := nd.exch.Deals().RemoveOffer(root)
						if err != nil {
							log.Error().Err(err).Msg("removing offer")
						}
					}
				}
				return
			case <-ctx.Done():
				log.Error().Msg("load timed out")
				sendErr(ctx.Err())
				return
			}
		}

	}()

	return results, nil
}

// DealInfoArgs gives some params to find the deal
type DealInfoArgs struct {
	CID cid.Cid
}

// DealInfoResult describes any ongoing deal
type DealInfoResult struct {
	Funds abi.TokenAmount
	PayCh address.Address
}

// DealInfo returns info about any ongoing deals for a given CID
func (nd *node) DealInfo(ctx context.Context, args *DealInfoArgs) (DealInfoResult, error) {
	return DealInfoResult{}, nil
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

// DAGParams is a set of features used to chunk and format files into DAGs
type DAGParams struct {
	chunker chunk.Splitter
	layout  func(*helpers.DagBuilderHelper) (ipldformat.Node, error)
}

// NewDAGParams initializes default params for generating a DAG
func NewDAGParams(buf io.ReadSeeker) *DAGParams {
	return &DAGParams{
		chunker: DefaultChunker(buf),
		layout:  DefaultLayout,
	}
}

// DAGOption is a functional option overriding default DAG params
type DAGOption func(*DAGParams)

// WithChunker overrides the default chunker with a given chunker interface
func WithChunker(chunker chunk.Splitter) DAGOption {
	return func(d *DAGParams) {
		d.chunker = chunker
	}
}

// WithLayout overrides the default dag layout with a layout function defining the overall layout of a DAG
func WithLayout(layout func(*helpers.DagBuilderHelper) (ipldformat.Node, error)) func(n *DAGParams) {
	return func(d *DAGParams) {
		d.layout = layout
	}
}

// Add a buffer into the given DAG. These DAGs can eventually be put into transactions.
func (nd *node) Add(ctx context.Context, dag ipldformat.DAGService, buf io.ReadSeeker, opts ...DAGOption) (cid.Cid, error) {
	bufferedDS := ipldformat.NewBufferedDAG(ctx, dag)
	dagParams := NewDAGParams(buf)
	for _, o := range opts {
		o(dagParams)
	}

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

	db, err := params.New(dagParams.chunker)
	if err != nil {
		return cid.Undef, err
	}

	n, err := dagParams.layout(db)
	if err != nil {
		return cid.Undef, err
	}

	err = bufferedDS.Commit()
	if err != nil {
		return cid.Undef, err
	}

	return n.Cid(), nil
}

// selectDAGParams returns the best chunk params according to the file's type
func selectDAGParams(filepath string, buf io.ReadSeeker) []DAGOption {
	fileType := utils.DetectFileType(filepath, buf)
	chunker := DefaultChunker(buf)
	layout := DefaultLayout

	switch fileType {
	case utils.FTAudio, utils.FTVideo:
		chunkSize := int64(units.MiB)
		chunker = chunk.NewSizeSplitter(buf, chunkSize)
		layout = trickle.Layout

	case utils.FTImage, utils.FTArchive:
		chunkSize := int64(units.MiB)
		chunker = chunk.NewSizeSplitter(buf, chunkSize)
		layout = balanced.Layout

	case utils.FTText, utils.FTFont:
		chunker = chunk.NewBuzhash(buf)
		layout = balanced.Layout
	}

	return []DAGOption{
		WithChunker(chunker),
		WithLayout(layout),
	}
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

	return nil, ErrNoTx
}

// addRecursive adds entire file trees into a single transaction
// it assumes the caller is holding the tx lock until it returns
// it currently flattens the keys though we may want to maintain the full keys to keep the structure
func (nd *node) addRecursive(ctx context.Context, name string, file files.Node, added map[string]bool) error {
	switch f := file.(type) {
	case files.Directory:
		it := f.Entries()
		for it.Next() {
			err := nd.addRecursive(ctx, it.Name(), it.Node(), added)
			if err != nil {
				return err
			}
		}
		return it.Err()
	case files.File:
		dagOptions := selectDAGParams(name, f)
		froot, err := nd.Add(ctx, nd.tx.Store().DAG, f, dagOptions...)
		if err != nil {
			return err
		}

		size, err := file.Size()
		if err != nil {
			return err
		}

		key := exchange.KeyFromPath(name)
		err = nd.tx.Put(key, froot, size)
		if err != nil {
			return err
		}
		added[key] = true
		return nil
	default:
		return errors.New("unknown file type")
	}
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
