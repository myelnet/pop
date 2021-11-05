package main

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/filecoin-project/go-address"
	"github.com/ipfs/go-path"
	"github.com/ipld/go-ipld-prime/codec/dagcbor"
	"github.com/ipld/go-ipld-prime/datamodel"
	"github.com/ipld/go-ipld-prime/fluent/qp"
	"github.com/ipld/go-ipld-prime/node/basicnode"
	"github.com/jpillora/backoff"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/peterbourgon/ff/v3"
	"github.com/peterbourgon/ff/v3/ffcli"

	"github.com/myelnet/pop/node"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

//go:embed static/*
var staticFiles embed.FS

// BrowserClient is a controller for a client node in a headless chrome browser
type BrowserClient struct {
	url        string
	mu         sync.Mutex
	notify     func(node.Notify)
	cancelFunc context.CancelFunc
}

func (c *BrowserClient) Get(ctx context.Context, args *node.GetArgs) {
	sendErr := func(err error) {
		c.send(node.Notify{
			GetResult: &node.GetResult{
				Err: err.Error(),
			}})
	}
	p := path.FromString(args.Cid)
	// /<cid>/path/file.ext => cid, ["path", file.ext"]
	root, _, err := path.SplitAbsPath(p)
	if err != nil {
		sendErr(err)
		return
	}

	addr, err := ma.NewMultiaddr(args.Peer)
	if err != nil {
		sendErr(err)
		return
	}

	payAddr, err := address.NewFromString(args.ProviderAddr)
	if err != nil {
		sendErr(err)
		return
	}

	err = writeStaticRecord(root.String(), node.RRecord{
		PeerAddr: addr.Bytes(),
		PayAddr:  payAddr,
		Size:     args.Size,
	})
	if err != nil {
		sendErr(err)
		return
	}

	resp, err := chromedp.RunResponse(ctx, chromedp.Navigate(c.url+"/"+args.Cid))
	if err != nil {
		sendErr(err)
		return
	}
	fmt.Println("Status", resp.Status)
	c.send(node.Notify{GetResult: &node.GetResult{}})
}

// send hits out notify callback if we attached one
func (c *BrowserClient) send(n node.Notify) {
	c.mu.Lock()
	notify := c.notify
	c.mu.Unlock()

	if notify != nil {
		notify(n)
	} else {
		log.Info().Interface("notif", n).Msg("nil notify callback; dropping")
	}
}

type CommandServer struct {
	n             *BrowserClient
	sendNotifyMsg func(jsonMsg []byte)
}

func NewCommandServer(node *BrowserClient, sendNotifyMsg func(b []byte)) *CommandServer {
	return &CommandServer{
		n:             node,
		sendNotifyMsg: sendNotifyMsg,
	}
}

func (cs *CommandServer) GotMsgBytes(ctx context.Context, b []byte) error {
	cmd := &Command{}
	if len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, cmd); err != nil {
		return err
	}
	return cs.GotMsg(ctx, cmd)
}
func (cs *CommandServer) GotMsg(ctx context.Context, cmd *Command) error {
	if c := cmd.Get; c != nil {
		// Get requests can be quite long and we don't want to block other commands
		go cs.n.Get(ctx, c)
		return nil
	}
	return fmt.Errorf("CommandServer: unknown command")
}

func (cs *CommandServer) send(n node.Notify) {
	b, err := json.Marshal(n)
	if err != nil {
		log.Fatal().Err(err).Interface("n", n).Msg("Failed json.Marshal(notify)")
	}
	if bytes.Contains(b, node.JsonEscapedZero) {
		log.Error().Msg("[unexpected] zero byte in CommandServer.send notify message")
	}
	cs.sendNotifyMsg(b)
}

type server struct {
	node *BrowserClient

	csMu sync.Mutex
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
		msg, err := node.ReadMsg(br)
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
		node.WriteMsg(c, b)
	}
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

// writeStaticRecord to disk static directory encoded as CBOR
func writeStaticRecord(key string, r node.RRecord) error {
	rbuf := new(bytes.Buffer)
	if err := r.MarshalCBOR(rbuf); err != nil {
		return err
	}

	n, err := qp.BuildList(basicnode.Prototype.Any, 1, func(la datamodel.ListAssembler) {
		qp.ListEntry(la, qp.Bytes(rbuf.Bytes()))
	})
	if err != nil {
		return err
	}

	lbuf := new(bytes.Buffer)
	if err := dagcbor.Encode(n, lbuf); err != nil {
		return err
	}

	rf, err := os.Create("./static/" + key)
	if err != nil {
		return err
	}
	_, err = rf.Write(lbuf.Bytes())
	if err != nil {
		return err
	}
	if err := rf.Close(); err != nil {
		return err
	}

	return nil
}

