package filecoin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	big2 "github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/go-state-types/crypto"
	"github.com/filecoin-project/go-state-types/exitcode"
	proof2 "github.com/filecoin-project/specs-actors/v2/actors/runtime/proof"
	block "github.com/ipfs/go-block-format"
	"github.com/ipfs/go-cid"
	"github.com/minio/blake2b-simd"
	"github.com/prometheus/common/log"
)

//go:generate cbor-gen-for  Ticket ElectionProof ExpTipSet BeaconEntry BlockHeader Message Actor SignedMessage MessageReceipt

// These types are extracted out of lotus to avoid importing the whole project

type TipSet struct {
	cids   []cid.Cid
	blks   []*BlockHeader
	height abi.ChainEpoch
}

type ExpTipSet struct {
	Cids   []cid.Cid
	Blocks []*BlockHeader
	Height abi.ChainEpoch
}

func (ts *TipSet) MarshalJSON() ([]byte, error) {
	return json.Marshal(ExpTipSet{
		Cids:   ts.cids,
		Blocks: ts.blks,
		Height: ts.height,
	})
}

func (ts *TipSet) UnmarshalJSON(b []byte) error {
	var ets ExpTipSet
	if err := json.Unmarshal(b, &ets); err != nil {
		return err
	}

	ots, err := NewTipSet(ets.Blocks)
	if err != nil {
		return err
	}

	*ts = *ots

	return nil
}

func (ts *TipSet) Height() abi.ChainEpoch {
	return ts.height
}

func tipsetSortFunc(blks []*BlockHeader) func(i, j int) bool {
	return func(i, j int) bool {
		ti := blks[i].LastTicket()
		tj := blks[j].LastTicket()

		if ti.Equals(tj) {
			log.Warnf("blocks have same ticket (%s %s)", blks[i].Miner, blks[j].Miner)
			return bytes.Compare(blks[i].Cid().Bytes(), blks[j].Cid().Bytes()) < 0
		}

		return ti.Less(tj)
	}
}

// NewTipSet Checks:
// * A tipset is composed of at least one block. (Because of our variable
//   number of blocks per tipset, determined by randomness, we do not impose
//   an upper limit.)
// * All blocks have the same height.
// * All blocks have the same parents (same number of them and matching CIDs).
func NewTipSet(blks []*BlockHeader) (*TipSet, error) {
	if len(blks) == 0 {
		return nil, fmt.Errorf("NewTipSet called with zero length array of blocks")
	}

	sort.Slice(blks, tipsetSortFunc(blks))

	var ts TipSet
	ts.cids = []cid.Cid{blks[0].Cid()}
	ts.blks = blks
	for _, b := range blks[1:] {
		if b.Height != blks[0].Height {
			return nil, fmt.Errorf("cannot create tipset with mismatching heights")
		}

		if len(blks[0].Parents) != len(b.Parents) {
			return nil, fmt.Errorf("cannot create tipset with mismatching number of parents")
		}

		for i, cid := range b.Parents {
			if cid != blks[0].Parents[i] {
				return nil, fmt.Errorf("cannot create tipset with mismatching parents")
			}
		}

		ts.cids = append(ts.cids, b.Cid())

	}
	ts.height = blks[0].Height

	return &ts, nil
}

// A TipSetKey is an immutable collection of CIDs forming a unique key for a tipset.
// The CIDs are assumed to be distinct and in canonical order. Two keys with the same
// CIDs in a different order are not considered equal.
// TipSetKey is a lightweight value type, and may be compared for equality with ==.
type TipSetKey struct {
	// The internal representation is a concatenation of the bytes of the CIDs, which are
	// self-describing, wrapped as a string.
	// These gymnastics make the a TipSetKey usable as a map key.
	// The empty key has value "".
	value string
}

var EmptyTSK = TipSetKey{}

type Ticket struct {
	VRFProof []byte
}

func (t *Ticket) Equals(ot *Ticket) bool {
	return bytes.Equal(t.VRFProof, ot.VRFProof)
}

