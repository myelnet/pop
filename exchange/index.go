package exchange

import (
	"bytes"
	"container/list"
	"context"
	"errors"
	"sync"

	"github.com/filecoin-project/go-hamt-ipld/v3"
	"github.com/filecoin-project/go-multistore"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	cbor "github.com/ipfs/go-ipld-cbor"
	cbg "github.com/whyrusleeping/cbor-gen"
)

//go:generate cbor-gen-for --map-encoding DataRef

// ErrRefNotFound is returned when a given ref is not in the store
var ErrRefNotFound = errors.New("ref not found")

// KIndex is the datastore key for persisting the index of a workdag
const KIndex = "idx"

// Index contains the information about which objects are currently stored
// the key is a CID.String().
// It also implements a Least Frequently Used cache eviction mechanism to maintain storage withing given
// bounds based on https://github.com/dgrijalva/lfu-go.
// Content is garbage collected during eviction.
type Index struct {
	ms      *multistore.MultiStore
	ds      datastore.Batching
	root    *hamt.Node
	store   cbor.IpldStore
	rootCID cid.Cid
	// Upper bound is the store usage amount afyer which we start evicting refs from the store
	ub uint64
	// Lower bound is the size we target when evicting to make room for new content
	// the interval between ub and lb is to try not evicting after every write once we reach ub
	lb uint64

	mu sync.Mutex
	// current size of content committed to the store
	size uint64
	// linked list keeps track of our frequencies to access as fast as possible
	freqs *list.List
	// We still need to keep a map in memory
	Refs map[string]*DataRef
}

// DataRef encapsulates information about a content committed for storage
type DataRef struct {
	PayloadCID  cid.Cid
	PayloadSize int64
	StoreID     multistore.StoreID
	Freq        int64
	// do not serialize
	freqNode *list.Element
}

// IndexOption customizes the behavior of the index
type IndexOption func(*Index)

// WithBounds sets the upper and lower bounds of the LFU store
func WithBounds(up, lo uint64) IndexOption {
	return func(idx *Index) {
		// Should crash execution rather than running the index with sneaky bugs
		if up < lo {
			panic("upper bound cannot be lower than lower bound")
		}
		idx.ub = up
		idx.lb = lo
	}
}

// NewIndex creates a new Index instance, loading entries into a doubly linked list for faster read and writes
func NewIndex(ds datastore.Batching, ms *multistore.MultiStore, opts ...IndexOption) (*Index, error) {
	idx := &Index{
		freqs:   list.New(),
		ds:      namespace.Wrap(ds, datastore.NewKey("/index")),
		ms:      ms,
		Refs:    make(map[string]*DataRef),
		rootCID: cid.Undef,
	}
	for _, o := range opts {
		o(idx)
	}

	idx.store = cbor.NewCborStore(blockstore.NewBlockstore(idx.ds))
	if err := idx.loadFromStore(); err != nil {
		return nil, err
	}

	// // Loads the ref frequencies in a doubly linked list for faster access
	err := idx.root.ForEach(context.TODO(), func(k string, val *cbg.Deferred) error {
		v := new(DataRef)
		if err := v.UnmarshalCBOR(bytes.NewReader(val.Raw)); err != nil {
			return err
		}
		idx.Refs[v.PayloadCID.String()] = v
		idx.size += uint64(v.PayloadSize)
		if e := idx.freqs.Front(); e == nil {
			// insert the first element in the list
			li := newListEntry(v.Freq)
			li.entries[v] = 1
			v.freqNode = idx.freqs.PushFront(li)
			return nil
		}
		for e := idx.freqs.Front(); e != nil; e = e.Next() {
			le := e.Value.(*listEntry)
			if le.freq == v.Freq {
				le.entries[v] = 1
				v.freqNode = e
				return nil
			}
			if le.freq > v.Freq {
				li := newListEntry(v.Freq)
				li.entries[v] = 1
				v.freqNode = idx.freqs.InsertBefore(li, e)
				return nil
			}
		}
		// if we're still here it means we're the highest frequency in the list so we
		// insert it at the back
		li := newListEntry(v.Freq)
		li.entries[v] = 1
		v.freqNode = idx.freqs.PushBack(li)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return idx, nil
}

func (idx *Index) loadFromStore() error {
	// var err error
	enc, err := idx.ds.Get(datastore.NewKey(KIndex))
	if err != nil && errors.Is(err, datastore.ErrNotFound) {
		nd, err := hamt.NewNode(idx.store, hamt.UseTreeBitWidth(5))
		if err != nil {
			return err
		}
		idx.root = nd
	} else if err != nil {
		return err
	}
	if err == nil {
		r, err := cid.Cast(enc)
		if err != nil {
			return err
		}
		idx.root, err = hamt.LoadNode(context.TODO(), idx.store, r, hamt.UseTreeBitWidth(5))
		if err != nil {
			return err
		}
		idx.rootCID = r
	}
	return nil
}

// GetStoreID returns the StoreID of the store which has the given content
func (idx *Index) GetStoreID(id cid.Cid) (multistore.StoreID, error) {
	ref, err := idx.GetRef(id)
	if err != nil {
		return 0, err
	}
	return ref.StoreID, nil
}

// GetStore returns the store associated with a data CID
func (idx *Index) GetStore(id cid.Cid) (*multistore.Store, error) {
	storeID, err := idx.GetStoreID(id)
	if err != nil {
		return nil, err
	}
	return idx.ms.Get(storeID)
}

// Root returns the HAMT root CID
func (idx *Index) Root() cid.Cid {
	return idx.rootCID
}

// Flush persists the Refs to the store
func (idx *Index) Flush() error {
	if err := idx.root.Flush(context.TODO()); err != nil {
		return err
	}
	r, err := idx.store.Put(context.TODO(), idx.root)
	if err != nil {
		return err
	}
	idx.rootCID = r
	return idx.ds.Put(datastore.NewKey(KIndex), r.Bytes())
}

// DropRef removes all content linked to a root CID and associated Refs
func (idx *Index) DropRef(k cid.Cid) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if found, err := idx.root.Delete(context.TODO(), k.String()); err != nil {
		return err
	} else if !found {
		return ErrRefNotFound
	}
	ref := idx.Refs[k.String()]
	idx.remEntry(ref.freqNode, ref)

	err := idx.ms.Delete(ref.StoreID)
	if err != nil {
		return err
	}

	delete(idx.Refs, k.String())
	return idx.Flush()
}

// SetRef adds a ref in the index and increments the LFU queue
func (idx *Index) SetRef(ref *DataRef) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	k := ref.PayloadCID.String()
	idx.Refs[k] = ref
	idx.size += uint64(ref.PayloadSize)
	if idx.ub > 0 && idx.lb > 0 {
		if idx.size > idx.ub {
			idx.evict(idx.size - idx.lb)
		}
	}
	// We evict the item before adding the new one
	idx.increment(ref)
	if err := idx.root.Set(context.TODO(), k, ref); err != nil {
		return err
	}
	return idx.Flush()
}

