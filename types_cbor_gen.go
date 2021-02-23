// Code generated by github.com/whyrusleeping/cbor-gen. DO NOT EDIT.

package hop

import (
	"fmt"
	"io"

	cbg "github.com/whyrusleeping/cbor-gen"
	xerrors "golang.org/x/xerrors"
)

var _ = xerrors.Errorf

var lengthBufProvision = []byte{130}

func (t *Provision) MarshalCBOR(w io.Writer) error {
	if t == nil {
		_, err := w.Write(cbg.CborNull)
		return err
	}
	if _, err := w.Write(lengthBufProvision); err != nil {
		return err
	}

	scratch := make([]byte, 9)

	// t.PayloadCID (cid.Cid) (struct)

	if err := cbg.WriteCidBuf(scratch, w, t.PayloadCID); err != nil {
		return xerrors.Errorf("failed to write cid field t.PayloadCID: %w", err)
	}

	// t.Size (uint64) (uint64)

	if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajUnsignedInt, uint64(t.Size)); err != nil {
		return err
	}

	return nil
}

func (t *Provision) UnmarshalCBOR(r io.Reader) error {
	*t = Provision{}

	br := cbg.GetPeeker(r)
	scratch := make([]byte, 8)

	maj, extra, err := cbg.CborReadHeaderBuf(br, scratch)
	if err != nil {
		return err
	}
	if maj != cbg.MajArray {
		return fmt.Errorf("cbor input should be of type array")
	}

	if extra != 2 {
		return fmt.Errorf("cbor input had wrong number of fields")
	}

	// t.PayloadCID (cid.Cid) (struct)

	{

		c, err := cbg.ReadCid(br)
		if err != nil {
			return xerrors.Errorf("failed to read cid field t.PayloadCID: %w", err)
		}

		t.PayloadCID = c

	}
	// t.Size (uint64) (uint64)

	{

		maj, extra, err = cbg.CborReadHeaderBuf(br, scratch)
		if err != nil {
			return err
		}
		if maj != cbg.MajUnsignedInt {
			return fmt.Errorf("wrong type for uint64 field")
		}
		t.Size = uint64(extra)

	}
	return nil
}

var lengthBufQueryResponse = []byte{137}

func (t *QueryResponse) MarshalCBOR(w io.Writer) error {
	if t == nil {
		_, err := w.Write(cbg.CborNull)
		return err
	}
	if _, err := w.Write(lengthBufQueryResponse); err != nil {
		return err
	}

	scratch := make([]byte, 9)

	// t.Status (hop.QueryResponseStatus) (uint64)

	if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajUnsignedInt, uint64(t.Status)); err != nil {
		return err
	}

	// t.PieceCIDFound (hop.QueryItemStatus) (uint64)

	if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajUnsignedInt, uint64(t.PieceCIDFound)); err != nil {
		return err
	}

	// t.Size (uint64) (uint64)

	if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajUnsignedInt, uint64(t.Size)); err != nil {
		return err
	}

	// t.PaymentAddress (address.Address) (struct)
	if err := t.PaymentAddress.MarshalCBOR(w); err != nil {
		return err
	}

	// t.MinPricePerByte (big.Int) (struct)
	if err := t.MinPricePerByte.MarshalCBOR(w); err != nil {
		return err
	}

	// t.MaxPaymentInterval (uint64) (uint64)

	if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajUnsignedInt, uint64(t.MaxPaymentInterval)); err != nil {
		return err
	}

	// t.MaxPaymentIntervalIncrease (uint64) (uint64)

	if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajUnsignedInt, uint64(t.MaxPaymentIntervalIncrease)); err != nil {
		return err
	}

	// t.Message (string) (string)
	if len(t.Message) > cbg.MaxLength {
		return xerrors.Errorf("Value in field t.Message was too long")
	}

	if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajTextString, uint64(len(t.Message))); err != nil {
		return err
	}
	if _, err := io.WriteString(w, string(t.Message)); err != nil {
		return err
	}

	// t.UnsealPrice (big.Int) (struct)
	if err := t.UnsealPrice.MarshalCBOR(w); err != nil {
		return err
	}
	return nil
}

