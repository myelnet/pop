package exchange

import (
	"container/list"
	"encoding/json"
	"errors"
	"sync"

	"github.com/filecoin-project/go-multistore"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
)

//go:generate cbor-gen-for DataRef RefMap

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
	ms *multistore.MultiStore
	ds datastore.Batching
	// Upper bound is the store usage amount afyer which we start evicting refs from the store
	ub uint64
	// Lower bound is the size we target when evicting to make room for new content
	// the interval between ub and lb is to try not evicting after every write once we reach ub
	lb uint64

	// Mutex is exported for when dealing directly with the Refs without incrementing frequencies
	Mu sync.Mutex
	// current size of content committed to the store
	size uint64
	// linked list keeps track of our frequencies to access as fast as possible
	freqs *list.List
	// RefMap wraps the Refs
	RefMap
}

// RefMap is disctionary indexing all the content stored in the Index
type RefMap struct {
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
		freqs: list.New(),
		ds:    namespace.Wrap(ds, datastore.NewKey("/index")),
		ms:    ms,
	}
	for _, o := range opts {
		o(idx)
	}

	enc, err := idx.ds.Get(datastore.NewKey(KIndex))
	if err != nil && errors.Is(err, datastore.ErrNotFound) {
		idx.Refs = make(map[string]*DataRef)
	} else if err != nil {
		return nil, err
	}
	if err == nil {
		var refs map[string]*DataRef
		if err := json.Unmarshal(enc, &refs); err != nil {
			return nil, err
		}
		idx.Refs = refs
	}

	// Loads the ref frequencies in a doubly linked list for faster access
refs:
	for _, v := range idx.Refs {
		idx.size += uint64(v.PayloadSize)
		if e := idx.freqs.Front(); e == nil {
			// insert the first element in the list
			li := newListEntry(v.Freq)
			li.entries[v] = 1
			v.freqNode = idx.freqs.PushFront(li)
			continue refs
		}
		for e := idx.freqs.Front(); e != nil; e = e.Next() {
			le := e.Value.(*listEntry)
			if le.freq == v.Freq {
				le.entries[v] = 1
				v.freqNode = e
				continue refs
			}
			if le.freq > v.Freq {
				li := newListEntry(v.Freq)
				li.entries[v] = 1
				v.freqNode = idx.freqs.InsertBefore(li, e)
				continue refs
			}
		}
		// if we're still here it means we're the highest frequency in the list so we
		// insert it at the back
		li := newListEntry(v.Freq)
		li.entries[v] = 1
		v.freqNode = idx.freqs.PushBack(li)
	}

	return idx, nil
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

// Flush persists the Refs to the store
func (idx *Index) Flush() error {
	enc, err := json.Marshal(idx.Refs)
	if err != nil {
		return err
	}

	return idx.ds.Put(datastore.NewKey(KIndex), enc)
}

// DropRef removes all content linked to a root CID and associated Refs
func (idx *Index) DropRef(k cid.Cid) error {
	idx.Mu.Lock()
	defer idx.Mu.Unlock()
	if ref, ok := idx.Refs[k.String()]; ok {
		idx.remEntry(ref.freqNode, ref)

		err := idx.ms.Delete(ref.StoreID)
		if err != nil {
			return err
		}

		delete(idx.Refs, k.String())
		return idx.Flush()
	}
	return ErrRefNotFound
}

// SetRef adds a ref in the index and increments the LFU queue
func (idx *Index) SetRef(ref *DataRef) error {
	idx.Mu.Lock()
	defer idx.Mu.Unlock()
	idx.Refs[ref.PayloadCID.String()] = ref
	idx.size += uint64(ref.PayloadSize)
	if idx.ub > 0 && idx.lb > 0 {
		if idx.size > idx.ub {
			idx.evict(idx.size - idx.lb)
		}
	}
	// We evict the item before adding the new one
	idx.increment(ref)
	return idx.Flush()
}

// GetRef gets a ref in the index for a given root CID and increments the LFU list registering a Read
func (idx *Index) GetRef(k cid.Cid) (*DataRef, error) {
	idx.Mu.Lock()
	defer idx.Mu.Unlock()
	ref, ok := idx.Refs[k.String()]
	if !ok {
		return nil, ErrRefNotFound
	}
	idx.increment(ref)
	return ref, idx.Flush()
}

// PeekRef returns a ref from the index without actually registering a read in the LFU
func (idx *Index) PeekRef(k cid.Cid) (*DataRef, error) {
	idx.Mu.Lock()
	defer idx.Mu.Unlock()
	ref, ok := idx.Refs[k.String()]
	if !ok {
		return nil, ErrRefNotFound
	}
	return ref, nil
}

// ListRefs returns all the content refs currently stored on this node as well as their read frequencies
func (idx *Index) ListRefs() ([]*DataRef, error) {
	idx.Mu.Lock()
	defer idx.Mu.Unlock()
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
