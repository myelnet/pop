package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/filecoin-project/go-address"
	"github.com/ipld/go-ipld-prime/codec/dagcbor"
	"github.com/ipld/go-ipld-prime/datamodel"
	"github.com/ipld/go-ipld-prime/fluent/qp"
	"github.com/ipld/go-ipld-prime/node/basicnode"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/myelnet/pop/node"
)

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
	var sizes []int64
	i := 1
	for {
		res := <-prs
		if res.Err != "" {
			return fmt.Errorf(res.Err)
		}
		keys = append(keys, res.Key)
		sizes = append(sizes, res.Size)
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
		return err
	}

	r := node.RRecord{
		PeerAddr: peerAddr.Bytes(),
		PayAddr:  payAddr,
		Size:     cr.Size,
	}

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

	rf, err := os.Create("./static/" + root)
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

	ts := httptest.NewServer(http.FileServer(http.Dir("./static")))
	defer ts.Close()

	done := make(chan struct{})

	ready := make(chan struct{})

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
				if string(arg.Value) == "activated" {
					ready <- struct{}{}
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
				// close(done)
				return
			}
			if i == len(keys) {
				close(done)
				return
			}
		}
	}()

	if err := chromedp.Run(ctx, chromedp.Navigate(ts.URL)); err != nil {
		return err
	}

	// <-ready

	// for _, k := range keys {
	// 	url := "/" + root + "/" + k
	// }

	<-done

	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
