package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"

	"github.com/myelnet/pop/node"
)

// Asset defines an asset reference to be serialized
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Offer references the ability for the node to serve any part of the DAG referenced by the root.
type Offer struct {
	Root           string `json:"root"`
	Selector       string `json:"selector"`
	PeerAddr       string `json:"peerAddr"`
	Size           int64  `json:"size"`
	PricePerByte   int    `json:"pricePerByte"`
	PaymentAddress string `json:"paymentAddress"`
	PaymentChannel string `json:"paymentChannel"`
}

type Content struct {
	Roots []Offer `json:"roots"`
	Keys  []Asset `json:"keys"`
}

func run() error {
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", false))

	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	// create a temp repo
	path, err := os.MkdirTemp("", ".pop")
	if err != nil {
		return err
	}
	nd, err := node.New(ctx, node.Options{
		RepoPath:       path,
		BootstrapPeers: []string{},
		FilEndpoint:    "https://infura.myel.cloud",
		Regions:        []string{"Global"},
		CancelFunc:     cancel,
	})
	if err != nil {
		return err
	}

	prs := make(chan node.PutResult, 16)
	nd.SetNotify(func(n node.Notify) {
		if pr := n.PutResult; pr != nil {
			prs <- *pr
		}
	})
	go nd.Put(ctx, &node.PutArgs{Path: "./content", Codec: 0x71})

	var keys []string
	i := 1
	for {
		res := <-prs
		if res.Err != "" {
			return fmt.Errorf(res.Err)
		}
		keys = append(keys, res.Key)
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
		return fmt.Errorf(cr.Err)
	}

	root := cr.Ref

	fmt.Println("==> committed with root", root)

	addrs, err := peer.AddrInfoToP2pAddrs(host.InfoFromHost(nd.Host))
	if err != nil {
		return err
	}

	wres := make(chan node.WalletResult, 1)
	nd.SetNotify(func(n node.Notify) {
		if wr := n.WalletResult; wr != nil {
			wres <- *wr
		}
	})
	nd.WalletList(ctx, nil)
	wl := <-wres

	var roots []Offer
	roots = append(roots, Offer{
		Root:           root,
		Selector:       "/",
		PeerAddr:       addrs[0].String(),
		Size:           cr.Size,
		PaymentAddress: wl.DefaultAddress,
	})
	var assets []Asset
	for _, k := range keys {
		assets = append(assets, Asset{
			Name: k,
			URL:  "/" + root + "/" + k,
		})
	}

	content := Content{
		Roots: roots,
		Keys:  assets,
	}

	buf := new(bytes.Buffer)
	e := json.NewEncoder(buf)
	e.SetIndent("", "    ")
	if err := e.Encode(content); err != nil {
		return err
	}
	c, err := os.Create("./static/content.json")
	if err != nil {
		return err
	}
	_, err = c.Write(buf.Bytes())
	if err != nil {
		return err
	}
	if err := c.Close(); err != nil {
		return err
	}

	ts := httptest.NewServer(http.FileServer(http.Dir("./static")))

	done := make(chan struct{})

	sent := make(chan *network.EventRequestWillBeSent)
	success := make(chan *network.Response)
	failure := make(chan *network.EventLoadingFailed)
	chromedp.ListenTarget(ctx, func(ev interface{}) {
		switch ev := ev.(type) {
		case *network.EventRequestWillBeSent:
			sent <- ev
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
			}
		}
	})
	go func() {
		i := 0
		for {
			select {
			case res := <-success:
				fmt.Println("Response:", res.URL, i)
				fmt.Println("status", res.Status)
				fmt.Println("requestTime", res.Timing.RequestTime)
				fmt.Println("receiveHeadersEnd", res.Timing.ReceiveHeadersEnd)
			case err := <-failure:
				fmt.Println("Failure:", err.ErrorText, err.RequestID)
			}
			i++
			if i == len(assets) {
				close(done)
				return
			}
		}
	}()
	go func() {
		for req := range sent {
			fmt.Println("Request:", req.Request.URL, req.RequestID)
		}
	}()

	if err := chromedp.Run(ctx, chromedp.Navigate(ts.URL)); err != nil {
		return err
	}

	<-done

	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
