package lotus

import (
	"context"
	"net/http"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-jsonrpc"
	"github.com/filecoin-project/go-state-types/network"
	"github.com/ipfs/go-cid"
)

// API tells the rpc client how to handle the different methods
type API struct {
	Methods struct {
		ChainHead             func(context.Context) (*TipSet, error)
		GasEstimateMessageGas func(context.Context, *Message, *MessageSendSpec, TipSetKey) (*Message, error)
		StateGetActor         func(context.Context, address.Address, TipSetKey) (*Actor, error)
		MpoolPush             func(context.Context, *SignedMessage) (cid.Cid, error)
		StateWaitMsg          func(context.Context, cid.Cid, uint64) (*MsgLookup, error)
		StateAccountKey       func(context.Context, address.Address, TipSetKey) (address.Address, error)
		StateReadState        func(context.Context, address.Address, TipSetKey) (*ActorState, error)
		StateNetworkVersion   func(context.Context, TipSetKey) (network.Version, error)
		ChainReadObj          func(context.Context, cid.Cid) ([]byte, error)
	}
}

// FilecoinAPI declares minimum required methods for interacting with the Filecoin Blockchain
type FilecoinAPI interface {
	ChainHead(context.Context) (*TipSet, error)
	GasEstimateMessageGas(context.Context, *Message, *MessageSendSpec, TipSetKey) (*Message, error)
	StateGetActor(context.Context, address.Address, TipSetKey) (*Actor, error)
	MpoolPush(context.Context, *SignedMessage) (cid.Cid, error)
	StateWaitMsg(context.Context, cid.Cid, uint64) (*MsgLookup, error)
	StateAccountKey(context.Context, address.Address, TipSetKey) (address.Address, error)
	StateReadState(context.Context, address.Address, TipSetKey) (*ActorState, error)
	StateNetworkVersion(context.Context, TipSetKey) (network.Version, error)
	ChainReadObj(context.Context, cid.Cid) ([]byte, error)
}

// NewRPC starts a new lotus RPC client
func NewRPC(ctx context.Context, addr string, header http.Header) (FilecoinAPI, jsonrpc.ClientCloser, error) {
	var res API
	closer, err := jsonrpc.NewMergeClient(ctx, addr, "Filecoin",
		[]interface{}{
			&res.Methods,
		},
		header,
	)
	return &res, closer, err
}

func (a *API) ChainHead(ctx context.Context) (*TipSet, error) {
	return a.Methods.ChainHead(ctx)
}

func (a *API) GasEstimateMessageGas(ctx context.Context, m *Message, mss *MessageSendSpec, tsk TipSetKey) (*Message, error) {
	return a.Methods.GasEstimateMessageGas(ctx, m, mss, tsk)
}

func (a *API) StateGetActor(ctx context.Context, addr address.Address, tsk TipSetKey) (*Actor, error) {
	return a.Methods.StateGetActor(ctx, addr, tsk)
}

func (a *API) MpoolPush(ctx context.Context, sm *SignedMessage) (cid.Cid, error) {
	return a.Methods.MpoolPush(ctx, sm)
}

func (a *API) StateWaitMsg(ctx context.Context, c cid.Cid, conf uint64) (*MsgLookup, error) {
	return a.Methods.StateWaitMsg(ctx, c, conf)
}

func (a *API) StateAccountKey(ctx context.Context, addr address.Address, tsk TipSetKey) (address.Address, error) {
	return a.Methods.StateAccountKey(ctx, addr, tsk)
}

func (a *API) StateReadState(ctx context.Context, addr address.Address, tsk TipSetKey) (*ActorState, error) {
	return a.Methods.StateReadState(ctx, addr, tsk)
}

func (a *API) StateNetworkVersion(ctx context.Context, tsk TipSetKey) (network.Version, error) {
	return a.Methods.StateNetworkVersion(ctx, tsk)
}

func (a *API) ChainReadObj(ctx context.Context, c cid.Cid) ([]byte, error) {
	return a.Methods.ChainReadObj(ctx, c)
}
