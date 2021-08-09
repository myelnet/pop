// Code generated by github.com/whyrusleeping/cbor-gen. DO NOT EDIT.

package exchange

import (
	"fmt"
	"io"
	"sort"

	cid "github.com/ipfs/go-cid"
	cbg "github.com/whyrusleeping/cbor-gen"
	xerrors "golang.org/x/xerrors"
)

var _ = xerrors.Errorf
var _ = cid.Undef
var _ = sort.Sort

func (t *DataRef) MarshalCBOR(w io.Writer) error {
	if t == nil {
		_, err := w.Write(cbg.CborNull)
		return err
	}
	if _, err := w.Write([]byte{165}); err != nil {
		return err
	}

	scratch := make([]byte, 9)

	// t.PayloadCID (cid.Cid) (struct)
	if len("PayloadCID") > cbg.MaxLength {
		return xerrors.Errorf("Value in field \"PayloadCID\" was too long")
	}

	if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajTextString, uint64(len("PayloadCID"))); err != nil {
		return err
	}
	if _, err := io.WriteString(w, string("PayloadCID")); err != nil {
		return err
	}

	if err := cbg.WriteCidBuf(scratch, w, t.PayloadCID); err != nil {
		return xerrors.Errorf("failed to write cid field t.PayloadCID: %w", err)
	}

	// t.PayloadSize (int64) (int64)
	if len("PayloadSize") > cbg.MaxLength {
		return xerrors.Errorf("Value in field \"PayloadSize\" was too long")
	}

	if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajTextString, uint64(len("PayloadSize"))); err != nil {
		return err
	}
	if _, err := io.WriteString(w, string("PayloadSize")); err != nil {
		return err
	}

	if t.PayloadSize >= 0 {
		if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajUnsignedInt, uint64(t.PayloadSize)); err != nil {
			return err
		}
	} else {
		if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajNegativeInt, uint64(-t.PayloadSize-1)); err != nil {
			return err
		}
	}

	// t.Keys (map[string]struct {}) (map)
	if len("Keys") > cbg.MaxLength {
		return xerrors.Errorf("Value in field \"Keys\" was too long")
	}

	if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajTextString, uint64(len("Keys"))); err != nil {
		return err
	}
	if _, err := io.WriteString(w, string("Keys")); err != nil {
		return err
	}

	{
		if len(t.Keys) > 4096 {
			return xerrors.Errorf("cannot marshal t.Keys map too large")
		}

		if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajMap, uint64(len(t.Keys))); err != nil {
			return err
		}

		keys := make([]string, 0, len(t.Keys))
		for k := range t.Keys {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if len(k) > cbg.MaxLength {
				return xerrors.Errorf("Value in field k was too long")
			}

			if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajTextString, uint64(len(k))); err != nil {
				return err
			}
			if _, err := io.WriteString(w, string(k)); err != nil {
				return err
			}
		}
	}

	// t.Freq (int64) (int64)
	if len("Freq") > cbg.MaxLength {
		return xerrors.Errorf("Value in field \"Freq\" was too long")
	}

	if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajTextString, uint64(len("Freq"))); err != nil {
		return err
	}
	if _, err := io.WriteString(w, string("Freq")); err != nil {
		return err
	}

	if t.Freq >= 0 {
		if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajUnsignedInt, uint64(t.Freq)); err != nil {
			return err
		}
	} else {
		if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajNegativeInt, uint64(-t.Freq-1)); err != nil {
			return err
		}
	}

	// t.BucketID (int64) (int64)
	if len("BucketID") > cbg.MaxLength {
		return xerrors.Errorf("Value in field \"BucketID\" was too long")
	}

	if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajTextString, uint64(len("BucketID"))); err != nil {
		return err
	}
	if _, err := io.WriteString(w, string("BucketID")); err != nil {
		return err
	}

	if t.BucketID >= 0 {
		if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajUnsignedInt, uint64(t.BucketID)); err != nil {
			return err
		}
	} else {
		if err := cbg.WriteMajorTypeHeaderBuf(scratch, w, cbg.MajNegativeInt, uint64(-t.BucketID-1)); err != nil {
			return err
		}
	}
	return nil
}

