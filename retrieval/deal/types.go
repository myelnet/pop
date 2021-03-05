package deal

import (
	"bytes"
	"fmt"

	"github.com/filecoin-project/go-address"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-fil-markets/piecestore"
	"github.com/filecoin-project/go-multistore"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/specs-actors/v3/actors/builtin/paych"
	"github.com/ipfs/go-cid"
	"github.com/ipld/go-ipld-prime"
	"github.com/ipld/go-ipld-prime/codec/dagcbor"
	peer "github.com/libp2p/go-libp2p-peer"
	cbg "github.com/whyrusleeping/cbor-gen"
)

//go:generate cbor-gen-for --map-encoding QueryParams Query QueryResponse Proposal Response Params Payment ClientState ProviderState PaymentInfo

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

// Offer is the conditions under which a provider is willing to approve a transfer
type Offer struct {
	PeerID   peer.ID
	Response QueryResponse
}

// ID is an identifier for a retrieval deal (unique to a client)
type ID uint64

func (d ID) String() string {
	return fmt.Sprintf("%d", d)
}

// Proposal is a proposal for a new retrieval deal
type Proposal struct {
	PayloadCID cid.Cid
	ID         ID
	Params
}

// Type method makes DealProposal usable as a voucher
func (dp *Proposal) Type() datatransfer.TypeIdentifier {
	return "RetrievalDealProposal/1"
}

// ProposalUndefined is an undefined deal proposal
var ProposalUndefined = Proposal{}

// Params are the parameters requested for a retrieval deal proposal
type Params struct {
	Selector                *cbg.Deferred // V1
	PieceCID                *cid.Cid
	PricePerByte            abi.TokenAmount
	PaymentInterval         uint64 // when to request payment
	PaymentIntervalIncrease uint64
	UnsealPrice             abi.TokenAmount
}

// SelectorSpecified returns whether we decoded any serialized selector
func (p Params) SelectorSpecified() bool {
	return p.Selector != nil && !bytes.Equal(p.Selector.Raw, cbg.CborNull)
}

// NewParams generates parameters for a retrieval deal, including a selector
func NewParams(pricePerByte abi.TokenAmount, paymentInterval uint64, paymentIntervalIncrease uint64, sel ipld.Node, pieceCid *cid.Cid, unsealPrice abi.TokenAmount) (Params, error) {
	var buffer bytes.Buffer

	if sel == nil {
		return Params{}, fmt.Errorf("selector required for NewParamsV1")
	}

	err := dagcbor.Encoder(sel, &buffer)
	if err != nil {
		return Params{}, fmt.Errorf("error encoding selector: %w", err)
	}

	return Params{
		Selector:                &cbg.Deferred{Raw: buffer.Bytes()},
		PieceCID:                pieceCid,
		PricePerByte:            pricePerByte,
		PaymentInterval:         paymentInterval,
		PaymentIntervalIncrease: paymentIntervalIncrease,
		UnsealPrice:             unsealPrice,
	}, nil
}

// Response is a response to a retrieval deal proposal
type Response struct {
	Status Status
	ID     ID

	// payment required to proceed
	PaymentOwed abi.TokenAmount

	Message string
}

// Type method makes DealResponse usable as a voucher result
func (dr *Response) Type() datatransfer.TypeIdentifier {
	return "RetrievalDealResponse/1"
}

// ResponseUndefined is an undefined deal response
var ResponseUndefined = Response{}

// ClientState is the current state of a deal from the point of view
// of a retrieval client
type ClientState struct {
	Proposal
	StoreID              *multistore.StoreID
	ChannelID            datatransfer.ChannelID
	LastPaymentRequested bool
	AllBlocksReceived    bool
	TotalFunds           abi.TokenAmount
	ClientWallet         address.Address
	MinerWallet          address.Address
	PaymentInfo          *PaymentInfo
	Status               Status
	Sender               peer.ID
	TotalReceived        uint64
	Message              string
	BytesPaidFor         uint64
	CurrentInterval      uint64
	PaymentRequested     abi.TokenAmount
	FundsSpent           abi.TokenAmount
	UnsealFundsPaid      abi.TokenAmount
	WaitMsgCID           *cid.Cid // the CID of any message the client deal is waiting for
	VoucherShortfall     abi.TokenAmount
	LegacyProtocol       bool
}

// ProviderState is the current state of a deal from the point of view
// of a retrieval provider
type ProviderState struct {
	Proposal
	StoreID         multistore.StoreID
	ChannelID       datatransfer.ChannelID
	PieceInfo       *piecestore.PieceInfo
	Status          Status
	Receiver        peer.ID
	TotalSent       uint64
	FundsReceived   abi.TokenAmount
	Message         string
	CurrentInterval uint64
	LegacyProtocol  bool
}

// Identifier provides a unique id for this provider deal
func (pds ProviderState) Identifier() ProviderDealIdentifier {
	return ProviderDealIdentifier{Receiver: pds.Receiver, DealID: pds.ID}
}

// ProviderDealIdentifier is a value that uniquely identifies a deal
type ProviderDealIdentifier struct {
	Receiver peer.ID
	DealID   ID
}

func (p ProviderDealIdentifier) String() string {
	return fmt.Sprintf("%v/%v", p.Receiver, p.DealID)
}

// PaymentInfo is the payment channel and lane for a deal, once it is setup
type PaymentInfo struct {
	PayCh address.Address
	Lane  uint64
}

// Payment is a payment for an in progress retrieval deal
type Payment struct {
	ID             ID
	PaymentChannel address.Address
	PaymentVoucher *paych.SignedVoucher
}

// Type method makes DealPayment usable as a voucher
func (dr *Payment) Type() datatransfer.TypeIdentifier {
	return "RetrievalDealPayment/1"
}

// ShortfallError is an error that indicates a short fall of funds
type ShortfallError struct {
	shortfall abi.TokenAmount
}

// NewShortfallError returns a new error indicating a shortfall of funds
func NewShortfallError(shortfall abi.TokenAmount) error {
	return ShortfallError{shortfall}
}

// Shortfall returns the numerical value of the shortfall
func (se ShortfallError) Shortfall() abi.TokenAmount {
	return se.shortfall
}
func (se ShortfallError) Error() string {
	return fmt.Sprintf("Inssufficient Funds. Shortfall: %s", se.shortfall.String())
}
