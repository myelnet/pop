package node

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	gopath "path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"runtime/debug"

	"github.com/caddyserver/certmagic"
	"github.com/filecoin-project/go-jsonrpc"
	"github.com/gabriel-vasile/mimetype"
	"github.com/gorilla/websocket"
	"github.com/ipfs/go-cid"
	files "github.com/ipfs/go-ipfs-files"
	ipath "github.com/ipfs/go-path"
	"github.com/jpillora/backoff"
	"github.com/myelnet/pop/exchange"
	"github.com/myelnet/pop/internal/utils"
	sel "github.com/myelnet/pop/selectors"
	"github.com/rs/zerolog/log"
	"github.com/soheilhy/cmux"
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
	c.SetReadDeadline(time.Time{})

	s.addConn(c)
	defer s.removeAndCloseConn(c)

	for ctx.Err() == nil {
		msg, err := ReadMsg(br)
		if errors.Is(err, io.EOF) {
			return
		}
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
	return http.HandlerFunc(s.handler)
}

func (s *server) handler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" && r.Method == http.MethodGet {
		io.WriteString(w, "<html><title>pop</title><body><h1>Hello</h1>This is your Myel pop.")
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
	case http.MethodPost:
		s.postHandler(w, r)
		return
	}

	errmsg := "Method " + r.Method + " not allowed: "
	status := http.StatusBadRequest
	errmsg = errmsg + "bad request for " + r.URL.Path
	http.Error(w, errmsg, status)
}

func (s *server) optionsHandler(w http.ResponseWriter, r *http.Request) {
	s.addUserHeaders(w)
}

func (s *server) addUserHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header()["Access-Control-Allow-Methods"] = []string{http.MethodPost, http.MethodGet}
	w.Header()["Access-Control-Allow-Headers"] = []string{"Content-Type", "User-Agent", "Range"}
	w.Header()["Access-Control-Expose-Headers"] = []string{"IPFS-Hash"}
}