func (t *DataRef) UnmarshalCBOR(r io.Reader) error {
	*t = DataRef{}

	br := cbg.GetPeeker(r)
	scratch := make([]byte, 8)

	maj, extra, err := cbg.CborReadHeaderBuf(br, scratch)
	if err != nil {
		return err
	}
	if maj != cbg.MajMap {
		return fmt.Errorf("cbor input should be of type map")
	}

	if extra > cbg.MaxLength {
		return fmt.Errorf("DataRef: map struct too large (%d)", extra)
	}

	var name string
	n := extra

	for i := uint64(0); i < n; i++ {

		{
			sval, err := cbg.ReadStringBuf(br, scratch)
			if err != nil {
				return err
			}

			name = string(sval)
		}

		switch name {
		// t.PayloadCID (cid.Cid) (struct)
		case "PayloadCID":

			{

				c, err := cbg.ReadCid(br)
				if err != nil {
					return xerrors.Errorf("failed to read cid field t.PayloadCID: %w", err)
				}

				t.PayloadCID = c

			}
			// t.PayloadSize (int64) (int64)
		case "PayloadSize":
			{
				maj, extra, err := cbg.CborReadHeaderBuf(br, scratch)
				var extraI int64
				if err != nil {
					return err
				}
				switch maj {
				case cbg.MajUnsignedInt:
					extraI = int64(extra)
					if extraI < 0 {
						return fmt.Errorf("int64 positive overflow")
					}
				case cbg.MajNegativeInt:
					extraI = int64(extra)
					if extraI < 0 {
						return fmt.Errorf("int64 negative oveflow")
					}
					extraI = -1 - extraI
				default:
					return fmt.Errorf("wrong type for int64 field: %d", maj)
				}

				t.PayloadSize = int64(extraI)
			}
			// t.Keys (map[string]struct {}) (map)
		case "Keys":

			maj, extra, err = cbg.CborReadHeaderBuf(br, scratch)
			if err != nil {
				return err
			}
			if maj != cbg.MajMap {
				return fmt.Errorf("expected a map (major type 5)")
			}
			if extra > 4096 {
				return fmt.Errorf("t.Keys: map too large")
			}

			t.Keys = make(map[string]struct{}, extra)

			for i, l := 0, int(extra); i < l; i++ {

				var k string

				{
					sval, err := cbg.ReadStringBuf(br, scratch)
					if err != nil {
						return err
					}

					k = string(sval)
				}

				var v struct{}
				t.Keys[k] = v

			}
			// t.Freq (int64) (int64)
		case "Freq":
			{
				maj, extra, err := cbg.CborReadHeaderBuf(br, scratch)
				var extraI int64
				if err != nil {
					return err
				}
				switch maj {
				case cbg.MajUnsignedInt:
					extraI = int64(extra)
					if extraI < 0 {
						return fmt.Errorf("int64 positive overflow")
					}
				case cbg.MajNegativeInt:
					extraI = int64(extra)
					if extraI < 0 {
						return fmt.Errorf("int64 negative oveflow")
					}
					extraI = -1 - extraI
				default:
					return fmt.Errorf("wrong type for int64 field: %d", maj)
				}

				t.Freq = int64(extraI)
			}
			// t.BucketID (int64) (int64)
		case "BucketID":
			{
				maj, extra, err := cbg.CborReadHeaderBuf(br, scratch)
				var extraI int64
				if err != nil {
					return err
				}
				switch maj {
				case cbg.MajUnsignedInt:
					extraI = int64(extra)
					if extraI < 0 {
						return fmt.Errorf("int64 positive overflow")
					}
				case cbg.MajNegativeInt:
					extraI = int64(extra)
					if extraI < 0 {
						return fmt.Errorf("int64 negative oveflow")
					}
					extraI = -1 - extraI
				default:
					return fmt.Errorf("wrong type for int64 field: %d", maj)
				}

				t.BucketID = int64(extraI)
			}

		default:
			// Field doesn't exist on this type, so ignore it
			cbg.ScanForLinks(r, func(cid.Cid) {})
		}
	}

	return nil
}
