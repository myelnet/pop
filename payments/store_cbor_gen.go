// Code generated by github.com/whyrusleeping/cbor-gen. DO NOT EDIT.

package payments

import (
	"fmt"
	"io"
	"sort"

	address "github.com/filecoin-project/go-address"
	paych "github.com/filecoin-project/specs-actors/v4/actors/builtin/paych"
	cid "github.com/ipfs/go-cid"
	cbg "github.com/whyrusleeping/cbor-gen"
	xerrors "golang.org/x/xerrors"
)

var _ = xerrors.Errorf
var _ = cid.Undef
var _ = sort.Sort

var lengthBufVoucherInfo = []byte{131}

func (t *VoucherInfo) MarshalCBOR(w io.Writer) error {
	if t == nil {
		_, err := w.Write(cbg.CborNull)
		return err
	}
	if _, err := w.Write(lengthBufVoucherInfo); err != nil {
		return err
	}

	scratch := make([]byte, 9)

	// t.Voucher (paych.SignedVoucher) (struct)
	if err := t.Voucher.MarshalCBOR(w); err != nil {
		return err
	}

	// t.Proof ([]uint8) (slice)
	if len(t.Proof) > cbg.ByteArrayMaxLen {
		return xerrors.Errorf("Byte array in field t.Proof was too long")
	}

	if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajByteString, uint64(len(t.Proof))); err != nil {
		return err
	}

	if _, err := w.Write(t.Proof[:]); err != nil {
		return err
	}

	// t.Submitted (bool) (bool)
	if err := cbg.WriteBool(w, t.Submitted); err != nil {
		return err
	}
	return nil
}

func (t *VoucherInfo) UnmarshalCBOR(r io.Reader) error {
	*t = VoucherInfo{}

	br := cbg.GetPeeker(r)
	scratch := make([]byte, 8)

	maj, extra, err := cbg.CborReadHeaderBuf(br, scratch)
	if err != nil {
		return err
	}
	if maj != cbg.MajArray {
		return fmt.Errorf("cbor input should be of type array")
	}

	if extra != 3 {
		return fmt.Errorf("cbor input had wrong number of fields")
	}

	// t.Voucher (paych.SignedVoucher) (struct)

	{

		b, err := br.ReadByte()
		if err != nil {
			return err
		}
		if b != cbg.CborNull[0] {
			if err := br.UnreadByte(); err != nil {
				return err
			}
			t.Voucher = new(paych.SignedVoucher)
			if err := t.Voucher.UnmarshalCBOR(br); err != nil {
				return xerrors.Errorf("unmarshaling t.Voucher pointer: %w", err)
			}
		}

	}
	// t.Proof ([]uint8) (slice)

	maj, extra, err = cbg.CborReadHeaderBuf(br, scratch)
	if err != nil {
		return err
	}

	if extra > cbg.ByteArrayMaxLen {
		return fmt.Errorf("t.Proof: byte array too large (%d)", extra)
	}
	if maj != cbg.MajByteString {
		return fmt.Errorf("expected byte array")
	}

	if extra > 0 {
		t.Proof = make([]uint8, extra)
	}

	if _, err := io.ReadFull(br, t.Proof[:]); err != nil {
		return err
	}
	// t.Submitted (bool) (bool)

	maj, extra, err = cbg.CborReadHeaderBuf(br, scratch)
	if err != nil {
		return err
	}
	if maj != cbg.MajOther {
		return fmt.Errorf("booleans must be major type 7")
	}
	switch extra {
	case 20:
		t.Submitted = false
	case 21:
		t.Submitted = true
	default:
		return fmt.Errorf("booleans are either major type 7, value 20 or 21 (got %d)", extra)
	}
	return nil
}

var lengthBufChannelInfo = []byte{140}

