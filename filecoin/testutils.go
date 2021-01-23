package filecoin

import (
	"context"
	"fmt"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/network"
	"github.com/ipfs/go-cid"
)

// MockLotusAPI is for testing purposes only
type MockLotusAPI struct {
	act       *Actor          // actor to return when calling StateGetActor
	msgLookup chan *MsgLookup // msgLookup to return when calling StateWaitMsg
}

func NewMockLotusAPI() *MockLotusAPI {
	return &MockLotusAPI{
		msgLookup: make(chan *MsgLookup),
	}
}

func (m *MockLotusAPI) ChainHead(context.Context) (*TipSet, error) {
	return nil, nil
}

func (m *MockLotusAPI) GasEstimateMessageGas(ctx context.Context, msg *Message, spec *MessageSendSpec, tsk TipSetKey) (*Message, error) {
	return msg, nil
}

func (m *MockLotusAPI) StateGetActor(ctx context.Context, addr address.Address, tsk TipSetKey) (*Actor, error) {
	return m.act, nil
}

func (m *MockLotusAPI) MpoolPush(ctx context.Context, smsg *SignedMessage) (cid.Cid, error) {
	return smsg.Cid(), nil
}

func (m *MockLotusAPI) StateWaitMsg(ctx context.Context, c cid.Cid, conf uint64) (*MsgLookup, error) {
	select {
	case lkp := <-m.msgLookup:
		return lkp, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("context timeout")
	}
}

func (m *MockLotusAPI) StateAccountKey(ctx context.Context, addr address.Address, tsk TipSetKey) (address.Address, error) {
	return addr, nil
}

func (m *MockLotusAPI) StateReadState(ctx context.Context, addr address.Address, tsk TipSetKey) (*ActorState, error) {
	return nil, nil
}

func (m *MockLotusAPI) StateNetworkVersion(ctx context.Context, tsk TipSetKey) (network.Version, error) {
	return network.Version10, nil
}

func (m *MockLotusAPI) ChainReadObj(ctx context.Context, c cid.Cid) ([]byte, error) {
	return []byte("test"), nil
}

func (m *MockLotusAPI) Close() {}

// Set lotus data

func (m *MockLotusAPI) SetActor(act *Actor) {
	m.act = act
}

// SetMsgLookup to release the StateWaitMsg request
func (m *MockLotusAPI) SetMsgLookup(lkp *MsgLookup) {
	m.msgLookup <- lkp
}