// GetRef gets a ref in the index for a given root CID and increments the LFU list registering a Read
func (idx *Index) GetRef(k cid.Cid) (*DataRef, error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	ref, ok := idx.Refs[k.String()]
	if !ok {
		return nil, ErrRefNotFound
	}
	idx.increment(ref)
	// Update the freq
	if err := idx.root.Set(context.TODO(), k.String(), ref); err != nil {
		return nil, err
	}
	return ref, idx.Flush()
}

// PeekRef returns a ref from the index without actually registering a read in the LFU
func (idx *Index) PeekRef(k cid.Cid) (*DataRef, error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	ref := new(DataRef)
	ref, ok := idx.Refs[k.String()]
	if !ok {
		return nil, ErrRefNotFound
	}
	return ref, nil
}

// ListRefs returns all the content refs currently stored on this node as well as their read frequencies
func (idx *Index) ListRefs() ([]*DataRef, error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	refs := make([]*DataRef, len(idx.Refs))
	i := 0
	for e := idx.freqs.Front(); e != nil; e = e.Next() {
		for k := range e.Value.(*listEntry).entries {
			refs[i] = k
			i++
		}
	}
	return refs, nil
}

// Len returns the number of roots this index is currently storing
func (idx *Index) Len() int {
	return len(idx.Refs)
}

type listEntry struct {
	entries map[*DataRef]byte
	freq    int64
}

func newListEntry(freq int64) *listEntry {
	return &listEntry{
		entries: make(map[*DataRef]byte),
		freq:    freq,
	}
}

func (idx *Index) increment(ref *DataRef) {
	currentPlace := ref.freqNode
	var nextFreq int64
	var nextPlace *list.Element
	if currentPlace == nil {
		// new entry
		nextFreq = 1
		nextPlace = idx.freqs.Front()
	} else {
		// move up
		nextFreq = currentPlace.Value.(*listEntry).freq + 1
		nextPlace = currentPlace.Next()
	}

	if nextPlace == nil || nextPlace.Value.(*listEntry).freq != nextFreq {
		// create a new list entry
		li := &listEntry{
			freq:    nextFreq,
			entries: make(map[*DataRef]byte),
		}
		if currentPlace != nil {
			nextPlace = idx.freqs.InsertAfter(li, currentPlace)
		} else {
			nextPlace = idx.freqs.PushFront(li)
		}
	}
	ref.Freq = nextFreq
	ref.freqNode = nextPlace
	nextPlace.Value.(*listEntry).entries[ref] = 1
	if currentPlace != nil {
		// remove from current position
		idx.remEntry(currentPlace, ref)
	}
}

func (idx *Index) remEntry(place *list.Element, entry *DataRef) {
	le := place.Value.(*listEntry)
	entries := le.entries
	delete(entries, entry)
	if len(entries) == 0 {
		idx.freqs.Remove(place)
	}
}

func (idx *Index) evict(size uint64) uint64 {
	// No lock here so it can be called
	// from within the lock (during Set)
	var evicted uint64
	for place := idx.freqs.Front(); place != nil; place = place.Next() {
		for entry := range place.Value.(*listEntry).entries {
			delete(idx.Refs, entry.PayloadCID.String())

			err := idx.ms.Delete(entry.StoreID)
			if err != nil {
				continue
			}

			idx.remEntry(place, entry)
			evicted += uint64(entry.PayloadSize)
			idx.size -= uint64(entry.PayloadSize)
			if evicted >= size {
				return evicted
			}
		}
	}
	return evicted
}
