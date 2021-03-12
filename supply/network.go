package supply

import (
	"bufio"
	"context"
	"fmt"

	cborutil "github.com/filecoin-project/go-cbor-util"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/mux"
	"github.com/libp2p/go-libp2p-core/network"
	peer "github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
)

//go:generate cbor-gen-for Request

// RequestProtocol labels our network for announcing new content to the network
const RequestProtocol = "/myel/pop/supply/dispatch/1.0"

func protoRegions(proto string, regions []Region) []protocol.ID {
	var pls []protocol.ID
	for _, r := range regions {
		pls = append(pls, protocol.ID(fmt.Sprintf("%s/%s", proto, r.Name)))
	}
	return pls
}

// Request describes the content to cache providers who may or may not accept to retrieve it
type Request struct {
	PayloadCID cid.Cid
	Size       uint64
}

// Type defines AddRequest as a datatransfer voucher for pulling the data from the request
func (Request) Type() datatransfer.TypeIdentifier {
	return "DispatchRequestVoucher"
}

// Response is an async collection of confirmations from data transfers to cache providers
type Response struct {
	recordChan chan PRecord
	unsub      datatransfer.Unsubscribe
}

// Next returns the next record from a new cache
func (r *Response) Next(ctx context.Context) (PRecord, error) {
	select {
	case r := <-r.recordChan:
		return r, nil
	case <-ctx.Done():
		return PRecord{}, ctx.Err()
	}
}

// Close stops listening for cache confirmations
func (r *Response) Close() {
	r.unsub()
	close(r.recordChan)
}

// Network handles all the different messaging protocols
// related to content supply
type Network struct {
	host      host.Host
	receiver  StreamReceiver
	protocols []protocol.ID
}

// NewNetwork creates a new Network instance
func NewNetwork(h host.Host, regions []Region) *Network {
	sn := &Network{
		host:      h,
		protocols: protoRegions(RequestProtocol, regions),
	}
	return sn
}

// NewRequestStream to send AddRequest messages to
func (sn *Network) NewRequestStream(dest peer.ID) (RequestStreamer, error) {
	s, err := sn.host.NewStream(context.Background(), dest, sn.protocols...)
	if err != nil {
		return nil, err
	}
	buffered := bufio.NewReaderSize(s, 16)
	return &requestStream{p: dest, rw: s, buffered: buffered}, nil
}

// SetDelegate assigns a handler for all the protocols
func (sn *Network) SetDelegate(sr StreamReceiver) {
	sn.receiver = sr
	for _, proto := range sn.protocols {
		sn.host.SetStreamHandler(proto, sn.handleStream)
	}
}

func (sn *Network) handleStream(s network.Stream) {
	if sn.receiver == nil {
		fmt.Printf("no receiver set")
		s.Reset()
		return
	}
	remotePID := s.Conn().RemotePeer()
	buffered := bufio.NewReaderSize(s, 16)
	ns := &requestStream{remotePID, s, buffered}
	sn.receiver.HandleRequest(ns)
}

// StreamReceiver will read the stream and do something in response
type StreamReceiver interface {
	HandleRequest(RequestStreamer)
}

// RequestStreamer reads AddRequest structs from a muxed stream
type RequestStreamer interface {
	ReadRequest() (Request, error)
	WriteRequest(Request) error
	OtherPeer() peer.ID
	Close() error
}

type requestStream struct {
	p        peer.ID
	rw       mux.MuxedStream
	buffered *bufio.Reader
}

func (a *requestStream) ReadRequest() (Request, error) {
	var m Request
	if err := m.UnmarshalCBOR(a.buffered); err != nil {
		return Request{}, err
	}
	return m, nil
}

func (a *requestStream) WriteRequest(m Request) error {
	return cborutil.WriteCborRPC(a.rw, &m)
}

func (a *requestStream) Close() error {
	return a.rw.Close()
}

func (a *requestStream) OtherPeer() peer.ID {
	return a.p
}