// HTTP get does not retrieve content but only serves content already cached locally or for which a loaded
// paychannel already exists to make sure content is loaded use JSON RPC method Load available via websocket
func (s *server) getHandler(w http.ResponseWriter, r *http.Request) {
	urlPath := r.URL.Path

	parsedPath := ipath.FromString(urlPath)

	// Extract the CID and file path segments
	root, segs, err := ipath.SplitAbsPath(parsedPath)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	var key string
	if len(segs) > 0 {
		key = segs[0]
	}

	s.addUserHeaders(w)

	tx := s.node.exch.Tx(r.Context(), exchange.WithRoot(root))

	has := tx.IsLocal(key)
	if !has {
		// If there is already a payment channel open we can handle it
		// else the delay for loading a payment channel is not reasonnable for an HTTP request
		_, err = s.node.exch.Deals().FindOfferByCid(root)
		if err != nil {
			http.Error(w, "content not cached on this node", http.StatusNotFound)
			return
		}
		results, err := s.node.Load(r.Context(), &GetArgs{Cid: urlPath})
		if err != nil {
			http.Error(w, "failed to load", http.StatusInternalServerError)
			return
		}
		for range results {
		}
	}

	if key == "" {
		// If there is no key we return all the entries as a JSON file detailing information
		// about each entry. This allows clients to inspec the content in a transaction before
		// fetching all of it.
		entries, err := tx.Entries()
		if err != nil {
			http.Error(w, "Failed to get entries", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
		return
	}
	fnd, err := tx.GetFile(segs[0])
	if err != nil {
		http.Error(w, "Failed to read file from store", http.StatusInternalServerError)
		return
	}

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

func (s *server) postHandler(w http.ResponseWriter, r *http.Request) {
	mediatype, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		http.Error(w, "unable to parse content type", http.StatusInternalServerError)
		return
	}

	var root cid.Cid
	if mediatype == "multipart/form-data" {
		codec := uint64(0x71)
		codecString := r.Header.Get("Codec-Type")
		if codecString != "" {
			codec, err = utils.CodecFromString(codecString)
			if err != nil {
				http.Error(w, "invalid codec name", http.StatusBadRequest)
				return
			}
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		tx := s.node.exch.Tx(r.Context(), exchange.WithCodec(codec))
		defer tx.Close()

		// Set Cache Replication-Factor
		cacheRF, err := parseContentReplication(r.Header.Get("Content-Replication"))
		if err != nil {
			http.Error(w, "failed to parse Content-Replication", http.StatusInternalServerError)
			return
		}
		if cacheRF > 12 {
			cacheRF = 12
		}
		tx.SetCacheRF(cacheRF)

		for part, err := mr.NextPart(); err == nil; part, err = mr.NextPart() {
			fileReader := files.NewReaderFile(part)
			dagOptions := selectDAGParams(part.FileName(), fileReader)

			c, err := s.node.Add(r.Context(), tx.Store().DAG, fileReader, dagOptions...)
			if err != nil {
				http.Error(w, "failed to add file", http.StatusInternalServerError)
				return
			}
			stats, err := utils.Stat(tx.Store(), c, sel.All())
			if err != nil {
				http.Error(w, "failed to get file stat", http.StatusInternalServerError)
				return
			}
			key := part.FileName()
			if key == "" {
				// If it's not a file the key should be the form name
				key = part.FormName()
			}
			err = tx.Put(key, c, int64(stats.Size))
			if err != nil {
				http.Error(w, "failed to put file in tx", http.StatusInternalServerError)
				return
			}
		}
		err = tx.Commit()
		if err != nil {
			http.Error(w, "failed to commit tx", http.StatusInternalServerError)
			return
		}
		ref := tx.Ref()
		err = s.node.exch.Index().SetRef(ref)
		if errors.Is(exchange.ErrRefAlreadyExists, err) {
			http.Error(w, "ref already exists", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "failed to set new ref", http.StatusInternalServerError)
			return
		}
		err = s.node.remind.Publish(ref.PayloadCID, ref.PayloadSize)
		if err != nil {
			http.Error(w, "failed to publish to remote index", http.StatusInternalServerError)
			return
		}
		root = tx.Root()
	} else {
		fileReader := files.NewReaderFile(r.Body)
		dagOptions := selectDAGParams("", fileReader)

		c, err := s.node.Add(r.Context(), s.node.dag, fileReader, dagOptions...)
		if err != nil {
			http.Error(w, "failed to add file to blockstore", http.StatusInternalServerError)
			return
		}
		root = c
	}

	s.addUserHeaders(w)
	w.Header().Set("IPFS-Hash", root.String())
	http.Redirect(w, r, root.String(), http.StatusCreated)
}

func parseContentReplication(contentReplication string) (int, error) {
	if contentReplication == "" {
		return 0, nil
	}

	contentReplicationInt, err := strconv.ParseInt(contentReplication, 10, 64)
	if err != nil {
		return 0, errors.New("unable to parse content replication")
	}

	return int(contentReplicationInt), nil
}

// Run runs a pop IPFS node
func Run(ctx context.Context, opts Options) error {
	l, err := net.Listen("tcp", "0.0.0.0:2001")
	if err != nil {
		return fmt.Errorf("SocketListen: %v", err)
	}

	// Create a mux to listen for TCP / HTTP requests on same port
	m := cmux.New(l)
	defer m.Close()

	httpl := m.Match(cmux.HTTP1Fast())
	tcpl := m.Match(cmux.Any())

	nd, err := New(ctx, opts)
	if err != nil {
		return fmt.Errorf("node.New: %v", err)
	}

	fmt.Printf("==> Started pop node\n")
	fmt.Printf("==> Joined %s regions\n", opts.Regions)
	if nd.exch.IsFilecoinOnline() {
		fmt.Printf("==> Connected to Filecoin RPC at %s\n", opts.FilEndpoint)
	}

	nd.metrics.Record("check-in",
		map[string]string{"peer": nd.host.ID().String()},
		map[string]interface{}{"msg": "logging-on"})

	if nd.metrics.URL() != "" {
		fmt.Printf("==> Checked-in with InfluxDB at %s\n", nd.metrics.URL())
	}

	server := &server{
		node: nd,
	}

	server.cs = NewCommandServer(nd, server.writeToClients)

	nd.notify = server.cs.send

	serveHTTP(server, httpl, nd.opts.Domains)

	go serveTCP(ctx, server, tcpl)
	go func() {
		err := m.Serve()
		if err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
			log.Error().Err(err).Msg("serve CMux")
		}
	}()

	<-ctx.Done()

	return ctx.Err()
}

// If we receive a wss request on port 80/443 we either forward to the libp2p websocket transport or directly call the http handler
func (s *server) proxyHandler() http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {

		u, err := url.Parse("ws://localhost:41505")
		if err != nil {
			log.Err(err).Msg("error when parsing ws url")
			return
		}
		u.Fragment = req.URL.Fragment
		u.Path = req.URL.Path
		u.RawQuery = req.URL.RawQuery

		dialer := websocket.DefaultDialer
		// Pass headers from the incoming request to the dialer to forward them to
		// the final destinations.
		requestHeader := http.Header{}
		if origin := req.Header.Get("Origin"); origin != "" {
			requestHeader.Add("Origin", origin)
		}
		for _, prot := range req.Header[http.CanonicalHeaderKey("Sec-WebSocket-Protocol")] {
			requestHeader.Add("Sec-WebSocket-Protocol", prot)
		}
		for _, cookie := range req.Header[http.CanonicalHeaderKey("Cookie")] {
			requestHeader.Add("Cookie", cookie)
		}
		if req.Host != "" {
			requestHeader.Set("Host", req.Host)
		}

		// Pass X-Forwarded-For headers too, code below is a part of
		// httputil.ReverseProxy. See http://en.wikipedia.org/wiki/X-Forwarded-For
		// for more information
		// TODO: use RFC7239 http://tools.ietf.org/html/rfc7239
		if clientIP, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
			// If we aren't the first proxy retain prior
			// X-Forwarded-For information as a comma+space
			// separated list and fold multiple headers into one.
			if prior, ok := req.Header["X-Forwarded-For"]; ok {
				clientIP = strings.Join(prior, ", ") + ", " + clientIP
			}
			requestHeader.Set("X-Forwarded-For", clientIP)
		}

		// Set the originating protocol of the incoming HTTP request. The SSL might
		// be terminated on our site and because we doing proxy adding this would
		// be helpful for applications on the backend.
		requestHeader.Set("X-Forwarded-Proto", "http")
		if req.TLS != nil {
			requestHeader.Set("X-Forwarded-Proto", "https")
		}

		// Connect to the backend URL, also pass the headers we get from the requst
		// together with the Forwarded headers we prepared above.
		// TODO: support multiplexing on the same backend connection instead of
		// opening a new TCP connection time for each request. This should be
		// optional:
		// http://tools.ietf.org/html/draft-ietf-hybi-websocket-multiplexing-01
		connBackend, resp, err := dialer.Dial(u.String(), requestHeader)
		if err != nil {
			log.Printf("websocketproxy: couldn't dial to remote backend url %s", err)
			if resp != nil {
				// If the WebSocket handshake fails, ErrBadHandshake is returned
				// along with a non-nil *http.Response so that callers can handle
				// redirects, authentication, etcetera.
				if err := copyResponse(rw, resp); err != nil {
					log.Error().Err(err).Msg("websocketproxy: couldn't write response after failed remote backend handshake")
				}
			} else {
				http.Error(rw, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			}
			return
		}
		defer connBackend.Close()

		upgrader := &websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
			EnableCompression: true,
		}

		// Only pass those headers to the upgrader.
		upgradeHeader := http.Header{}
		if hdr := resp.Header.Get("Sec-Websocket-Protocol"); hdr != "" {
			upgradeHeader.Set("Sec-Websocket-Protocol", hdr)
		}
		if hdr := resp.Header.Get("Set-Cookie"); hdr != "" {
			upgradeHeader.Set("Set-Cookie", hdr)
		}

		// Now upgrade the existing incoming request to a WebSocket connection.
		// Also pass the header that we gathered from the Dial handshake.
		connPub, err := upgrader.Upgrade(rw, req, upgradeHeader)
		if err != nil {
			log.Error().Err(err).Msg("websocketproxy: couldn't upgrade")
			return
		}
		defer connPub.Close()

		errClient := make(chan error, 1)
		errBackend := make(chan error, 1)
		replicateWebsocketConn := func(dst, src *websocket.Conn, errc chan error) {
			for {
				msgType, msg, err := src.ReadMessage()
				if err != nil {
					m := websocket.FormatCloseMessage(websocket.CloseNormalClosure, fmt.Sprintf("%v", err))
					if e, ok := err.(*websocket.CloseError); ok {
						if e.Code != websocket.CloseNoStatusReceived {
							m = websocket.FormatCloseMessage(e.Code, e.Text)
						}
					}
					errc <- err
					dst.WriteMessage(websocket.CloseMessage, m)
					break
				}
				err = dst.WriteMessage(msgType, msg)
				if err != nil {
					errc <- err
					break
				}
			}
		}

		go replicateWebsocketConn(connPub, connBackend, errClient)
		go replicateWebsocketConn(connBackend, connPub, errBackend)

		var message string
		select {
		case err = <-errClient:
			message = "websocketproxy: Error when copying from backend to client"
		case err = <-errBackend:
			message = "websocketproxy: Error when copying from client to backend"

		}
		if e, ok := err.(*websocket.CloseError); !ok || e.Code == websocket.CloseAbnormalClosure {
			log.Error().Err(err).Msg(message)
		}
	})
}