func (t *ChannelInfo) MarshalCBOR(w io.Writer) error {
	if t == nil {
		_, err := w.Write(cbg.CborNull)
		return err
	}
	if _, err := w.Write(lengthBufChannelInfo); err != nil {
		return err
	}

	scratch := make([]byte, 9)

	// t.ChannelID (string) (string)
	if len(t.ChannelID) > cbg.MaxLength {
		return xerrors.Errorf("Value in field t.ChannelID was too long")
	}

	if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajTextString, uint64(len(t.ChannelID))); err != nil {
		return err
	}
	if _, err := io.WriteString(w, string(t.ChannelID)); err != nil {
		return err
	}

	// t.Channel (address.Address) (struct)
	if err := t.Channel.MarshalCBOR(w); err != nil {
		return err
	}

	// t.Control (address.Address) (struct)
	if err := t.Control.MarshalCBOR(w); err != nil {
		return err
	}

	// t.Target (address.Address) (struct)
	if err := t.Target.MarshalCBOR(w); err != nil {
		return err
	}

	// t.Direction (uint64) (uint64)

	if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajUnsignedInt, uint64(t.Direction)); err != nil {
		return err
	}

	// t.Vouchers ([]*payments.VoucherInfo) (slice)
	if len(t.Vouchers) > cbg.MaxLength {
		return xerrors.Errorf("Slice value in field t.Vouchers was too long")
	}

	if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajArray, uint64(len(t.Vouchers))); err != nil {
		return err
	}
	for _, v := range t.Vouchers {
		if err := v.MarshalCBOR(w); err != nil {
			return err
		}
	}

	// t.NextLane (uint64) (uint64)

	if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajUnsignedInt, uint64(t.NextLane)); err != nil {
		return err
	}

	// t.Amount (big.Int) (struct)
	if err := t.Amount.MarshalCBOR(w); err != nil {
		return err
	}

	// t.PendingAmount (big.Int) (struct)
	if err := t.PendingAmount.MarshalCBOR(w); err != nil {
		return err
	}

	// t.CreateMsg (cid.Cid) (struct)

	if t.CreateMsg == nil {
		if _, err := w.Write(cbg.CborNull); err != nil {
			return err
		}
	} else {
		if err := cbg.WriteCidBuf(scratch, w, *t.CreateMsg); err != nil {
			return xerrors.Errorf("failed to write cid field t.CreateMsg: %w", err)
		}
	}

	// t.AddFundsMsg (cid.Cid) (struct)

	if t.AddFundsMsg == nil {
		if _, err := w.Write(cbg.CborNull); err != nil {
			return err
		}
	} else {
		if err := cbg.WriteCidBuf(scratch, w, *t.AddFundsMsg); err != nil {
			return xerrors.Errorf("failed to write cid field t.AddFundsMsg: %w", err)
		}
	}

	// t.Settling (bool) (bool)
	if err := cbg.WriteBool(w, t.Settling); err != nil {
		return err
	}
	return nil
}

func (t *ChannelInfo) UnmarshalCBOR(r io.Reader) error {
	*t = ChannelInfo{}

	br := cbg.GetPeeker(r)
	scratch := make([]byte, 8)

	maj, extra, err := cbg.CborReadHeaderBuf(br, scratch)
	if err != nil {
		return err
	}
	if maj != cbg.MajArray {
		return fmt.Errorf("cbor input should be of type array")
	}

	if extra != 12 {
		return fmt.Errorf("cbor input had wrong number of fields")
	}

	// t.ChannelID (string) (string)

	{
		sval, err := cbg.ReadStringBuf(br, scratch)
		if err != nil {
			return err
		}

		t.ChannelID = string(sval)
	}
	// t.Channel (address.Address) (struct)

	{

		b, err := br.ReadByte()
		if err != nil {
			return err
		}
		if b != cbg.CborNull[0] {
			if err := br.UnreadByte(); err != nil {
				return err
			}
			t.Channel = new(address.Address)
			if err := t.Channel.UnmarshalCBOR(br); err != nil {
				return xerrors.Errorf("unmarshaling t.Channel pointer: %w", err)
			}
		}

	}
	// t.Control (address.Address) (struct)

	{

		if err := t.Control.UnmarshalCBOR(br); err != nil {
			return xerrors.Errorf("unmarshaling t.Control: %w", err)
		}

	}
	// t.Target (address.Address) (struct)

	{

		if err := t.Target.UnmarshalCBOR(br); err != nil {
			return xerrors.Errorf("unmarshaling t.Target: %w", err)
		}

	}
	// t.Direction (uint64) (uint64)

	{

		maj, extra, err = cbg.CborReadHeaderBuf(br, scratch)
		if err != nil {
			return err
		}
		if maj != cbg.MajUnsignedInt {
			return fmt.Errorf("wrong type for uint64 field")
		}
		t.Direction = uint64(extra)

	}
	// t.Vouchers ([]*payments.VoucherInfo) (slice)

	maj, extra, err = cbg.CborReadHeaderBuf(br, scratch)
	if err != nil {
		return err
	}

	if extra > cbg.MaxLength {
		return fmt.Errorf("t.Vouchers: array too large (%d)", extra)
	}

	if maj != cbg.MajArray {
		return fmt.Errorf("expected cbor array")
	}

	if extra > 0 {
		t.Vouchers = make([]*VoucherInfo, extra)
	}

	for i := 0; i < int(extra); i++ {

		var v VoucherInfo
		if err := v.UnmarshalCBOR(br); err != nil {
			return err
		}

		t.Vouchers[i] = &v
	}

	// t.NextLane (uint64) (uint64)

	{

		maj, extra, err = cbg.CborReadHeaderBuf(br, scratch)
		if err != nil {
			return err
		}
		if maj != cbg.MajUnsignedInt {
			return fmt.Errorf("wrong type for uint64 field")
		}
		t.NextLane = uint64(extra)

	}
	// t.Amount (big.Int) (struct)

	{

		if err := t.Amount.UnmarshalCBOR(br); err != nil {
			return xerrors.Errorf("unmarshaling t.Amount: %w", err)
		}

	}
	// t.PendingAmount (big.Int) (struct)

	{

		if err := t.PendingAmount.UnmarshalCBOR(br); err != nil {
			return xerrors.Errorf("unmarshaling t.PendingAmount: %w", err)
		}

	}
	// t.CreateMsg (cid.Cid) (struct)

	{

		b, err := br.ReadByte()
		if err != nil {
			return err
		}
		if b != cbg.CborNull[0] {
			if err := br.UnreadByte(); err != nil {
				return err
			}

			c, err := cbg.ReadCid(br)
			if err != nil {
				return xerrors.Errorf("failed to read cid field t.CreateMsg: %w", err)
			}

			t.CreateMsg = &c
		}

	}
	// t.AddFundsMsg (cid.Cid) (struct)

	{

		b, err := br.ReadByte()
		if err != nil {
			return err
		}
		if b != cbg.CborNull[0] {
			if err := br.UnreadByte(); err != nil {
				return err
			}

			c, err := cbg.ReadCid(br)
			if err != nil {
				return xerrors.Errorf("failed to read cid field t.AddFundsMsg: %w", err)
			}

			t.AddFundsMsg = &c
		}

	}
	// t.Settling (bool) (bool)

	maj, extra, err = cbg.CborReadHeaderBuf(br, scratch)
	if err != nil {
		return err
	}
	if maj != cbg.MajOther {
		return fmt.Errorf("booleans must be major type 7")
	}
	switch extra {
	case 20:
		t.Settling = false
	case 21:
		t.Settling = true
	default:
		return fmt.Errorf("booleans are either major type 7, value 20 or 21 (got %d)", extra)
	}
	return nil
}