type Content struct {
	Root  string
	Keys  []string
	Sizes []int64
	Offer node.RRecord
}

func runNode(ctx context.Context, cancel context.CancelFunc, contentDir string) (co Content, err error) {
	// create a temp repo
	path, err := os.MkdirTemp("", ".pop")
	if err != nil {
		return co, err
	}
	nd, err := node.New(ctx, node.Options{
		RepoPath:       path,
		BootstrapPeers: []string{},
		FilEndpoint:    "https://infura.myel.cloud",
		Regions:        []string{"Global"},
		CancelFunc:     cancel,
	})
	if err != nil {
		return co, err
	}

	prs := make(chan node.PutResult, 16)
	nd.SetNotify(func(n node.Notify) {
		if pr := n.PutResult; pr != nil {
			prs <- *pr
		}
	})
	go nd.Put(ctx, &node.PutArgs{Path: e2eArgs.contentDir, Codec: 0x71})

	i := 1
	for {
		res := <-prs
		if res.Err != "" {
			return co, fmt.Errorf(res.Err)
		}
		co.Keys = append(co.Keys, res.Key)
		co.Sizes = append(co.Sizes, res.Size)
		if i == res.Len {
			break
		}
		i++
	}

	fmt.Printf("==> put %d keys\n", i)

	refs := make(chan node.CommResult, 1)
	nd.SetNotify(func(n node.Notify) {
		if cr := n.CommResult; cr != nil {
			refs <- *cr
		}
	})
	nd.Commit(ctx, &node.CommArgs{CacheRF: 0})
	cr := <-refs

	if cr.Err != "" {
		return co, fmt.Errorf(cr.Err)
	}

	root := cr.Ref
	co.Root = root

	fmt.Println("==> committed with root", root)

	addrs, err := peer.AddrInfoToP2pAddrs(host.InfoFromHost(nd.Host))
	if err != nil {
		return co, err
	}

	wres := make(chan node.WalletResult, 1)
	nd.SetNotify(func(n node.Notify) {
		if wr := n.WalletResult; wr != nil {
			wres <- *wr
		}
	})
	nd.WalletList(ctx, nil)
	wl := <-wres

	var peerAddr ma.Multiaddr
	for _, maddr := range addrs {
		str := maddr.String()
		if strings.Contains(str, "ws") && strings.Contains(str, "127.0.0.1") {
			peerAddr = maddr
			break
		}
	}
	fmt.Println("peer address", peerAddr)

	payAddr, err := address.NewFromString(wl.DefaultAddress)
	if err != nil {
		return co, err
	}

	r := node.RRecord{
		PeerAddr: peerAddr.Bytes(),
		PayAddr:  payAddr,
		Size:     cr.Size,
	}
	co.Offer = r

	return co, nil
}

// Command is a message sent from a client to the daemon
type Command struct {
	Get *node.GetArgs
}

var startArgs struct {
	headless    bool
	provider    bool
	privKeyPath string
	contentDir  string
}

var startCmd = &ffcli.Command{
	Name:      "start",
	ShortHelp: "Starts a headless chrome browser client",
	Exec:      runStart,
	FlagSet: (func() *flag.FlagSet {
		fs := flag.NewFlagSet("start", flag.ExitOnError)
		fs.BoolVar(&startArgs.headless, "headless", true, "run chrome as headless (without GUI)")
		fs.BoolVar(&startArgs.provider, "provider", false, "start a provider node to retrieve from")
		fs.StringVar(&startArgs.privKeyPath, "privkey", "", "path to private key to use by default")
		fs.StringVar(&startArgs.contentDir, "content", "./content", "path to some content to add to the provider")
		return fs
	})(),
}

