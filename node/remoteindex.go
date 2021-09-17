package node

import (
	"bytes"
	"context"
	"net/http"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/crypto"
	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/myelnet/pop/wallet"
)

//go:generate cbor-gen-for RRecord

// RRecord is a lightweight record to store as encoded bytes in a remote index
type RRecord struct {
	PeerAddr  []byte
	PayAddr   address.Address
	Size      int64
	Signature *crypto.Signature
}

// SigningBytes prepares the bytes for signing
func (r *RRecord) SigningBytes() ([]byte, error) {
	osr := *r
	osr.Signature = nil

	buf := new(bytes.Buffer)
	if err := osr.MarshalCBOR(buf); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// RemoteIndex can publish cbor encoded records that a provider is storing a thing
type RemoteIndex struct {
	url     string
	peerID  peer.ID
	client  *http.Client
	wallet  wallet.Driver
	address []byte
}

// NewRemoteIndex creates a new index instance for the given wallet and host
func NewRemoteIndex(url string, h host.Host, w wallet.Driver) (*RemoteIndex, error) {
	info := host.InfoFromHost(h)
	addrs, err := peer.AddrInfoToP2pAddrs(info)
	if err != nil {
		return nil, err
	}
	return &RemoteIndex{
		url:     url,
		peerID:  info.ID,
		client:  &http.Client{},
		wallet:  w,
		address: addrs[0].Bytes(),
	}, nil
}

// Publish a record to the remote url for a given cid
func (ri *RemoteIndex) Publish(key cid.Cid, size int64) error {
	// ignore if no url has been set
	if ri.url == "" {
		return nil
	}
	srec := &RRecord{
		PeerAddr: ri.address,
		PayAddr:  ri.wallet.DefaultAddress(),
		Size:     size,
	}
	rb, err := srec.SigningBytes()
	if err != nil {
		return err
	}
	sig, err := ri.wallet.Sign(context.TODO(), srec.PayAddr, rb)
	if err != nil {
		return err
	}
	srec.Signature = sig

	buf := new(bytes.Buffer)
	if err := srec.MarshalCBOR(buf); err != nil {
		return err
	}
	req, err := http.NewRequest("PUT", ri.url+"/"+key.String()+":"+ri.peerID.String(), buf)
	if err != nil {
		return err
	}
	if _, err := ri.client.Do(req); err != nil {
		return err
	}
	return nil
}

// Delete a record when the content has been evicted from our store
func (ri *RemoteIndex) Delete(key cid.Cid) error {
	if ri.url == "" {
		return nil
	}
	req, err := http.NewRequest("DELETE", ri.url+"/"+key.String()+":"+ri.peerID.String(), nil)
	if err != nil {
		return err
	}
	// TODO: add signature here too
	if _, err := ri.client.Do(req); err != nil {
		return err
	}
	return nil
}
