package node

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/rs/zerolog/log"
)

// server listens for connection and controls the node to execute requests
type server struct {
	node *node

	csMu sync.Mutex // lock order: csMu, then mu
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

	nd, err := New(ctx, opts)
	if err != nil {
		return fmt.Errorf("node.New: %v", err)
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