func serveHTTP(server *server, l net.Listener, domains []string) {
	mx := http.NewServeMux()

	// register RPC
	rpcServer := jsonrpc.NewServer()
	rpcServer.Register("pop", server.node)

	mx.Handle("/rpc", rpcServer)
	mx.Handle("/", server.localhostHandler())
	mx.Handle("/p2p/", server.proxyHandler())

	if server.node.opts.UpgradeSecret != "" {
		mx.Handle("/upgrade", upgradeHandler(server.node.opts.UpgradeSecret))
	}

	go func() {
		s := &http.Server{
			Handler: mx,
		}
		err := s.Serve(l)
		if err != nil && err != cmux.ErrServerClosed {
			log.Error().Err(err).Msg("serveHTTP")
		}
	}()

	if !server.node.opts.Certmagic {
		return
	}

	go func() {
		repoPath, err := utils.RepoPath()
		if err != nil {
			log.Err(err).Msg("error when getting repo path")
			return
		}

		certmagic.Default.Storage = &certmagic.FileStorage{Path: filepath.Join(repoPath, "certmagic")}

		err = certmagic.HTTPS(domains, mx)
		if err != nil {
			log.Err(err).Msg("error when serving https")
			return
		}
		fmt.Println("==> Started tls proxy for domains:")
		for _, d := range domains {
			fmt.Println("- ", d)
		}
	}()
}

func serveTCP(ctx context.Context, server *server, l net.Listener) {
	b := backoff.Backoff{
		Min: time.Second,
		Max: time.Second * 5,
	}

	for ctx.Err() == nil {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() == nil {
				log.Error().Err(err).Msg("listen.Accept")

				// backOff
				delay := b.Duration()
				time.Sleep(delay)
			}
			continue
		}

		// reset backoff
		b.Reset()

		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Error().Msgf("recovered from panic : [%v] - stack trace : \n [%s]", r, debug.Stack())
				}
			}()

			server.serveConn(ctx, c)
		}()
	}
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func copyResponse(rw http.ResponseWriter, resp *http.Response) error {
	copyHeader(rw.Header(), resp.Header)
	rw.WriteHeader(resp.StatusCode)
	defer resp.Body.Close()

	_, err := io.Copy(rw, resp.Body)
	return err
}
