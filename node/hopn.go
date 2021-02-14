package node

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/ipfs/go-datastore"
	badgerds "github.com/ipfs/go-ds-badger"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/host"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/rs/zerolog/log"
)

// IPFSNode is the IPFS API
type IPFSNode interface {
	Ping(ip string)
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

func (n *node) Ping(param string) {
	n.send(Notify{PingResult: &PingResult{
		ListenAddr: ma.Join(n.host.Addrs()...).String(),
	}})
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
		return fmt.Errorf("safesocket.Listen: %v", err)
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

	ds, err := badgerds.NewDatastore(opts.RepoPath, &dsopts)
	if err != nil {
		return err
	}
	nd.ds = ds

	h, err := libp2p.New(ctx)
	if err != nil {
		return err
	}
	nd.host = h

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