func (t *Ticket) Less(o *Ticket) bool {
	tDigest := blake2b.Sum256(t.VRFProof)
	oDigest := blake2b.Sum256(o.VRFProof)
	return bytes.Compare(tDigest[:], oDigest[:]) < 0
}

type BeaconEntry struct {
	Round uint64
	Data  []byte
}

type ElectionProof struct {
	WinCount int64
	VRFProof []byte
}

type BigInt = big2.Int

func NewInt(i uint64) BigInt {
	return BigInt{Int: big.NewInt(0).SetUint64(i)}
}

func BigAdd(a, b BigInt) BigInt {
	return BigInt{Int: big.NewInt(0).Add(a.Int, b.Int)}
}

type BlockHeader struct {
	Miner address.Address // 0

	Ticket *Ticket // 1

	ElectionProof *ElectionProof // 2

	BeaconEntries []BeaconEntry // 3

	WinPoStProof []proof2.PoStProof // 4

	Parents []cid.Cid // 5

	ParentWeight BigInt // 6

	Height abi.ChainEpoch // 7

	ParentStateRoot cid.Cid // 8

	ParentMessageReceipts cid.Cid // 8

	Messages cid.Cid // 10

	BLSAggregate *crypto.Signature // 11

	Timestamp uint64 // 12

	BlockSig *crypto.Signature // 13

	ForkSignaling uint64 // 14

	// ParentBaseFee is the base fee after executing parent tipset
	ParentBaseFee abi.TokenAmount // 15

	// internal
	validated bool // true if the signature has been validated
}

func (blk *BlockHeader) ToStorageBlock() (block.Block, error) {
	data, err := blk.Serialize()
	if err != nil {
		return nil, err
	}

	c, err := abi.CidBuilder.Sum(data)
	if err != nil {
		return nil, err
	}

	return block.NewBlockWithCid(data, c)
}

func (blk *BlockHeader) Cid() cid.Cid {
	sb, err := blk.ToStorageBlock()
	if err != nil {
		panic(err) // Not sure i'm entirely comfortable with this one, needs to be checked
	}

	return sb.Cid()
}

func (blk *BlockHeader) Serialize() ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := blk.MarshalCBOR(buf); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func (blk *BlockHeader) LastTicket() *Ticket {
	return blk.Ticket
}

type Message struct {
	Version uint64

	To   address.Address
	From address.Address

	Nonce uint64

	Value abi.TokenAmount

	GasLimit   int64
	GasFeeCap  abi.TokenAmount
	GasPremium abi.TokenAmount

	Method abi.MethodNum
	Params []byte
}

func (m *Message) Caller() address.Address {
	return m.From
}

func (m *Message) Receiver() address.Address {
	return m.To
}

func (m *Message) ValueReceived() abi.TokenAmount {
	return m.Value
}

