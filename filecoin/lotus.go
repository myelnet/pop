package filecoin

import (
	"context"
	"net/http"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-jsonrpc"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/network"
	"github.com/ipfs/go-cid"
)

// LotusAPI tells the rpc client how to handle the different methods
type LotusAPI struct {
	Methods struct {
		ChainHead                         func(context.Context) (*TipSet, error)
		GasEstimateMessageGas             func(context.Context, *Message, *MessageSendSpec, TipSetKey) (*Message, error)
		StateGetActor                     func(context.Context, address.Address, TipSetKey) (*Actor, error)
		MpoolPush                         func(context.Context, *SignedMessage) (cid.Cid, error)
		StateWaitMsg                      func(context.Context, cid.Cid, uint64) (*MsgLookup, error)
		StateAccountKey                   func(context.Context, address.Address, TipSetKey) (address.Address, error)
		StateLookupID                     func(context.Context, address.Address, TipSetKey) (address.Address, error)
		StateReadState                    func(context.Context, address.Address, TipSetKey) (*ActorState, error)
		StateNetworkVersion               func(context.Context, TipSetKey) (network.Version, error)
		StateMarketBalance                func(context.Context, address.Address, TipSetKey) (MarketBalance, error)
		StateDealProviderCollateralBounds func(context.Context, abi.PaddedPieceSize, bool, TipSetKey) (DealCollateralBounds, error)
		StateMinerInfo                    func(context.Context, address.Address, TipSetKey) (MinerInfo, error)
		StateListMiners                   func(context.Context, TipSetKey) ([]address.Address, error)
		ChainReadObj                      func(context.Context, cid.Cid) ([]byte, error)
		ChainGetMessage                   func(context.Context, cid.Cid) (*Message, error)
	}
	closer jsonrpc.ClientCloser
}

// NewLotusRPC starts a new lotus RPC client
func NewLotusRPC(ctx context.Context, addr string, header http.Header) (API, error) {
	var res LotusAPI
	closer, err := jsonrpc.NewMergeClient(ctx, addr, "Filecoin",
		[]interface{}{
			&res.Methods,
		},
		header,
	)
	res.closer = closer
	return &res, err
}

func (a *LotusAPI) Close() {
	a.closer()
}

func (a *LotusAPI) ChainHead(ctx context.Context) (*TipSet, error) {
	return a.Methods.ChainHead(ctx)
}

func (a *LotusAPI) GasEstimateMessageGas(ctx context.Context, m *Message, mss *MessageSendSpec, tsk TipSetKey) (*Message, error) {
	return a.Methods.GasEstimateMessageGas(ctx, m, mss, tsk)
}

func (a *LotusAPI) StateGetActor(ctx context.Context, addr address.Address, tsk TipSetKey) (*Actor, error) {
	return a.Methods.StateGetActor(ctx, addr, tsk)
}

func (a *LotusAPI) MpoolPush(ctx context.Context, sm *SignedMessage) (cid.Cid, error) {
	return a.Methods.MpoolPush(ctx, sm)
}

func (a *LotusAPI) StateWaitMsg(ctx context.Context, c cid.Cid, conf uint64) (*MsgLookup, error) {
	return a.Methods.StateWaitMsg(ctx, c, conf)
}

func (a *LotusAPI) StateAccountKey(ctx context.Context, addr address.Address, tsk TipSetKey) (address.Address, error) {
	return a.Methods.StateAccountKey(ctx, addr, tsk)
}

func (a *LotusAPI) StateLookupID(ctx context.Context, addr address.Address, tsk TipSetKey) (address.Address, error) {
	return a.Methods.StateLookupID(ctx, addr, tsk)
}

func (a *LotusAPI) StateReadState(ctx context.Context, addr address.Address, tsk TipSetKey) (*ActorState, error) {
	return a.Methods.StateReadState(ctx, addr, tsk)
}

func (a *LotusAPI) StateNetworkVersion(ctx context.Context, tsk TipSetKey) (network.Version, error) {
	return a.Methods.StateNetworkVersion(ctx, tsk)
}

func (a *LotusAPI) StateMarketBalance(ctx context.Context, addr address.Address, tsk TipSetKey) (MarketBalance, error) {
	return a.Methods.StateMarketBalance(ctx, addr, tsk)
}

func (a *LotusAPI) StateDealProviderCollateralBounds(ctx context.Context, size abi.PaddedPieceSize, verified bool, tsk TipSetKey) (DealCollateralBounds, error) {
	return a.Methods.StateDealProviderCollateralBounds(ctx, size, verified, tsk)
}

func (a *LotusAPI) StateMinerInfo(ctx context.Context, addr address.Address, tsk TipSetKey) (MinerInfo, error) {
	return a.Methods.StateMinerInfo(ctx, addr, tsk)
}

func (a *LotusAPI) StateListMiners(ctx context.Context, tsk TipSetKey) ([]address.Address, error) {
	return a.Methods.StateListMiners(ctx, tsk)
}

func (a *LotusAPI) ChainReadObj(ctx context.Context, c cid.Cid) ([]byte, error) {
	return a.Methods.ChainReadObj(ctx, c)
}

func (a *LotusAPI) ChainGetMessage(ctx context.Context, c cid.Cid) (*Message, error) {
	return a.Methods.ChainGetMessage(ctx, c)
}
