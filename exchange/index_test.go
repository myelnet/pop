package exchange

import (
	"math/rand"
	"testing"

	"github.com/filecoin-project/go-multistore"
	"github.com/ipfs/go-datastore"
	dss "github.com/ipfs/go-datastore/sync"
	blocksutil "github.com/ipfs/go-ipfs-blocksutil"
	"github.com/stretchr/testify/require"
)

var blockGen = blocksutil.NewBlockGenerator()

func TestIndexLFU(t *testing.T) {
	ds := dss.MutexWrap(datastore.NewMapDatastore())
	ms, err := multistore.NewMultiDstore(ds)
	require.NoError(t, err)

	idx, err := NewIndex(ds, ms, WithBounds(512000, 500000))

	ref1 := &DataRef{
		PayloadCID:  blockGen.Next().Cid(),
		PayloadSize: 256000,
	}
	require.NoError(t, idx.SetRef(ref1))

	ref2 := &DataRef{
		PayloadCID:  blockGen.Next().Cid(),
		PayloadSize: 110000,
	}
	require.NoError(t, idx.SetRef(ref2))

	// Adding some reads
	_, err = idx.GetRef(ref2.PayloadCID)
	_, err = idx.GetRef(ref2.PayloadCID)

	ref3 := &DataRef{
		PayloadCID:  blockGen.Next().Cid(),
		PayloadSize: 356000,
	}
	require.NoError(t, idx.SetRef(ref3))

	// Now our first ref should be evicted
	_, err = idx.GetRef(ref1.PayloadCID)
	require.Error(t, err)

	// But our second ref should still be around
	_, err = idx.GetRef(ref2.PayloadCID)
	require.NoError(t, err)

	// Test reinitializing the list from the stored frequencies
	idx, err = NewIndex(ds, ms, WithBounds(512000, 500000))
	require.NoError(t, err)

	ref4 := &DataRef{
		PayloadCID:  blockGen.Next().Cid(),
		PayloadSize: 20000,
	}
	require.NoError(t, idx.SetRef(ref4))

	ref5 := &DataRef{
		PayloadCID:  blockGen.Next().Cid(),
		PayloadSize: 60000,
	}
	require.NoError(t, idx.SetRef(ref5))

	// ref2 should still be around
	_, err = idx.GetRef(ref2.PayloadCID)
	require.NoError(t, err)

	// ref3 is gone
	_, err = idx.GetRef(ref3.PayloadCID)
	require.Error(t, err)
}

func TestIndexDropRef(t *testing.T) {
	ds := dss.MutexWrap(datastore.NewMapDatastore())
	ms, err := multistore.NewMultiDstore(ds)
	require.NoError(t, err)

	idx, err := NewIndex(ds, ms)
	require.NoError(t, err)

	ref := &DataRef{
		PayloadCID:  blockGen.Next().Cid(),
		PayloadSize: 256000,
	}
	require.NoError(t, idx.SetRef(ref))

	err = idx.DropRef(ref.PayloadCID)
	require.NoError(t, err)

	_, err = idx.GetRef(ref.PayloadCID)
	require.Error(t, err)
}

func TestIndexListRefs(t *testing.T) {
	ds := dss.MutexWrap(datastore.NewMapDatastore())
	ms, err := multistore.NewMultiDstore(ds)
	require.NoError(t, err)

	idx, err := NewIndex(ds, ms, WithBounds(1000, 900))

	var refs []*DataRef
	// this loop sets 100 refs for 24 bytes = 2400 bytes
	for i := 0; i < 101; i++ {
		ref := &DataRef{
			PayloadCID:  blockGen.Next().Cid(),
			PayloadSize: 24,
		}
		require.NoError(t, idx.SetRef(ref))
		refs = append(refs, ref)

		// randomly add a read after every write
		_, err = idx.GetRef(refs[rand.Intn(len(refs))].PayloadCID)
	}

	list, err := idx.ListRefs()
	require.NoError(t, err)
	// we only have room for 41 packets = 984
	require.Equal(t, 41, len(list))
}
