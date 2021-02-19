package node

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	datatransfer "github.com/filecoin-project/go-data-transfer"
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
	ipldformat "github.com/ipfs/go-ipld-format"
	"github.com/ipfs/go-merkledag"
	unixfile "github.com/ipfs/go-unixfs/file"
	"github.com/ipfs/go-unixfs/importer/balanced"
	"github.com/ipfs/go-unixfs/importer/helpers"
	"github.com/ipld/go-ipld-prime"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	"github.com/libp2p/go-libp2p"
	connmgr "github.com/libp2p/go-libp2p-connmgr"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/routing"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	peerstore "github.com/libp2p/go-libp2p-peerstore"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/p2p/net/conngater"
	ma "github.com/multiformats/go-multiaddr"
	mh "github.com/multiformats/go-multihash"
	"github.com/myelnet/go-hop-exchange"
	"github.com/myelnet/go-hop-exchange/wallet"
	"github.com/rs/zerolog/log"
)

const DefaultHashFunction = uint64(mh.BLAKE2B_MIN + 31)
const unixfsChunkSize uint64 = 1 << 10
const unixfsLinksPerLevel = 1024

// IPFSNode is the IPFS API
type IPFSNode interface {
	Ping(ip string)
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

	nd.ds, err = badgerds.NewDatastore(opts.RepoPath, &dsopts)
	if err != nil {
		return nil, err
	}

	nd.bs = blockstore.NewBlockstore(nd.ds)

	nd.dag = merkledag.NewDAGService(blockservice.New(nd.bs, offline.Exchange(nd.bs)))

	gater, err := conngater.NewBasicConnectionGater(nd.ds)
	if err != nil {
		return nil, err
	}

	nd.host, err = libp2p.New(
		ctx,
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

	nd.exch, err = hop.NewExchange(ctx,
		hop.WithBlockstore(nd.bs),
		hop.WithPubSub(nd.ps),
		hop.WithHost(nd.host),
		hop.WithDatastore(nd.ds),
		hop.WithGraphSync(nd.gs),
		hop.WithRepoPath(opts.RepoPath),
		// TODO: secure keystore
		hop.WithKeystore(wallet.NewMemKeystore()),
	)
	if err != nil {
		return nil, err
	}
	go nd.bootstrap(ctx, opts.BootstrapPeers)

	return nd, nil

}

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
func (nd *node) Ping(param string) {
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
		ListenAddrs: addrs,
		Peers:       pstr,
	}})
}

// Add a file to the IPFS unixfs dag
func (nd *node) Add(ctx context.Context, args *AddArgs) {

	link, err := nd.loadFileToBlockstore(ctx, args.Path)
	root := link.(cidlink.Link).Cid
	if err != nil {
		nd.send(Notify{
			AddResult: &AddResult{
				Err: err.Error(),
			},
		})
		return
	}
	nd.send(Notify{
		AddResult: &AddResult{
			Cid: root.String(),
		}})
}

func (nd *node) loadFileToBlockstore(ctx context.Context, path string) (ipld.Link, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("unable to open file: %v", err)
	}

	var buf bytes.Buffer
	tr := io.TeeReader(f, &buf)
	file := files.NewReaderFile(tr)

	// import to UnixFS
	bufferedDS := ipldformat.NewBufferedDAG(ctx, nd.dag)

	prefix, err := merkledag.PrefixForCidVersion(1)
	if err != nil {
		return nil, fmt.Errorf("unable to create cid prefix: %v", err)
	}
	prefix.MhType = DefaultHashFunction

	params := helpers.DagBuilderParams{
		Maxlinks:   unixfsLinksPerLevel,
		RawLeaves:  true,
		CidBuilder: prefix,
		Dagserv:    bufferedDS,
	}

	db, err := params.New(chunk.NewSizeSplitter(file, int64(unixfsChunkSize)))
	if err != nil {
		return nil, fmt.Errorf("unable to init chunker: %v", err)
	}

	n, err := balanced.Layout(db)
	if err != nil {
		return nil, fmt.Errorf("unable to chunk file: %v", err)
	}

	err = bufferedDS.Commit()
	if err != nil {
		return nil, fmt.Errorf("unable to commit blocks to store: %v", err)
	}

	return cidlink.Link{Cid: n.Cid()}, nil
}

func (nd *node) Get(ctx context.Context, args *GetArgs) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(args.Timeout)*time.Minute)
	defer cancel()
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
		unsub := nd.exch.DataTransfer().SubscribeToEvents(
			func(event datatransfer.Event, state datatransfer.ChannelState) {
				log.Info().
					Str("event", datatransfer.Events[event.Code]).
					Str("status", datatransfer.Statuses[state.Status()]).
					Msg("Retrieving")
			},
		)
		defer unsub()
	}
	err = nd.get(ctx, root, args)
	if err != nil {
		sendErr(err)
	}
}

func (nd *node) get(ctx context.Context, c cid.Cid, args *GetArgs) error {
	// TODO handle different predefined selectors
	session, err := nd.exch.Session(ctx, c)
	if err != nil {
		return err
	}
	err = session.SyncBlocks(ctx)
	if err != nil {
		return err
	}
	for {
		select {
		case deal := <-session.StartedTransfer():
			nd.send(Notify{
				GetResult: &GetResult{
					DealID: deal.String(),
				},
			})
			continue
		case err := <-session.Done():
			if err != nil {
				return err
			}
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
				GetResult: &GetResult{},
			})
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

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

func (nd *node) connPeers() []peer.ID {
	conns := nd.host.Network().Conns()
	var out []peer.ID
	for _, c := range conns {
		pid := c.RemotePeer()
		out = append(out, pid)
	}
	return out
}