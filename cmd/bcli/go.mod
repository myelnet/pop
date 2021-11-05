module github.com/myelnet/pop/benchmarks

go 1.16

require (
	github.com/chromedp/cdproto v0.0.0-20211025030258-2570df970243
	github.com/chromedp/chromedp v0.7.4
	github.com/filecoin-project/go-address v0.0.5
	github.com/ipld/go-ipld-prime v0.12.0
	github.com/jpillora/backoff v1.0.0
	github.com/libp2p/go-libp2p-core v0.8.5
	github.com/multiformats/go-multiaddr v0.3.1
	github.com/myelnet/pop v0.1.0
	github.com/peterbourgon/ff/v3 v3.0.0
	github.com/rs/zerolog v1.20.0
)

replace github.com/myelnet/pop => ../