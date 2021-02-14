package node

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sync"

	"github.com/ipfs/go-blockservice"
	"github.com/ipfs/go-datastore"
	badgerds "github.com/ipfs/go-ds-badger"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	chunk "github.com/ipfs/go-ipfs-chunker"
	offline "github.com/ipfs/go-ipfs-exchange-offline"
	files "github.com/ipfs/go-ipfs-files"
	ipldformat "github.com/ipfs/go-ipld-format"
	"github.com/ipfs/go-merkledag"
	"github.com/ipfs/go-unixfs/importer/balanced"
	"github.com/ipfs/go-unixfs/importer/helpers"
	"github.com/ipld/go-ipld-prime"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/host"
	ma "github.com/multiformats/go-multiaddr"
	mh "github.com/multiformats/go-multihash"
	"github.com/rs/zerolog/log"
)

const DefaultHashFunction = uint64(mh.BLAKE2B_MIN + 31)
const unixfsChunkSize uint64 = 1 << 10
const unixfsLinksPerLevel = 1024

// IPFSNode is the IPFS API
type IPFSNode interface {
	Ping(ip string)
	Add(context.Context, *AddArgs)
}

// Options determines configurations for the IPFS node
type Options struct {
	// RepoPath is the file system path to use to persist our datastore
	RepoPath string
	// SocketPath is the unix socket path to listen on
	SocketPath string
}

type node struct {
	host host.Host
	ds   datastore.Batching
	bs   blockstore.Blockstore
	dag  ipldformat.DAGService

	mu     sync.Mutex
	notify func(Notify)
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
	nd.send(Notify{PingResult: &PingResult{
		ListenAddr: ma.Join(nd.host.Addrs()...).String(),
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

type server struct {
	node *node

	csMu sync.Mutex // lock order: bsMu, then mu
	cs   *CommandServer

	mu      sync.Mutex
	clients map[net.Conn]bool
}

func (s *server) serveConn(ctx context.Context, c net.Conn) {
	br := bufio.NewReader(c)

	s.addConn(c)
	defer s.removeAndCloseConn(c)

	for ctx.Err() == nil {
		msg, err := ReadMsg(br)
		if err != nil {
			log.Error().Err(err).Msg("ReadMsg")
			return
		}
		s.csMu.Lock()
		if err := s.cs.GotMsgBytes(ctx, msg); err != nil {
			log.Error().Err(err).Msg("GotMsgBytes")
		}
		s.csMu.Unlock()

	}
}

func (s *server) addConn(c net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.clients == nil {
		s.clients = map[net.Conn]bool{}
	}

	s.clients[c] = true
}

func (s *server) removeAndCloseConn(c net.Conn) {
	s.mu.Lock()
	delete(s.clients, c)
	s.mu.Unlock()
	c.Close()
}

func (s *server) writeToClients(b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		WriteMsg(c, b)
	}
}

// Run runs a hop enabled IPFS node
func Run(ctx context.Context, opts Options) error {
	done := make(chan struct{})
	defer close(done)

	// listen, err := socket.Listen(socketPath, port)
	listen, err := SocketListen(opts.SocketPath)
	if err != nil {
		return fmt.Errorf("SocketListen: %v", err)
	}

	go func() {
		select {
		case <-ctx.Done():
		case <-done:
		}
		listen.Close()
	}()

	nd := &node{}

	dsopts := badgerds.DefaultOptions
	dsopts.SyncWrites = false
	dsopts.Truncate = true

	nd.ds, err = badgerds.NewDatastore(opts.RepoPath, &dsopts)
	if err != nil {
		return err
	}

	nd.bs = blockstore.NewBlockstore(nd.ds)

	nd.dag = merkledag.NewDAGService(blockservice.New(nd.bs, offline.Exchange(nd.bs)))

	nd.host, err = libp2p.New(ctx)
	if err != nil {
		return err
	}

	log.Info().Interface("addrs", nd.host.Addrs()).Msg("Node started libp2p")

	server := &server{
		node: nd,
	}

	server.cs = NewCommandServer(nd, server.writeToClients)

	nd.notify = server.cs.send

	for i := 1; ctx.Err() == nil; i++ {
		c, err := listen.Accept()

		if err != nil {
			if ctx.Err() == nil {
				log.Error().Err(err).Msg("listen.Accept")
				// backOff
			}
			continue
		}

		go server.serveConn(ctx, c)
	}

	return ctx.Err()
}
