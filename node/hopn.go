package node

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/filecoin-project/go-address"
	"github.com/ipfs/go-blockservice"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	badgerds "github.com/ipfs/go-ds-badger"
	"github.com/ipfs/go-graphsync"
	gsimpl "github.com/ipfs/go-graphsync/impl"
	gsnet "github.com/ipfs/go-graphsync/network"
	"github.com/ipfs/go-graphsync/storeutil"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	chunk "github.com/ipfs/go-ipfs-chunker"
	offline "github.com/ipfs/go-ipfs-exchange-offline"
	files "github.com/ipfs/go-ipfs-files"
	keystore "github.com/ipfs/go-ipfs-keystore"
	ipldformat "github.com/ipfs/go-ipld-format"
	"github.com/ipfs/go-merkledag"
	unixfile "github.com/ipfs/go-unixfs/file"
	"github.com/ipfs/go-unixfs/importer/balanced"
	"github.com/ipfs/go-unixfs/importer/helpers"
	"github.com/ipld/go-ipld-prime"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
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
	"github.com/myelnet/go-hop-exchange"
	"github.com/myelnet/go-hop-exchange/filecoin"
	"github.com/myelnet/go-hop-exchange/retrieval/client"
	"github.com/myelnet/go-hop-exchange/retrieval/deal"
	"github.com/myelnet/go-hop-exchange/supply"
	"github.com/myelnet/go-hop-exchange/wallet"
	"github.com/rs/zerolog/log"
)

const DefaultHashFunction = uint64(mh.BLAKE2B_MIN + 31)
const unixfsLinksPerLevel = 1024
const KLibp2pHost = "libp2p-host"

// IPFSNode is the IPFS API
type IPFSNode interface {
	Ping(context.Context, string)
	Add(context.Context, *AddArgs)
	Get(context.Context, *GetArgs)
}

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

type node struct {
	host host.Host
	ds   datastore.Batching
	bs   blockstore.Blockstore
	dag  ipldformat.DAGService
	gs   graphsync.GraphExchange
	ps   *pubsub.PubSub
	exch *hop.Exchange

	mu     sync.Mutex
	notify func(Notify)
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

	settings := hop.Settings{
		Datastore:  nd.ds,
		Blockstore: nd.bs,
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

	nd.exch, err = hop.NewExchange(ctx, settings)
	if err != nil {
		return nil, err
	}
	if opts.PrivKey != "" {
		nd.importAddress(opts.PrivKey)
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
	sendErr(fmt.Errorf("must be a valid id address or peer id"))
}

func (nd *node) ping(ctx context.Context, pi peer.AddrInfo) error {
	strs := make([]string, 0, len(pi.Addrs))
	for _, a := range pi.Addrs {
		strs = append(strs, a.String())
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
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

// Add a file to the IPFS unixfs dag
func (nd *node) Add(ctx context.Context, args *AddArgs) {

	sendErr := func(err error) {
		nd.send(Notify{
			AddResult: &AddResult{
				Err: err.Error(),
			},
		})
	}

	link, err := nd.loadFilesToBlockstore(ctx, args)
	root := link.(cidlink.Link).Cid
	if err != nil {
		sendErr(err)
		return
	}
	stats, err := hop.DAGStat(ctx, nd.bs, root, hop.AllSelector())
	if err != nil {
		log.Error().Err(err).Msg("DAGStat")
	}
	nd.send(Notify{
		AddResult: &AddResult{
			Cid:       root.String(),
			Size:      filecoin.SizeStr(filecoin.NewInt(uint64(stats.Size))),
			NumBlocks: stats.NumBlocks,
		}})

	if args.Dispatch {
		// TODO: adjust timeout?
		ctx, cancel := context.WithTimeout(ctx, 1*time.Hour)
		defer cancel()

		res, err := nd.exch.Dispatch(root)
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
				AddResult: &AddResult{
					Cache: rec.Provider.String(),
				},
			})
			if err != nil {
				sendErr(ctx.Err())
			}
			return
		}
	}
}

func (nd *node) loadFilesToBlockstore(ctx context.Context, args *AddArgs) (ipld.Link, error) {

	st, err := os.Stat(args.Path)
	if err != nil {
		return nil, fmt.Errorf("unable to open file: %w", err)
	}
	file, err := files.NewSerialFile(args.Path, false, st)
	if err != nil {
		return nil, fmt.Errorf("NewSerialFile: %w", err)
	}

	switch f := file.(type) {
	case files.Directory:
		return nd.addDir(ctx, args.Path, f)
	case files.File:
		return nd.addFile(ctx, f, args)
	default:
		return nil, fmt.Errorf("unknown file type")
	}
}

func (nd *node) addFile(ctx context.Context, file files.File, args *AddArgs) (ipld.Link, error) {

	bufferedDS := ipldformat.NewBufferedDAG(ctx, nd.dag)

	prefix, err := merkledag.PrefixForCidVersion(1)
	if err != nil {
		return nil, fmt.Errorf("unable to create cid prefix: %w", err)
	}
	prefix.MhType = DefaultHashFunction

	params := helpers.DagBuilderParams{
		Maxlinks:   unixfsLinksPerLevel,
		RawLeaves:  true,
		CidBuilder: prefix,
		Dagserv:    bufferedDS,
	}

	db, err := params.New(chunk.NewSizeSplitter(file, int64(args.ChunkSize)))
	if err != nil {
		return nil, fmt.Errorf("unable to init chunker: %w", err)
	}

	n, err := balanced.Layout(db)
	if err != nil {
		return nil, fmt.Errorf("unable to chunk file: %w", err)
	}

	err = bufferedDS.Commit()
	if err != nil {
		return nil, fmt.Errorf("unable to commit blocks to store: %w", err)
	}
	return cidlink.Link{Cid: n.Cid()}, nil
}

func (nd *node) addDir(ctx context.Context, path string, dir files.Directory) (ipld.Link, error) {
	return nil, fmt.Errorf("TODO")
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
	root, err := cid.Parse(args.Cid)
	if err != nil {
		sendErr(err)
		return
	}
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

	for {
		select {
		case err := <-session.Done():
			if err != nil {
				return err
			}
			end := time.Now()
			transDuration := end.Sub(start) - discDuration
			if args.Out != "" {
				n, err := nd.dag.Get(ctx, c)
				if err != nil {
					return err
				}
				file, err := unixfile.NewUnixfsFile(ctx, nd.dag, n)
				if err != nil {
					return err
				}
				err = files.WriteTo(file, args.Out)
				if err != nil {
					return err
				}
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
		log.Info().Str("address", addr.String()).Msg("imported private key")
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