func (t *QueryResponse) UnmarshalCBOR(r io.Reader) error {
	*t = QueryResponse{}

	br := cbg.GetPeeker(r)
	scratch := make([]byte, 8)

	maj, extra, err := cbg.CborReadHeaderBuf(br, scratch)
	if err != nil {
		return err
	}
	if maj != cbg.MajArray {
		return fmt.Errorf("cbor input should be of type array")
	}

	if extra != 9 {
		return fmt.Errorf("cbor input had wrong number of fields")
	}

	// t.Status (hop.QueryResponseStatus) (uint64)

	{

		maj, extra, err = cbg.CborReadHeaderBuf(br, scratch)
		if err != nil {
			return err
		}
		if maj != cbg.MajUnsignedInt {
			return fmt.Errorf("wrong type for uint64 field")
		}
		t.Status = QueryResponseStatus(extra)

	}
	// t.PieceCIDFound (hop.QueryItemStatus) (uint64)

	{

		maj, extra, err = cbg.CborReadHeaderBuf(br, scratch)
		if err != nil {
			return err
		}
		if maj != cbg.MajUnsignedInt {
			return fmt.Errorf("wrong type for uint64 field")
		}
		t.PieceCIDFound = QueryItemStatus(extra)

	}
	// t.Size (uint64) (uint64)

	{

		maj, extra, err = cbg.CborReadHeaderBuf(br, scratch)
		if err != nil {
			return err
		}
		if maj != cbg.MajUnsignedInt {
			return fmt.Errorf("wrong type for uint64 field")
		}
		t.Size = uint64(extra)

	}
	// t.PaymentAddress (address.Address) (struct)

	{

		if err := t.PaymentAddress.UnmarshalCBOR(br); err != nil {
			return xerrors.Errorf("unmarshaling t.PaymentAddress: %w", err)
		}

	}
	// t.MinPricePerByte (big.Int) (struct)

	{

		if err := t.MinPricePerByte.UnmarshalCBOR(br); err != nil {
			return xerrors.Errorf("unmarshaling t.MinPricePerByte: %w", err)
		}

	}
	// t.MaxPaymentInterval (uint64) (uint64)

	{

		maj, extra, err = cbg.CborReadHeaderBuf(br, scratch)
		if err != nil {
			return err
		}
		if maj != cbg.MajUnsignedInt {
			return fmt.Errorf("wrong type for uint64 field")
		}
		t.MaxPaymentInterval = uint64(extra)

	}
	// t.MaxPaymentIntervalIncrease (uint64) (uint64)

	{

		maj, extra, err = cbg.CborReadHeaderBuf(br, scratch)
		if err != nil {
			return err
		}
		if maj != cbg.MajUnsignedInt {
			return fmt.Errorf("wrong type for uint64 field")
		}
		t.MaxPaymentIntervalIncrease = uint64(extra)

	}
	// t.Message (string) (string)

	{
		sval, err := cbg.ReadStringBuf(br, scratch)
		if err != nil {
			return err
		}

		t.Message = string(sval)
	}
	// t.UnsealPrice (big.Int) (struct)

	{

		if err := t.UnsealPrice.UnmarshalCBOR(br); err != nil {
			return xerrors.Errorf("unmarshaling t.UnsealPrice: %w", err)
		}

	}
	return nil
}

var lengthBufQuery = []byte{130}

func (t *Query) MarshalCBOR(w io.Writer) error {
	if t == nil {
		_, err := w.Write(cbg.CborNull)
		return err
	}
	if _, err := w.Write(lengthBufQuery); err != nil {
		return err
	}

	scratch := make([]byte, 9)

	// t.PayloadCID (cid.Cid) (struct)

	if err := cbg.WriteCidBuf(scratch, w, t.PayloadCID); err != nil {
		return xerrors.Errorf("failed to write cid field t.PayloadCID: %w", err)
	}

	// t.QueryParams (hop.QueryParams) (struct)
	if err := t.QueryParams.MarshalCBOR(w); err != nil {
		return err
	}
	return nil
}

func (t *Query) UnmarshalCBOR(r io.Reader) error {
	*t = Query{}

	br := cbg.GetPeeker(r)
	scratch := make([]byte, 8)

	maj, extra, err := cbg.CborReadHeaderBuf(br, scratch)
	if err != nil {
		return err
	}
	if maj != cbg.MajArray {
		return fmt.Errorf("cbor input should be of type array")
	}

	if extra != 2 {
		return fmt.Errorf("cbor input had wrong number of fields")
	}

	// t.PayloadCID (cid.Cid) (struct)

	{

		c, err := cbg.ReadCid(br)
		if err != nil {
			return xerrors.Errorf("failed to read cid field t.PayloadCID: %w", err)
		}

		t.PayloadCID = c

	}
	// t.QueryParams (hop.QueryParams) (struct)

	{

		if err := t.QueryParams.UnmarshalCBOR(br); err != nil {
			return xerrors.Errorf("unmarshaling t.QueryParams: %w", err)
		}

	}
	return nil
}

var lengthBufQueryParams = []byte{129}