func (m *Message) Serialize() ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := m.MarshalCBOR(buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (m *Message) ToStorageBlock() (block.Block, error) {
	data, err := m.Serialize()
	if err != nil {
		return nil, err
	}

	c, err := abi.CidBuilder.Sum(data)
	if err != nil {
		return nil, err
	}

	return block.NewBlockWithCid(data, c)
}

func (m *Message) Cid() cid.Cid {
	b, err := m.ToStorageBlock()
	if err != nil {
		panic(fmt.Sprintf("failed to marshal message: %s", err)) // I think this is maybe sketchy, what happens if we try to serialize a message with an undefined address in it?
	}

	return b.Cid()
}

type mCid struct {
	*RawMessage
	CID cid.Cid
}

type RawMessage Message

func (m *Message) MarshalJSON() ([]byte, error) {
	return json.Marshal(&mCid{
		RawMessage: (*RawMessage)(m),
		CID:        m.Cid(),
	})
}

type MessageSendSpec struct {
	MaxFee abi.TokenAmount
}

type Actor struct {
	Code    cid.Cid
	Head    cid.Cid
	Nonce   uint64
	Balance BigInt
}

type SignedMessage struct {
	Message   Message
	Signature crypto.Signature
}

func (sm *SignedMessage) ToStorageBlock() (block.Block, error) {
	if sm.Signature.Type == crypto.SigTypeBLS {
		return sm.Message.ToStorageBlock()
	}

	data, err := sm.Serialize()
	if err != nil {
		return nil, err
	}

	c, err := abi.CidBuilder.Sum(data)
	if err != nil {
		return nil, err
	}

	return block.NewBlockWithCid(data, c)
}

func (sm *SignedMessage) Cid() cid.Cid {
	if sm.Signature.Type == crypto.SigTypeBLS {
		return sm.Message.Cid()
	}

	sb, err := sm.ToStorageBlock()
	if err != nil {
		panic(err)
	}

	return sb.Cid()
}

func (sm *SignedMessage) Serialize() ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := sm.MarshalCBOR(buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type smCid struct {
	*RawSignedMessage
	CID cid.Cid
}

type RawSignedMessage SignedMessage

func (sm *SignedMessage) MarshalJSON() ([]byte, error) {
	return json.Marshal(&smCid{
		RawSignedMessage: (*RawSignedMessage)(sm),
		CID:              sm.Cid(),
	})
}

type MessageReceipt struct {
	ExitCode exitcode.ExitCode
	Return   []byte
	GasUsed  int64
}

type MsgLookup struct {
	Message   cid.Cid // Can be different than requested, in case it was replaced, but only gas values changed
	Receipt   MessageReceipt
	ReturnDec interface{}
	TipSet    TipSetKey
	Height    abi.ChainEpoch
}

type ActorState struct {
	Balance BigInt
	State   interface{}
}

const FilecoinPrecision = uint64(1_000_000_000_000_000_000)

type FIL BigInt

func (f FIL) String() string {
	return f.Unitless() + " FIL"
}

func (f FIL) Unitless() string {
	r := new(big.Rat).SetFrac(f.Int, big.NewInt(int64(FilecoinPrecision)))
	if r.Sign() == 0 {
		return "0"
	}
	return strings.TrimRight(strings.TrimRight(r.FloatString(18), "0"), ".")
}

var unitPrefixes = []string{"a", "f", "p", "n", "μ", "m"}

func (f FIL) Short() string {
	n := BigInt(f)

	dn := uint64(1)
	var prefix string
	for _, p := range unitPrefixes {
		if n.LessThan(NewInt(dn * 1000)) {
			prefix = p
			break
		}
		dn *= 1000
	}

	r := new(big.Rat).SetFrac(f.Int, big.NewInt(int64(dn)))
	if r.Sign() == 0 {
		return "0"
	}

	return strings.TrimRight(strings.TrimRight(r.FloatString(3), "0"), ".") + " " + prefix + "FIL"
}

func ParseFIL(s string) (FIL, error) {
	suffix := strings.TrimLeft(s, ".1234567890")
	s = s[:len(s)-len(suffix)]
	var attofil bool
	if suffix != "" {
		norm := strings.ToLower(strings.TrimSpace(suffix))
		switch norm {
		case "", "fil":
		case "attofil", "afil":
			attofil = true
		default:
			return FIL{}, fmt.Errorf("unrecognized suffix: %q", suffix)
		}
	}

	if len(s) > 50 {
		return FIL{}, fmt.Errorf("string length too large: %d", len(s))
	}

	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return FIL{}, fmt.Errorf("failed to parse %q as a decimal number", s)
	}

	if !attofil {
		r = r.Mul(r, big.NewRat(int64(FilecoinPrecision), 1))
	}

	if !r.IsInt() {
		var pref string
		if attofil {
			pref = "atto"
		}
		return FIL{}, fmt.Errorf("invalid %sFIL value: %q", pref, s)
	}

	return FIL{r.Num()}, nil
}