var lengthBufMsgInfo = []byte{132}

func (t *MsgInfo) MarshalCBOR(w io.Writer) error {
	if t == nil {
		_, err := w.Write(cbg.CborNull)
		return err
	}
	if _, err := w.Write(lengthBufMsgInfo); err != nil {
		return err
	}

	scratch := make([]byte, 9)

	// t.ChannelID (string) (string)
	if len(t.ChannelID) > cbg.MaxLength {
		return xerrors.Errorf("Value in field t.ChannelID was too long")
	}

	if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajTextString, uint64(len(t.ChannelID))); err != nil {
		return err
	}
	if _, err := io.WriteString(w, string(t.ChannelID)); err != nil {
		return err
	}

	// t.MsgCid (cid.Cid) (struct)

	if err := cbg.WriteCidBuf(scratch, w, t.MsgCid); err != nil {
		return xerrors.Errorf("failed to write cid field t.MsgCid: %w", err)
	}

	// t.Received (bool) (bool)
	if err := cbg.WriteBool(w, t.Received); err != nil {
		return err
	}

	// t.Err (string) (string)
	if len(t.Err) > cbg.MaxLength {
		return xerrors.Errorf("Value in field t.Err was too long")
	}

	if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajTextString, uint64(len(t.Err))); err != nil {
		return err
	}
	if _, err := io.WriteString(w, string(t.Err)); err != nil {
		return err
	}
	return nil
}

func (t *MsgInfo) UnmarshalCBOR(r io.Reader) error {
	*t = MsgInfo{}

	br := cbg.GetPeeker(r)
	scratch := make([]byte, 8)

	maj, extra, err := cbg.CborReadHeaderBuf(br, scratch)
	if err != nil {
		return err
	}
	if maj != cbg.MajArray {
		return fmt.Errorf("cbor input should be of type array")
	}

	if extra != 4 {
		return fmt.Errorf("cbor input had wrong number of fields")
	}

	// t.ChannelID (string) (string)

	{
		sval, err := cbg.ReadStringBuf(br, scratch)
		if err != nil {
			return err
		}

		t.ChannelID = string(sval)
	}
	// t.MsgCid (cid.Cid) (struct)

	{

		c, err := cbg.ReadCid(br)
		if err != nil {
			return xerrors.Errorf("failed to read cid field t.MsgCid: %w", err)
		}

		t.MsgCid = c

	}
	// t.Received (bool) (bool)

	maj, extra, err = cbg.CborReadHeaderBuf(br, scratch)
	if err != nil {
		return err
	}
	if maj != cbg.MajOther {
		return fmt.Errorf("booleans must be major type 7")
	}
	switch extra {
	case 20:
		t.Received = false
	case 21:
		t.Received = true
	default:
		return fmt.Errorf("booleans are either major type 7, value 20 or 21 (got %d)", extra)
	}
	// t.Err (string) (string)

	{
		sval, err := cbg.ReadStringBuf(br, scratch)
		if err != nil {
			return err
		}

		t.Err = string(sval)
	}
	return nil
}
