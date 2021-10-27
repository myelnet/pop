package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
)

func main() {
	ctx, cancel := chromedp.NewContext(context.Background())
	defer cancel()

	ts := httptest.NewServer(http.FileServer(http.Dir("./static")))

	var nodes []*cdp.Node
	if err := chromedp.Run(ctx,
		chromedp.Navigate(ts.URL),
		chromedp.Nodes(`document`, &nodes, chromedp.ByJSPath),
	); err != nil {
		log.Fatal(err)
	}

	fmt.Println("Document tree:")
	fmt.Print(nodes[0].Dump("  ", "  ", false))
}
