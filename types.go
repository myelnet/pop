package hop

import (
	"github.com/filecoin-project/go-address"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/ipfs/go-cid"
)

//go:generate cbor-gen-for --map-encoding Provision QueryResponse Query QueryParams Ask StorageDataTransferVoucher

// Provision is a message describing new content a provider can decide to store or not
type Provision struct {
	PayloadCID cid.Cid
	Size       uint64
}

// QueryParams - indicate what specific information about a piece that a retrieval
// client is interested in, as well as specific parameters the client is seeking
// for the retrieval deal
type QueryParams struct {
	PieceCID *cid.Cid // optional, query if miner has this cid in this piece. some miners may not be able to respond.
	// MaxPricePerByte            abi.TokenAmount // optional, tell miner uninterested if more expensive than this
	// MinPaymentInterval         uint64          // optional, tell miner uninterested unless payment interval is greater than this
	// MinPaymentIntervalIncrease uint64          // optional, tell miner uninterested unless payment interval increase is greater than this
}

// Query is a query to a given provider to determine information about a piece
// they may have available for retrieval
// If we don't have a specific provider in mind we can use gossip Hop to find one
type Query struct {
	PayloadCID cid.Cid
	QueryParams
}

// QueryResponseStatus indicates whether a queried piece is available
type QueryResponseStatus uint64

const (
	// QueryResponseAvailable indicates a provider has a piece and is prepared to
	// return it
	QueryResponseAvailable QueryResponseStatus = iota

	// QueryResponseUnavailable indicates a provider either does not have or cannot
	// serve the queried piece to the client
	QueryResponseUnavailable

	// QueryResponseError indicates something went wrong generating a query response
	QueryResponseError
)

// QueryItemStatus indicates whether the requested part of a piece (payload or selector)
// is available for retrieval
type QueryItemStatus uint64

const (
	// QueryItemAvailable indicates requested part of the piece is available to be
	// served
	QueryItemAvailable QueryItemStatus = iota

	// QueryItemUnavailable indicates the piece either does not contain the requested
	// item or it cannot be served
	QueryItemUnavailable

	// QueryItemUnknown indicates the provider cannot determine if the given item
	// is part of the requested piece (for example, if the piece is sealed and the
	// miner does not maintain a payload CID index)
	QueryItemUnknown
)

// QueryResponse is a miners response to a given retrieval query
type QueryResponse struct {
	Status                     QueryResponseStatus
	PieceCIDFound              QueryItemStatus // if a PieceCID was requested, the result
	Size                       uint64          // Total size of piece in bytes
	PaymentAddress             address.Address // address to send funds to -- may be different than miner addr
	MinPricePerByte            abi.TokenAmount
	MaxPaymentInterval         uint64
	MaxPaymentIntervalIncrease uint64
	Message                    string
	UnsealPrice                abi.TokenAmount
}

// PieceRetrievalPrice is the total price to retrieve the piece (size * MinPricePerByte + UnsealedPrice)
func (qr QueryResponse) PieceRetrievalPrice() abi.TokenAmount {
	return big.Add(big.Mul(qr.MinPricePerByte, abi.NewTokenAmount(int64(qr.Size))), qr.UnsealPrice)
}

// Ask is the provider condition for accepting a deal
type Ask struct {
	PricePerByte            abi.TokenAmount
	PaymentInterval         uint64
	PaymentIntervalIncrease uint64
}

// StorageDataTransferVoucher is a voucher for requesting a node store your content
type StorageDataTransferVoucher struct {
	Proposal cid.Cid
}

// Type satisfies the transfer voucher interface
func (dv *StorageDataTransferVoucher) Type() datatransfer.TypeIdentifier {
	return "StorageDataTransferVoucher"
}
