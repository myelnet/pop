package filecoin

import (
	"context"
	"net/http"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/dline"
	"github.com/filecoin-project/go-state-types/network"
	"github.com/ipfs/go-cid"
)

// API declares minimum required methods for interacting with the Filecoin Blockchain
// We can support different filecoin implementations such as lotus or venus
type API interface {
	ChainHead(context.Context) (*TipSet, error)
	GasEstimateMessageGas(context.Context, *Message, *MessageSendSpec, TipSetKey) (*Message, error)
	StateGetActor(context.Context, address.Address, TipSetKey) (*Actor, error)
	MpoolPush(context.Context, *SignedMessage) (cid.Cid, error)
	StateWaitMsg(context.Context, cid.Cid, uint64) (*MsgLookup, error)
	StateAccountKey(context.Context, address.Address, TipSetKey) (address.Address, error)
	StateLookupID(context.Context, address.Address, TipSetKey) (address.Address, error)
	StateReadState(context.Context, address.Address, TipSetKey) (*ActorState, error)
	StateNetworkVersion(context.Context, TipSetKey) (network.Version, error)
	StateMarketBalance(context.Context, address.Address, TipSetKey) (MarketBalance, error)
	StateDealProviderCollateralBounds(context.Context, abi.PaddedPieceSize, bool, TipSetKey) (DealCollateralBounds, error)
	StateMinerInfo(context.Context, address.Address, TipSetKey) (MinerInfo, error)
	StateMinerProvingDeadline(context.Context, address.Address, TipSetKey) (*dline.Info, error)
	StateCall(context.Context, *Message, TipSetKey) (*InvocResult, error)
	ChainReadObj(context.Context, cid.Cid) ([]byte, error)
	ChainGetMessage(context.Context, cid.Cid) (*Message, error)
	Close()
}

// APIEndpoint address and auth token to access a remote api
type APIEndpoint struct {
	Address string
	Header  http.Header
}