func (t *QueryParams) MarshalCBOR(w io.Writer) error {
	if t == nil {
		_, err := w.Write(cbg.CborNull)
		return err
	}
	if _, err := w.Write(lengthBufQueryParams); err != nil {
		return err
	}

	scratch := make([]byte, 9)

	// t.PieceCID (cid.Cid) (struct)

	if t.PieceCID == nil {
		if _, err := w.Write(cbg.CborNull); err != nil {
			return err
		}
	} else {
		if err := cbg.WriteCidBuf(scratch, w, *t.PieceCID); err != nil {
			return xerrors.Errorf("failed to write cid field t.PieceCID: %w", err)
		}
	}

	return nil
}

func (t *QueryParams) UnmarshalCBOR(r io.Reader) error {
	*t = QueryParams{}

	br := cbg.GetPeeker(r)
	scratch := make([]byte, 8)

	maj, extra, err := cbg.CborReadHeaderBuf(br, scratch)
	if err != nil {
		return err
	}
	if maj != cbg.MajArray {
		return fmt.Errorf("cbor input should be of type array")
	}

	if extra != 1 {
		return fmt.Errorf("cbor input had wrong number of fields")
	}

	// t.PieceCID (cid.Cid) (struct)

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
				return xerrors.Errorf("failed to read cid field t.PieceCID: %w", err)
			}

			t.PieceCID = &c
		}

	}
	return nil
}

var lengthBufAsk = []byte{131}

func (t *Ask) MarshalCBOR(w io.Writer) error {
	if t == nil {
		_, err := w.Write(cbg.CborNull)
		return err
	}
	if _, err := w.Write(lengthBufAsk); err != nil {
		return err
	}

	scratch := make([]byte, 9)

	// t.PricePerByte (big.Int) (struct)
	if err := t.PricePerByte.MarshalCBOR(w); err != nil {
		return err
	}

	// t.PaymentInterval (uint64) (uint64)

	if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajUnsignedInt, uint64(t.PaymentInterval)); err != nil {
		return err
	}

	// t.PaymentIntervalIncrease (uint64) (uint64)

	if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajUnsignedInt, uint64(t.PaymentIntervalIncrease)); err != nil {
		return err
	}

	return nil
}

func (t *Ask) UnmarshalCBOR(r io.Reader) error {
	*t = Ask{}

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

	// t.PricePerByte (big.Int) (struct)

	{

		if err := t.PricePerByte.UnmarshalCBOR(br); err != nil {
			return xerrors.Errorf("unmarshaling t.PricePerByte: %w", err)
		}

	}
	// t.PaymentInterval (uint64) (uint64)

	{

		maj, extra, err = cbg.CborReadHeaderBuf(br, scratch)
		if err != nil {
			return err
		}
		if maj != cbg.MajUnsignedInt {
			return fmt.Errorf("wrong type for uint64 field")
		}
		t.PaymentInterval = uint64(extra)

	}
	// t.PaymentIntervalIncrease (uint64) (uint64)

	{

		maj, extra, err = cbg.CborReadHeaderBuf(br, scratch)
		if err != nil {
			return err
		}
		if maj != cbg.MajUnsignedInt {
			return fmt.Errorf("wrong type for uint64 field")
		}
		t.PaymentIntervalIncrease = uint64(extra)

	}
	return nil
}

var lengthBufStorageDataTransferVoucher = []byte{129}

func (t *StorageDataTransferVoucher) MarshalCBOR(w io.Writer) error {
	if t == nil {
		_, err := w.Write(cbg.CborNull)
		return err
	}
	if _, err := w.Write(lengthBufStorageDataTransferVoucher); err != nil {
		return err
	}

	scratch := make([]byte, 9)

	// t.Proposal (cid.Cid) (struct)

	if err := cbg.WriteCidBuf(scratch, w, t.Proposal); err != nil {
		return xerrors.Errorf("failed to write cid field t.Proposal: %w", err)
	}

	return nil
}

func (t *StorageDataTransferVoucher) UnmarshalCBOR(r io.Reader) error {
	*t = StorageDataTransferVoucher{}

	br := cbg.GetPeeker(r)
	scratch := make([]byte, 8)

	maj, extra, err := cbg.CborReadHeaderBuf(br, scratch)
	if err != nil {
		return err
	}
	if maj != cbg.MajArray {
		return fmt.Errorf("cbor input should be of type array")
	}

	if extra != 1 {
		return fmt.Errorf("cbor input had wrong number of fields")
	}

	// t.Proposal (cid.Cid) (struct)

	{

		c, err := cbg.ReadCid(br)
		if err != nil {
			return xerrors.Errorf("failed to read cid field t.Proposal: %w", err)
		}

		t.Proposal = c

	}
	return nil
}
