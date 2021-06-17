package exchange

import (
	"bytes"
	"context"
	"math/rand"
	"runtime"
	"testing"

	blocks "github.com/ipfs/go-block-format"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	dss "github.com/ipfs/go-datastore/sync"
	"github.com/ipfs/go-graphsync/ipldutil"
	"github.com/ipfs/go-graphsync/storeutil"
	blocksutil "github.com/ipfs/go-ipfs-blocksutil"
	"github.com/ipld/go-ipld-prime"
	"github.com/ipld/go-ipld-prime/codec/dagcbor"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	basicnode "github.com/ipld/go-ipld-prime/node/basic"
	sel "github.com/myelnet/pop/selectors"
	"github.com/stretchr/testify/require"
)

var blockGen = blocksutil.NewBlockGenerator()

func TestIndexLFU(t *testing.T) {
	ds := dss.MutexWrap(datastore.NewMapDatastore())

	idx, err := NewIndex(ds, WithBounds(512000, 500000))

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
	idx, err = NewIndex(ds, WithBounds(512000, 500000))
	require.NoError(t, err)

	// Add another read to ref2
	_, err = idx.GetRef(ref2.PayloadCID)
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

// This test verifies refs are moving correctly across buckets when incrementing reads and writes
func TestIndexRanking(t *testing.T) {
	ds := dss.MutexWrap(datastore.NewMapDatastore())

	idx, err := NewIndex(ds, WithBounds(512000, 500000))

	write := func() *DataRef {
		ref := &DataRef{
			PayloadCID:  blockGen.Next().Cid(),
			PayloadSize: 200,
		}
		require.NoError(t, idx.SetRef(ref))
		return ref
	}
	read := func(ref *DataRef) {
		_, err = idx.GetRef(ref.PayloadCID)
		require.NoError(t, err)

	}

	// t1
	ref1 := write()
	require.Equal(t, 1, idx.blist.Len())
	require.Equal(t, int64(0), ref1.Freq)
	// t2
	read(ref1)
	require.Equal(t, 1, idx.blist.Len())
	require.Equal(t, int64(1), ref1.Freq)
	// t3
	ref2 := write()
	require.Equal(t, 1, idx.blist.Len())
	// t4
	read(ref1)
	{
		// we should have 2 buckets
		require.Equal(t, 2, idx.blist.Len())
		// ref1 is in the last bucket
		last := idx.blist.Back()
		lb := last.Value.(*bucket)
		require.Equal(t, int64(3), lb.id)
		require.Equal(t, byte(1), lb.entries[ref1])
		// ref2 is in the first bucket
		first := idx.blist.Front()
		fb := first.Value.(*bucket)
		require.Equal(t, int64(2), fb.id)
		require.Equal(t, byte(1), fb.entries[ref2])
	}
	// t5
	read(ref2)
	{
		// All refs should be in the same bucket now
		require.Equal(t, 1, idx.blist.Len())
		only := idx.blist.Back()
		b := only.Value.(*bucket)
		require.Equal(t, int64(3), b.id)
	}
	// t6
	ref3 := write()
	{
		// All refs are still in the same bucket
		require.Equal(t, 1, idx.blist.Len())
		only := idx.blist.Back()
		b := only.Value.(*bucket)
		require.Equal(t, int64(3), b.id)
		require.Equal(t, 3, len(b.entries))
	}
	// t7
	read(ref1)
	{
		// New bucket is created
		require.Equal(t, 2, idx.blist.Len())
		last := idx.blist.Back()
		lb := last.Value.(*bucket)
		require.Equal(t, int64(4), lb.id)
		require.Equal(t, 1, len(lb.entries))
		first := idx.blist.Front()
		fb := first.Value.(*bucket)
		require.Equal(t, int64(3), fb.id)
		require.Equal(t, 2, len(fb.entries))
	}
	// t8
	read(ref3)
	{
		// ref3 is moved over to the last bucket
		require.Equal(t, 2, idx.blist.Len())
		last := idx.blist.Back()
		lb := last.Value.(*bucket)
		require.Equal(t, int64(4), lb.id)
		require.Equal(t, 2, len(lb.entries))
		first := idx.blist.Front()
		fb := first.Value.(*bucket)
		require.Equal(t, int64(3), fb.id)
		require.Equal(t, 1, len(fb.entries))
	}
	// t9
	write()
	{
		require.Equal(t, 2, idx.blist.Len())
		last := idx.blist.Back()
		lb := last.Value.(*bucket)
		require.Equal(t, int64(4), lb.id)
		require.Equal(t, 3, len(lb.entries))
	}
	// t10
	read(ref1)
	{
		require.Equal(t, 3, idx.blist.Len())
		last := idx.blist.Back()
		lb := last.Value.(*bucket)
		require.Equal(t, int64(5), lb.id)
		require.Equal(t, 1, len(lb.entries))
	}
	// t11
	read(ref3)
	{
		require.Equal(t, 3, idx.blist.Len())
		last := idx.blist.Back()
		lb := last.Value.(*bucket)
		require.Equal(t, int64(5), lb.id)
		require.Equal(t, 2, len(lb.entries))
	}
	// t12
	write()
	{
		require.Equal(t, 3, idx.blist.Len())
		last := idx.blist.Back()
		lb := last.Value.(*bucket)
		require.Equal(t, int64(5), lb.id)
		require.Equal(t, 3, len(lb.entries))
	}
	// t13
	read(ref3)
	{
		require.Equal(t, 4, idx.blist.Len())
		last := idx.blist.Back()
		lb := last.Value.(*bucket)
		require.Equal(t, int64(6), lb.id)
		require.Equal(t, 1, len(lb.entries))
	}
	// t14
	write()
	{
		require.Equal(t, 4, idx.blist.Len())
		last := idx.blist.Back()
		lb := last.Value.(*bucket)
		require.Equal(t, int64(6), lb.id)
		require.Equal(t, 2, len(lb.entries))
	}
}

func TestIndexDropRef(t *testing.T) {
	ds := dss.MutexWrap(datastore.NewMapDatastore())

	idx, err := NewIndex(ds)
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

func TestIndexUpdateRef(t *testing.T) {
	ds := dss.MutexWrap(datastore.NewMapDatastore())

	idx, err := NewIndex(ds)
	require.NoError(t, err)

	// create a ref with 1 Key (out of 2) & add it to the index
	c := blockGen.Next().Cid()
	ref := &DataRef{
		PayloadCID:  c,
		PayloadSize: 256000,
		Keys:        [][]byte{[]byte("data1")},
	}
	require.NoError(t, idx.SetRef(ref))

	// create a ref with the 2nd key & add it to the index
	ref2 := &DataRef{
		PayloadCID:  c,
		PayloadSize: 256000,
		Keys:        [][]byte{[]byte("data2")},
	}
	// must fail because a ref with the same cid already exists
	require.Error(t, ErrRefAlreadyExists, idx.SetRef(ref2))

	// only 1 key must be present for the cid : c
	ref, err = idx.GetRef(c)
	require.NoError(t, err)
	require.Equal(t, 1, len(ref.Keys))

	// to add the other key we need to use the updateRef method
	require.NoError(t, idx.UpdateRef(ref2))
	ref, err = idx.GetRef(c)
	require.NoError(t, err)

	// we should have the 2 keys data1 & data2
	require.Equal(t, 2, len(ref.Keys))
	require.Equal(t, "data1", string(ref.Keys[0]))
	require.Equal(t, "data2", string(ref.Keys[1]))
}

func TestIndexListRefs(t *testing.T) {
	ds := dss.MutexWrap(datastore.NewMapDatastore())

	idx, err := NewIndex(ds, WithBounds(1000, 900))

	var refs []*DataRef
	// this loop sets 100 refs for 24 bytes = 2400 bytes
	for i := 0; i < 103; i++ {
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

	count := len(list)
	// we only have room for 41 packets = 984
	require.Greater(t, count, 36)

	// set a ref that already exists
	require.Error(t, idx.SetRef(&DataRef{
		PayloadCID:  list[4].PayloadCID,
		PayloadSize: list[4].PayloadSize,
	}), ErrRefAlreadyExists)

	// length should be the same size
	list, err = idx.ListRefs()
	require.NoError(t, err)
	require.Equal(t, count, len(list))
}

func BenchmarkFlush(b *testing.B) {
	b.Run("SetRef", func(b *testing.B) {
		ds := dss.MutexWrap(datastore.NewMapDatastore())

		idx, err := NewIndex(ds, WithBounds(1000, 900))
		require.NoError(b, err)

		b.ReportAllocs()
		runtime.GC()

		for i := 0; i < b.N; i++ {
			cid := blockGen.Next().Cid()
			require.NoError(b, idx.SetRef(&DataRef{
				PayloadCID:  cid,
				PayloadSize: 100000,
				Freq:        3,
			}))
		}
	})
}

// This selector should query a HAMT without following the links
func TestIndexSelector(t *testing.T) {
	ds := dss.MutexWrap(datastore.NewMapDatastore())

	idx, err := NewIndex(ds)
	require.NoError(t, err)

	lb := cidlink.LinkBuilder{
		Prefix: cid.Prefix{
			Version:  1,
			Codec:    0x71, // dag-cbor as per multicodec
			MhType:   DefaultHashFunction,
			MhLength: -1,
		},
	}

	for i := 0; i < 10; i++ {
		nd := basicnode.NewInt(i)
		lnk, err := lb.Build(
			context.TODO(),
			ipld.LinkContext{},
			nd,
			storeutil.StorerForBlockstore(idx.Bstore()),
		)
		var buffer bytes.Buffer
		require.NoError(t, dagcbor.Encoder(nd, &buffer))
		require.NoError(t, err)
		blk, err := blocks.NewBlockWithCid(buffer.Bytes(), lnk.(cidlink.Link).Cid)
		require.NoError(t, err)
		require.NoError(t, idx.Bstore().Put(blk))

		require.NoError(t, idx.SetRef(&DataRef{
			PayloadCID:  blk.Cid(),
			PayloadSize: 24,
			Freq:        3,
		}))
	}

	link := cidlink.Link{Cid: idx.Root()}
	traverser := ipldutil.TraversalBuilder{
		Root:     link,
		Selector: sel.Hamt(),
	}.Start(context.Background())

	complete := false
	for !complete {
		var err error
		complete, err = traverser.IsComplete()
		require.NoError(t, err)
		lnk, _ := traverser.CurrentRequest()
		l, ok := lnk.(cidlink.Link)
		if !ok {
			continue
		}
		key := l.Cid
		blk, err := idx.Bstore().Get(key)
		require.NoError(t, err)
		err = traverser.Advance(bytes.NewBuffer(blk.RawData()))
		require.NoError(t, err)
	}
}

func TestIndexInterest(t *testing.T) {
	newIndex := func(n int) *Index {
		ds := dss.MutexWrap(datastore.NewMapDatastore())

		idx, err := NewIndex(ds, WithBounds(1000, 900))
		require.NoError(t, err)

		var refs []*DataRef
		// this loop sets 100 refs for 24 bytes = 2400 bytes
		for i := 0; i < n; i++ {
			ref := &DataRef{
				PayloadCID:  blockGen.Next().Cid(),
				PayloadSize: 24,
			}
			require.NoError(t, idx.SetRef(ref))
			refs = append(refs, ref)

			// randomly add a read after every write, err doesn't matter
			_, _ = idx.GetRef(refs[rand.Intn(i+1)].PayloadCID)
		}
		return idx
	}
	// Our main index
	idx := newIndex(100)

	// A new index we receive
	idx1 := newIndex(50)
	require.NoError(t, idx.LoadInterest(idx1.Root(), idx1.store))

	// Another index received
	idx2 := newIndex(101)
	require.NoError(t, idx.LoadInterest(idx2.Root(), idx2.store))
}

func TestLoadInterest(t *testing.T) {
	newIndex := func() *Index {
		ds := dss.MutexWrap(datastore.NewMapDatastore())

		idx, err := NewIndex(ds, WithBounds(1000, 900))
		require.NoError(t, err)
		return idx
	}

	idx1 := newIndex()
	ref1 := &DataRef{
		PayloadCID:  blockGen.Next().Cid(),
		PayloadSize: 100,
	}
	require.NoError(t, idx1.SetRef(ref1))
	_, err := idx1.GetRef(ref1.PayloadCID)
	require.NoError(t, err)

	idx2 := newIndex()
	ref2 := &DataRef{
		PayloadCID:  blockGen.Next().Cid(),
		PayloadSize: 100,
	}
	require.NoError(t, idx2.SetRef(ref2))

	ref1b := &DataRef{
		PayloadCID:  ref1.PayloadCID,
		PayloadSize: 100,
	}
	require.NoError(t, idx2.SetRef(ref1b))
	_, err = idx2.GetRef(ref1.PayloadCID)
	require.NoError(t, err)

	idx := newIndex()
	require.NoError(t, idx.LoadInterest(idx1.Root(), idx1.store))
	require.NoError(t, idx.LoadInterest(idx2.Root(), idx2.store))

	// We should have a 2 refs in the interest
	require.Equal(t, 2, idx.InterestLen())

	// It returns all of them since we have space
	in, err := idx.Interesting()
	require.NoError(t, err)
	require.Equal(t, 2, len(in))

	// Another index has 4 refs!
	idx3 := newIndex()
	var reflist []*DataRef
	for i := 0; i < 4; i++ {
		reflist = append(reflist, &DataRef{
			PayloadCID:  blockGen.Next().Cid(),
			PayloadSize: 200,
		})
		require.NoError(t, idx3.SetRef(reflist[i]))
		// randomly add a read after every write, err doesn't matter
		_, _ = idx3.GetRef(reflist[rand.Intn(i+1)].PayloadCID)
	}
	require.NoError(t, idx3.SetRef(&DataRef{
		PayloadCID:  ref1.PayloadCID,
		PayloadSize: 100,
	}))
	require.NoError(t, idx3.SetRef(&DataRef{
		PayloadCID:  ref2.PayloadCID,
		PayloadSize: 100,
	}))
	// Load them up in the index
	require.NoError(t, idx.LoadInterest(idx3.Root(), idx3.store))

	// Should have 6 refs in there
	require.Equal(t, 6, idx.InterestLen())
	in, err = idx.Interesting()
	require.NoError(t, err)
	require.Equal(t, 6, len(in))

	// Let's load them all in there
	allrefs := append([]*DataRef{ref1, ref2}, reflist...)
	for i, ref := range allrefs {
		// Def will mess up the idx3 list but we don't need it anymore
		ref.bucketNode = nil
		require.NoError(t, idx.SetRef(ref))
		require.NoError(t, idx.DropInterest(ref.PayloadCID))

		// randomly add a read after every write, err doesn't matter
		_, _ = idx.GetRef(allrefs[rand.Intn(i+1)].PayloadCID)
	}
	// We should have 0 interest now
	require.Equal(t, 0, idx.InterestLen())
	_, err = idx.Interesting()
	require.Error(t, err)

	// Create a new index
	idx4 := newIndex()
	var reflist2 []*DataRef
	for i := 0; i < 10; i++ {
		reflist2 = append(reflist2, &DataRef{
			PayloadCID:  blockGen.Next().Cid(),
			PayloadSize: 100,
		})
		require.NoError(t, idx4.SetRef(reflist2[i]))
		// randomly add a read after every write, err doesn't matter
		_, _ = idx4.GetRef(reflist2[rand.Intn(i+1)].PayloadCID)
	}
	// Make an outlier
	for i := 0; i < 10; i++ {
		_, _ = idx4.GetRef(reflist2[0].PayloadCID)
	}

	// Load it again in the interest
	require.NoError(t, idx.LoadInterest(idx4.Root(), idx4.store))
	// 10 interests
	require.Equal(t, 10, idx.InterestLen())

	// is the index suggesting some updates
	in, err = idx.Interesting()
	require.NoError(t, err)
	require.Equal(t, 1, len(in))

	// the outlier should be the most interesting
	for k := range in {
		require.Equal(t, reflist2[0].PayloadCID, k.PayloadCID)
	}
}
