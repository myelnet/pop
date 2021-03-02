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

//go:generate cbor-gen-for AddRequest

// AddRequestProtocol labels our network for announcing new content to the network
const AddRequestProtocol = "/myel/hop/supply/add/1.0"

func protoRegions(proto string, regions []Region) []protocol.ID {
	var pls []protocol.ID
	for _, r := range regions {
		pls = append(pls, protocol.ID(fmt.Sprintf("%s/%s", proto, r.Name)))
	}
	return pls
}

// AddRequest describes the new content to anounce
type AddRequest struct {
	PayloadCID cid.Cid
	Size       uint64
}

// Type defines AddRequest as a datatransfer voucher for pulling the data from the request
func (a *AddRequest) Type() datatransfer.TypeIdentifier {
	return "AddRequestVoucher"
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
		protocols: protoRegions(AddRequestProtocol, regions),
	}
	return sn
}

// NewAddRequestStream to send AddRequest messages to
func (sn *Network) NewAddRequestStream(dest peer.ID) (AddRequestStreamer, error) {
	s, err := sn.host.NewStream(context.Background(), dest, sn.protocols...)
	if err != nil {
		return nil, err
	}
	buffered := bufio.NewReaderSize(s, 16)
	return &addRequestStream{p: dest, rw: s, buffered: buffered}, nil
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
	ns := &addRequestStream{remotePID, s, buffered}
	sn.receiver.HandleAddRequest(ns)
}

// StreamReceiver will read the stream and do something in response
type StreamReceiver interface {
	HandleAddRequest(AddRequestStreamer)
}

// AddRequestStreamer reads AddRequest structs from a muxed stream
type AddRequestStreamer interface {
	ReadAddRequest() (AddRequest, error)
	WriteAddRequest(AddRequest) error
	OtherPeer() peer.ID
	Close() error
}

type addRequestStream struct {
	p        peer.ID
	rw       mux.MuxedStream
	buffered *bufio.Reader
}

func (a *addRequestStream) ReadAddRequest() (AddRequest, error) {
	var m AddRequest
	if err := m.UnmarshalCBOR(a.buffered); err != nil {
		return AddRequest{}, err
	}
	return m, nil
}

func (a *addRequestStream) WriteAddRequest(m AddRequest) error {
	return cborutil.WriteCborRPC(a.rw, &m)
}

func (a *addRequestStream) Close() error {
	return a.rw.Close()
}

func (a *addRequestStream) OtherPeer() peer.ID {
	return a.p
}
