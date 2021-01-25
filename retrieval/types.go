package retrieval

import (
	"fmt"

	"github.com/filecoin-project/go-address"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-fil-markets/piecestore"
	"github.com/filecoin-project/go-multistore"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/specs-actors/v3/actors/builtin/paych"
	"github.com/ipfs/go-cid"
	peer "github.com/libp2p/go-libp2p-peer"
	cbg "github.com/whyrusleeping/cbor-gen"
)

// DealID is an identifier for a retrieval deal (unique to a client)
type DealID uint64

func (d DealID) String() string {
	return fmt.Sprintf("%d", d)
}

// DealProposal is a proposal for a new retrieval deal
type DealProposal struct {
	PayloadCID cid.Cid
	ID         DealID
	Params
}

// Type method makes DealProposal usable as a voucher
func (dp *DealProposal) Type() datatransfer.TypeIdentifier {
	return "RetrievalDealProposal/1"
}

// DealProposalUndefined is an undefined deal proposal
var DealProposalUndefined = DealProposal{}

// Params are the parameters requested for a retrieval deal proposal
type Params struct {
	Selector                *cbg.Deferred // V1
	PieceCID                *cid.Cid
	PricePerByte            abi.TokenAmount
	PaymentInterval         uint64 // when to request payment
	PaymentIntervalIncrease uint64
	UnsealPrice             abi.TokenAmount
}

// ClientDealState is the current state of a deal from the point of view
// of a retrieval client
type ClientDealState struct {
	DealProposal
	StoreID              *multistore.StoreID
	ChannelID            datatransfer.ChannelID
	LastPaymentRequested bool
	AllBlocksReceived    bool
	TotalFunds           abi.TokenAmount
	ClientWallet         address.Address
	MinerWallet          address.Address
	PaymentInfo          *PaymentInfo
	Status               DealStatus
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

// ProviderDealState is the current state of a deal from the point of view
// of a retrieval provider
type ProviderDealState struct {
	DealProposal
	StoreID         multistore.StoreID
	ChannelID       datatransfer.ChannelID
	PieceInfo       *piecestore.PieceInfo
	Status          DealStatus
	Receiver        peer.ID
	TotalSent       uint64
	FundsReceived   abi.TokenAmount
	Message         string
	CurrentInterval uint64
	LegacyProtocol  bool
}

// PaymentInfo is the payment channel and lane for a deal, once it is setup
type PaymentInfo struct {
	PayCh address.Address
	Lane  uint64
}

// DealPayment is a payment for an in progress retrieval deal
type DealPayment struct {
	ID             DealID
	PaymentChannel address.Address
	PaymentVoucher *paych.SignedVoucher
}