func runStart(parent context.Context, args []string) error {
	l, err := net.Listen("tcp", "0.0.0.0:2002")
	if err != nil {
		return err
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", startArgs.headless))

	allocCtx, cancel := chromedp.NewExecAllocator(parent, opts...)
	defer cancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	if startArgs.provider {
		content, err := runNode(ctx, cancel, startArgs.contentDir)
		if err != nil {
			return err
		}
		for _, k := range content.Keys {
			fmt.Println("Added", k)
		}
	}

	staticFS := http.FS(staticFiles)
	ts := httptest.NewServer(http.FileServer(staticFS))
	defer ts.Close()

	ready := make(chan struct{}, 1)

	chromedp.ListenTarget(ctx, func(ev interface{}) {
		switch ev := ev.(type) {
		case *runtime.EventConsoleAPICalled:
			fmt.Printf("* console.%s call:\n", ev.Type)
			for _, arg := range ev.Args {
				fmt.Printf("%s - %s\n", arg.Type, arg.Value)

				if fmt.Sprintf("%s", arg.Value) == "\"activated\"" {
					ready <- struct{}{}
				}
			}
		}
	})

	fmt.Println("test server running at", ts.URL)

	if err := chromedp.Run(ctx, chromedp.Navigate(ts.URL+"/static")); err != nil {
		return err
	}
	// wait for service worker to be ready
	<-ready

	nd := &BrowserClient{
		url:        ts.URL,
		cancelFunc: cancel,
	}

	server := &server{
		node: nd,
	}
	server.cs = NewCommandServer(nd, server.writeToClients)

	nd.notify = server.cs.send

	go serveTCP(ctx, server, l)

	<-ctx.Done()
	return ctx.Err()
}

var getArgs struct {
	selector string
	timeout  int
	peer     string
	maxppb   int64
	paddr    string
	size     int64
}

var getCmd = &ffcli.Command{
	Name:       "get",
	ShortUsage: "get <cid>",
	ShortHelp:  "Retrieve content from the network",
	LongHelp: strings.TrimSpace(`
The 'bcli get' command retrieves blocks with a given root cid and an optional selector
(defaults retrieves all the linked blocks).
`),
	Exec: runGet,
	FlagSet: (func() *flag.FlagSet {
		fs := flag.NewFlagSet("get", flag.ExitOnError)
		fs.StringVar(&getArgs.selector, "selector", "all", "select blocks to retrieve for a root cid")
		fs.IntVar(&getArgs.timeout, "timeout", 60, "timeout before the request should be cancelled by the node (in minutes)")
		fs.StringVar(&getArgs.peer, "peer", "", "target a specific peer when retrieving the content")
		fs.Int64Var(&getArgs.maxppb, "maxppb", 0, "max price per byte (0=\"default node's value\", -1=\"free retrieval\")")
		fs.StringVar(&getArgs.paddr, "provider-addr", "", "provider filecoin address")
		fs.Int64Var(&getArgs.size, "size", 0, "expected size of the content if known")
		return fs
	})(),
}

func runGet(ctx context.Context, args []string) error {
	c, cc, ctx, cancel := connect(ctx)
	defer cancel()

	grc := make(chan *node.GetResult)
	cc.SetNotifyCallback(func(n node.Notify) {
		if gr := n.GetResult; gr != nil {
			grc <- gr
		}
	})
	go receive(ctx, cc, c)

	if getArgs.peer == "" {
		return fmt.Errorf("a valid peer address is required")
	}

	cc.Get(&node.GetArgs{
		Cid:          args[0],
		Timeout:      getArgs.timeout,
		Sel:          getArgs.selector,
		Peer:         getArgs.peer,
		MaxPPB:       getArgs.maxppb,
		ProviderAddr: getArgs.paddr,
		Size:         getArgs.size,
	})

	for {
		select {
		case gr := <-grc:
			if gr.Err != "" {
				return errors.New(gr.Err)
			}
			fmt.Println("==> Transfer completed")
			return nil
		case <-ctx.Done():
			return fmt.Errorf("Get operation timed out")
		}
	}
}

var e2eArgs struct {
	contentDir string
	headless   bool
}

var e2eCmd = &ffcli.Command{
	Name:      "e2e",
	ShortHelp: "Run an end to end suite of retrievals",
	Exec:      runE2E,
	FlagSet: (func() *flag.FlagSet {
		fs := flag.NewFlagSet("e2e", flag.ExitOnError)
		fs.StringVar(&e2eArgs.contentDir, "content", "./content", "path to some content to add to the provider")
		fs.BoolVar(&e2eArgs.headless, "headless", true, "run chrome as headless (without GUI)")
		return fs
	})(),
}

func runE2E(parent context.Context, args []string) error {
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", e2eArgs.headless))

	allocCtx, cancel := chromedp.NewExecAllocator(parent, opts...)
	defer cancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	content, err := runNode(ctx, cancel, e2eArgs.contentDir)
	if err != nil {
		return err
	}

	if err := writeStaticRecord(content.Root, content.Offer); err != nil {
		return err
	}

	ts := httptest.NewServer(http.FileServer(http.Dir("./static")))
	defer ts.Close()

	ready := make(chan struct{})
	done := make(chan struct{})

	success := make(chan *network.Response)
	failure := make(chan *network.EventLoadingFailed)
	request := make(chan *network.EventRequestWillBeSent)
	chromedp.ListenTarget(ctx, func(ev interface{}) {
		switch ev := ev.(type) {
		case *network.EventRequestWillBeSent:
			request <- ev
		case *network.EventResponseReceived:
			if ev.Response != nil {
				success <- ev.Response
			}
		case *network.EventLoadingFailed:
			failure <- ev
		case *runtime.EventConsoleAPICalled:
			fmt.Printf("* console.%s call:\n", ev.Type)
			for _, arg := range ev.Args {
				fmt.Printf("%s - %s\n", arg.Type, arg.Value)

				if fmt.Sprintf("%s", arg.Value) == "\"activated\"" {
					close(ready)
				}
			}
		}
	})
	go func() {
		i := 0
		total := 0.0

		attempted := make(map[string]*network.Request)

		for {
			select {
			case req := <-request:
				fmt.Println("Request:", req.Request.URL, req.RequestID)
				attempted[req.Request.URL] = req.Request
			case res := <-success:
				fmt.Println("Response:", res.URL, i)
				fmt.Println("status", res.Status)
				fmt.Println("size", res.EncodedDataLength)
				fmt.Println("receiveHeadersEnd", res.Timing.ReceiveHeadersEnd)
				speed := res.EncodedDataLength / res.Timing.ReceiveHeadersEnd
				total += speed

				delete(attempted, res.URL)

				i++

			case err := <-failure:
				fmt.Println("==> Failure:", err.ErrorText, err.RequestID)
				i++
			case <-time.After(10 * time.Second):
				for k := range attempted {
					fmt.Println("Timeout", k)
				}
				close(done)
				return
			}
		}
	}()

	if err := chromedp.Run(ctx, chromedp.Navigate(ts.URL)); err != nil {
		return err
	}

	<-ready

	for _, k := range content.Keys {
		exp := fmt.Sprintf(`
    var imgSection = document.querySelector('section');
    var myImage = document.createElement('img');
    var myFigure = document.createElement('figure');
    var myCaption = document.createElement('caption');

    myImage.src = '%s';
    myImage.setAttribute('alt', '%s');
    myCaption.innerHTML = '<strong>' + '%s' + '</strong>';

    imgSection.appendChild(myFigure);
    myFigure.appendChild(myImage);
    myFigure.appendChild(myCaption);
`, "/"+content.Root+"/"+k, k, k)
		err := chromedp.Run(ctx, chromedp.Evaluate(exp, nil))
		if err != nil {
			return err
		}
	}

	<-done

	return nil
}

func connect(ctx context.Context) (net.Conn, *node.CommandClient, context.Context, context.CancelFunc) {
	c, err := net.Dial("tcp", "127.0.0.1:2002")
	if err != nil {
		log.Fatal().Msg("Unable to connect")
	}

	clientToServer := func(b []byte) {
		node.WriteMsg(c, b)
	}

	ctx, cancel := context.WithCancel(ctx)

	go func() {
		interrupt := make(chan os.Signal, 1)
		signal.Notify(interrupt, syscall.SIGINT, syscall.SIGTERM)
		<-interrupt
		c.Close()
		cancel()
	}()

	cc := node.NewCommandClient(clientToServer)
	return c, cc, ctx, cancel
}

// receive backend messages on conn and push them into cc.
func receive(ctx context.Context, cc *node.CommandClient, conn net.Conn) {
	defer conn.Close()
	for ctx.Err() == nil {
		msg, err := node.ReadMsg(conn)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Error().Err(err).Msg("ReadMsg")
			break
		}
		cc.GotNotifyMsg(msg)
	}
}
func run(args []string) error {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	rootfs := flag.NewFlagSet("bcli", flag.ExitOnError)

	// env vars can be used as program args, i.e : ENV LOG=debug go run . start
	err := ff.Parse(rootfs, args, ff.WithEnvVarNoPrefix())
	if err != nil {
		return err
	}

	rootCmd := &ffcli.Command{
		Name:       "bcli",
		ShortUsage: "bcli subcommand [flags]",
		ShortHelp:  "Manage a headless chrome client from the command line",
		LongHelp: strings.TrimSpace(`
This CLI is for development only. To get started run 'bcli start'.
`),
		Subcommands: []*ffcli.Command{
			startCmd,
			getCmd,
			e2eCmd,
		},
		FlagSet: rootfs,
		Exec:    func(context.Context, []string) error { return flag.ErrHelp },
	}

	if err := rootCmd.Parse(args); err != nil {
		return err
	}

	err = rootCmd.Run(context.Background())
	if err == flag.ErrHelp {
		return nil
	}
	return err
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
