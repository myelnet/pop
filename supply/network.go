package supply

import (
	"bufio"
	"context"
	"fmt"

	cborutil "github.com/filecoin-project/go-cbor-util"
	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/mux"
	"github.com/libp2p/go-libp2p-core/network"
	peer "github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
)

// AddRequestProtocolID labels our network for announcing new content to the network
const AddRequestProtocolID = protocol.ID("/myel/hop/supply/add/1.0")

// QueryProtocolID labels our supply network protocol
const QueryProtocolID = protocol.ID("/myel/hop/supply/query/1.0")

// AddRequest describes the new content to anounce
type AddRequest struct {
	PayloadCID cid.Cid
	Size       uint64
}

// Network handles all the different messaging protocols
// related to content supply
type Network struct {
	host      host.Host
	receiver  StreamReceiver
	protocols []protocol.ID
}

// NewNetwork creates a new Network instance
func NewNetwork(h host.Host) *Network {
	sn := &Network{
		host: h,
		protocols: []protocol.ID{
			AddRequestProtocolID,
			QueryProtocolID,
		},
	}
	return sn
}

// NewAddRequestStream to send AddRequest messages to
func (sn *Network) NewAddRequestStream(dest peer.ID) (*addRequestStream, error) {
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
	HandleAddRequest(*addRequestStream)
}

type addRequestStream struct {
	p        peer.ID
	rw       mux.MuxedStream
	buffered *bufio.Reader
}

func (a *addRequestStream) ReadAddRequest() (AddRequest, error) {
	var m AddRequest
	// if err := m.UnmarshalCBOR(a.buffered); err != nil {
	// 	return AddRequest{}, err
	// }
	return m, nil
}

func (a *addRequestStream) WriteAddRequest(m AddRequest) error {
	return cborutil.WriteCborRPC(a.rw, &m)
}

func (a *addRequestStream) Close() error {
	return a.rw.Close()
}
