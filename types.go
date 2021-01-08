package hop

import (
	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/ipfs/go-cid"
)

//go:generate cbor-gen-for Provision QueryResponse Query QueryParams Ask

// Provision is a message describing new content a provider can decide to store or not
type Provision struct {
	PayloadCID cid.Cid
	Size       uint64
}

// QueryParams - indicate what specific information about a piece that a retrieval
// client is interested in, as well as specific parameters the client is seeking
// for the retrieval deal
type QueryParams struct {
	MaxPricePerByte            abi.TokenAmount // optional, tell miner uninterested if more expensive than this
	MinPaymentInterval         uint64          // optional, tell miner uninterested unless payment interval is greater than this
	MinPaymentIntervalIncrease uint64          // optional, tell miner uninterested unless payment interval increase is greater than this
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

// QueryResponse is a miners response to a given retrieval query
type QueryResponse struct {
	Status                     QueryResponseStatus
	Size                       uint64          // Total size of piece in bytes
	PaymentAddress             address.Address // address to send funds to -- may be different than miner addr
	MinPricePerByte            abi.TokenAmount
	MaxPaymentInterval         uint64
	MaxPaymentIntervalIncrease uint64
	Message                    string
}

// Ask is the provider condition for accepting a deal
type Ask struct {
	PricePerByte            abi.TokenAmount
	PaymentInterval         uint64
	PaymentIntervalIncrease uint64
}
