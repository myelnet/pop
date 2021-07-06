package filecoin

import (
	"context"
	"fmt"
	"sync"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/crypto"
	"github.com/filecoin-project/go-state-types/dline"
	"github.com/filecoin-project/go-state-types/network"
	"github.com/ipfs/go-cid"
)

func testBlockHeader() *BlockHeader {
	addr, err := address.NewIDAddress(12512063)
	if err != nil {
		panic(err)
	}

	c, err := cid.Decode("bafyreicmaj5hhoy5mgqvamfhgexxyergw7hdeshizghodwkjg6qmpoco7i")
	if err != nil {
		panic(err)
	}

	return &BlockHeader{
		Miner: addr,
		Ticket: &Ticket{
			VRFProof: []byte("vrf proof0000000vrf proof0000000"),
		},
		ElectionProof: &ElectionProof{
			VRFProof: []byte("vrf proof0000000vrf proof0000000"),
		},
		Parents:               []cid.Cid{c, c},
		ParentMessageReceipts: c,
		BLSAggregate:          &crypto.Signature{Type: crypto.SigTypeBLS, Data: []byte("bls signature")},
		ParentWeight:          NewInt(123125126212),
		Messages:              c,
		Height:                85919298723,
		ParentStateRoot:       c,
		BlockSig:              &crypto.Signature{Type: crypto.SigTypeBLS, Data: []byte("block signature")},
		ParentBaseFee:         NewInt(3432432843291),
	}
}

// MockLotusAPI is for testing purposes only
type MockLotusAPI struct {
	act         *Actor  // actor to return when calling StateGetActor
	head        *TipSet // head returned when calling ChainHead
	actMu       sync.Mutex
	actState    *ActorState          // state returned when calling StateReadState
	obj         []byte               // bytes returned when calling ChainReadObj
	objReader   func(cid.Cid) []byte // bytes to return given a specific cid
	msgLookup   chan *MsgLookup      // msgLookup to return when calling StateWaitMsg
	accountKey  address.Address      // address returned when calling StateAccountKey
	lookupID    address.Address      // address returned when calling StateLookupID
	invocResult *InvocResult         // invocResult returned when calling StateCall
}

func NewMockLotusAPI() *MockLotusAPI {
	head, err := NewTipSet([]*BlockHeader{testBlockHeader()})
	if err != nil {
		panic(err)
	}
	return &MockLotusAPI{
		msgLookup: make(chan *MsgLookup),
		head:      head,
	}
}

func (m *MockLotusAPI) ChainHead(context.Context) (*TipSet, error) {
	return m.head, nil
}

func (m *MockLotusAPI) GasEstimateMessageGas(ctx context.Context, msg *Message, spec *MessageSendSpec, tsk TipSetKey) (*Message, error) {
	return msg, nil
}

func (m *MockLotusAPI) StateGetActor(ctx context.Context, addr address.Address, tsk TipSetKey) (*Actor, error) {
	m.actMu.Lock()
	defer m.actMu.Unlock()
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
	if m.accountKey != address.Undef {
		return m.accountKey, nil
	}
	return addr, nil
}

func (m *MockLotusAPI) StateLookupID(ctx context.Context, addr address.Address, tsk TipSetKey) (address.Address, error) {
	return m.lookupID, nil
}

func (m *MockLotusAPI) StateReadState(ctx context.Context, addr address.Address, tsk TipSetKey) (*ActorState, error) {
	return m.actState, nil
}

func (m *MockLotusAPI) StateNetworkVersion(ctx context.Context, tsk TipSetKey) (network.Version, error) {
	return network.Version10, nil
}

func (m *MockLotusAPI) StateMarketBalance(ctx context.Context, addr address.Address, tsk TipSetKey) (MarketBalance, error) {
	return MarketBalance{}, nil
}

func (m *MockLotusAPI) StateDealProviderCollateralBounds(ctx context.Context, s abi.PaddedPieceSize, b bool, tsk TipSetKey) (DealCollateralBounds, error) {
	return DealCollateralBounds{}, nil
}

func (m *MockLotusAPI) StateMinerProvingDeadline(ctx context.Context, addr address.Address, tsk TipSetKey) (*dline.Info, error) {
	return nil, nil
}

func (m *MockLotusAPI) StateCall(ctx context.Context, msg *Message, tsk TipSetKey) (*InvocResult, error) {
	return m.invocResult, nil
}

func (m *MockLotusAPI) ChainGetMessage(ctx context.Context, c cid.Cid) (*Message, error) {
	return nil, nil
}

func (m *MockLotusAPI) ChainReadObj(ctx context.Context, c cid.Cid) ([]byte, error) {
	if m.objReader != nil {
		return m.objReader(c), nil
	}
	return m.obj, nil
}

func (m *MockLotusAPI) StateMinerInfo(ctx context.Context, addr address.Address, tsk TipSetKey) (MinerInfo, error) {
	return MinerInfo{}, nil
}

func (m *MockLotusAPI) Close() {}

// Set lotus data

func (m *MockLotusAPI) SetActor(act *Actor) {
	m.act = act
}

func (m *MockLotusAPI) SetActorState(state *ActorState) {
	m.actMu.Lock()
	m.actState = state
	m.actMu.Unlock()
}

func (m *MockLotusAPI) SetObject(obj []byte) {
	m.obj = obj
}

func (m *MockLotusAPI) SetObjectReader(r func(cid.Cid) []byte) {
	m.objReader = r
}

func (m *MockLotusAPI) SetAccountKey(addr address.Address) {
	m.accountKey = addr
}

// SetMsgLookup to release the StateWaitMsg request
func (m *MockLotusAPI) SetMsgLookup(lkp *MsgLookup) {
	m.msgLookup <- lkp
}

func (m *MockLotusAPI) SetInvocResult(i *InvocResult) {
	m.invocResult = i
}
