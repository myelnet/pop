package node

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	gopath "path"
	"sync"
	"time"

	"github.com/gabriel-vasile/mimetype"
	files "github.com/ipfs/go-ipfs-files"
	ipldformat "github.com/ipfs/go-ipld-format"
	ipath "github.com/ipfs/go-path"
	"github.com/ipfs/go-path/resolver"
	unixfile "github.com/ipfs/go-unixfs/file"
	uio "github.com/ipfs/go-unixfs/io"
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
	c.SetReadDeadline(time.Now().Add(time.Second))
	peek, _ := br.Peek(4)
	c.SetReadDeadline(time.Time{})
	isHTTPReq := string(peek) == "GET "

	if isHTTPReq {
		httpServer := http.Server{
			// Localhost connections are cheap; so only do
			// keep-alives for a short period of time, as these
			// active connections lock the server into only serving
			// that user. If the user has this page open, we don't
			// want another switching user to be locked out for
			// minutes. 5 seconds is enough to let browser hit
			// favicon.ico and such.
			IdleTimeout: 5 * time.Second,
			Handler:     s.localhostHandler(),
		}
		httpServer.Serve(&oneConnListener{&protoSwitchConn{br: br, Conn: c}})
		return
	}

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

func (s *server) localhostHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			io.WriteString(w, "<html><title>Hop</title><body><h1>Hello</h1>This is the local IPFS daemon.")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Hour)
		defer cancel()
		r = r.WithContext(ctx)

		switch r.Method {
		case http.MethodGet, http.MethodHead:
			s.getHandler(w, r)
			return
		case http.MethodOptions:
			s.optionsHandler(w, r)
			return
		}

		errmsg := "Method " + r.Method + " not allowed: "
		status := http.StatusBadRequest
		errmsg = errmsg + "bad request for " + r.URL.Path
		http.Error(w, errmsg, status)
	})
}

func (s *server) optionsHandler(w http.ResponseWriter, r *http.Request) {
	s.addUserHeaders(w)
}

func (s *server) addUserHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", http.MethodGet)
}

func (s *server) getHandler(w http.ResponseWriter, r *http.Request) {
	urlPath := r.URL.Path

	parsedPath := ipath.FromString(urlPath)
	if err := parsedPath.IsValid(); err != nil {
		http.Error(w, "invalid ipfs path", http.StatusBadRequest)
		return
	}

	re := &resolver.Resolver{
		DAG:         s.node.dag,
		ResolveOnce: uio.ResolveUnixfsOnce,
	}

	// This doesn't actually hit the dag service but resolves a path into a cid
	ci, _, err := re.ResolveToLastNode(r.Context(), parsedPath)
	if err != nil {
		http.Error(w, "ipfs resolve -r "+urlPath, http.StatusNotFound)
		return
	}
	// Check if we have the blocks locally
	nd, err := s.node.dag.Get(r.Context(), ci)
	if err != nil {
		if err == ipldformat.ErrNotFound {
			// try to retrieve the blocks
			err = s.node.get(r.Context(), ci, &GetArgs{})
			if err != nil {
				// TODO: give better feedback into what went wrong
				http.Error(w, "Failed to retrieve content", http.StatusInternalServerError)
				return
			}

			// If all went well we should have the blocks now
			nd, err = s.node.dag.Get(r.Context(), ci)
			if err != nil {
				http.Error(w, "Unable to find content", http.StatusNotFound)
				return
			}

		} else {
			http.Error(w, "Unable to find content", http.StatusNotFound)
			return
		}

	}
	fnd, err := unixfile.NewUnixfsFile(r.Context(), s.node.dag, nd)
	if err != nil {
		http.Error(w, "Unable to create unix files", http.StatusInternalServerError)
		return
	}

	s.addUserHeaders(w)

	modtime := time.Now()
	if f, ok := fnd.(files.File); ok {
		name := gopath.Base(urlPath)

		size, err := f.Size()
		if err != nil {
			http.Error(w, "cannot serve files with unknown sizes", http.StatusBadGateway)
			return
		}
		content := &lazySeeker{
			size:   size,
			reader: f,
		}

		mimeType, err := mimetype.DetectReader(content)
		if err != nil {
			http.Error(w, fmt.Sprintf("cannot detect content-type: %s", err.Error()), http.StatusInternalServerError)
			return
		}

		ctype := mimeType.String()
		_, err = content.Seek(0, io.SeekStart)
		if err != nil {
			http.Error(w, "seeker can't seek", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", ctype)
		http.ServeContent(w, r, name, modtime, content)
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

	log.Info().
		Strs("regions", opts.Regions).
		Bool("isFilecoinRPCOnline", nd.exch.IsFilecoinOnline()).
		Msg("node is running")

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

type dummyAddr string

// wraps a connection into a listener
type oneConnListener struct {
	conn net.Conn
}

func (l *oneConnListener) Accept() (c net.Conn, err error) {
	c = l.conn
	if c == nil {
		err = io.EOF
		return
	}
	err = nil
	l.conn = nil
	return
}

func (l *oneConnListener) Close() error { return nil }

func (l *oneConnListener) Addr() net.Addr { return dummyAddr("unused-address") }

func (a dummyAddr) Network() string { return string(a) }
func (a dummyAddr) String() string  { return string(a) }

// protoSwitchConn is a net.Conn that lets us Read from its bufio.Reader
type protoSwitchConn struct {
	net.Conn
	br *bufio.Reader
}

func (psc *protoSwitchConn) Read(p []byte) (int, error) { return psc.br.Read(p) }
