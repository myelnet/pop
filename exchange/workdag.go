package exchange

import (
	"bytes"
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"text/tabwriter"

	"github.com/filecoin-project/go-multistore"
	cid "github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
	chunk "github.com/ipfs/go-ipfs-chunker"
	files "github.com/ipfs/go-ipfs-files"
	ipldformat "github.com/ipfs/go-ipld-format"
	"github.com/ipfs/go-merkledag"
	unixfile "github.com/ipfs/go-unixfs/file"
	"github.com/ipfs/go-unixfs/importer/balanced"
	"github.com/ipfs/go-unixfs/importer/helpers"
	"github.com/ipld/go-ipld-prime"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	basicnode "github.com/ipld/go-ipld-prime/node/basic"
	mh "github.com/multiformats/go-multihash"
	"github.com/myelnet/pop/filecoin"
)

var (
	// ErrEntryNotFound is returned by Index.Entry, if an entry is not found.
	ErrEntryNotFound = errors.New("entry not found")
	// ErrRefNotFound is returned when looking for a ref that doesn't exist
	ErrRefNotFound = errors.New("ref not found")
)

const (
	// KStoreID is datastore key for persisting the last ID of a store for the current workdag
	KStoreID = "storeid"

	// KIndex is the datastore key for persisting the index of a workdag
	KIndex = "index"
)

// DefaultHashFunction used for generating CIDs of imported data
// although less convenient than SHA2, BLAKE2B seems to be more peformant in most cases
const DefaultHashFunction = uint64(mh.BLAKE2B_MIN + 31)

// Workdag manages our DAG operations. It stores a whole index of any content stored by the node.
// It also implements a Least Frequently Used cache eviction mechanism to maintain storage withing given
// bounds.
type Workdag struct {
	storeID multistore.StoreID
	store   *multistore.Store
	ms      *multistore.MultiStore
	ds      datastore.Batching
	// Upper bound is the store usage amount afyer which we start evicting refs from the store
	ub uint64
	// Lower bound is the size we target when evicting to make room for new content
	// the interval between ub and lb is to try not evicting after every write once we reach ub
	lb uint64
	// current size of content committed to the store
	size uint64
	// linked list keeps track of our frequencies as fast as possible
	freqs *list.List

	// cache a mutex protected version of the index in memory
	imu sync.Mutex
	idx *Index
}

// WorkdagOption customizes the behavior of the workdag directly
type WorkdagOption func(*Workdag)

// WithBounds sets the upper and lower bounds of the LFU store
func WithBounds(up, lo uint64) WorkdagOption {
	return func(wd *Workdag) {
		wd.ub = up
		wd.lb = lo
	}
}

// NewWorkdag instanciates a workdag, checks if we have a store ID and loads the right store
func NewWorkdag(ms *multistore.MultiStore, ds datastore.Batching, opts ...WorkdagOption) (*Workdag, error) {
	w := &Workdag{
		ds:    namespace.Wrap(ds, datastore.NewKey("/workdag")),
		ms:    ms,
		freqs: list.New(),
	}
	for _, o := range opts {
		o(w)
	}
	idx, err := w.Index()
	if err != nil && errors.Is(err, datastore.ErrNotFound) {
		idx = &Index{
			StoreID: ms.Next(),
			Refs:    make(map[string]*DataRef),
		}
	} else if err != nil {
		return nil, err
	}
	w.storeID = idx.StoreID
	w.store, err = ms.Get(idx.StoreID)
	if err != nil {
		return nil, err
	}
refs:
	for _, v := range idx.Refs {
		w.size += uint64(v.PayloadSize)
		if e := w.freqs.Front(); e == nil {
			// insert the first element in the list
			li := newListEntry(v.Freq)
			li.entries[v] = 1
			v.freqNode = w.freqs.PushFront(li)
			continue refs
		}
		for e := w.freqs.Front(); e != nil; e = e.Next() {
			le := e.Value.(*listEntry)
			if le.freq == v.Freq {
				le.entries[v] = 1
				v.freqNode = e
				continue refs
			}
			if le.freq > v.Freq {
				li := newListEntry(v.Freq)
				li.entries[v] = 1
				v.freqNode = w.freqs.InsertBefore(li, e)
				continue refs
			}
		}
		// if we're still here it means we're the highest frequency in the list so we
		// insert it at the back
		li := newListEntry(v.Freq)
		li.entries[v] = 1
		v.freqNode = w.freqs.PushBack(li)
	}

	return w, w.SetIndex(idx)
}

// Store exposes the underlying store
func (w *Workdag) Store() *multistore.Store {
	return w.store
}

// StoreID exposes the ID of the underlying store
func (w *Workdag) StoreID() multistore.StoreID {
	return w.storeID
}

// SetIndex updates the Workdag index after an operation
func (w *Workdag) SetIndex(idx *Index) error {
	w.imu.Lock()
	w.idx = idx
	w.imu.Unlock()

	enc, err := json.Marshal(idx)
	if err != nil {
		return err
	}

	return w.ds.Put(datastore.NewKey(KIndex), enc)
}

// Index decodes and returns the workdag index from the datastore
func (w *Workdag) Index() (*Index, error) {
	w.imu.Lock()
	if w.idx != nil {
		defer w.imu.Unlock()
		return w.idx, nil
	}
	w.imu.Unlock()
	enc, err := w.ds.Get(datastore.NewKey(KIndex))
	if err != nil {
		return nil, err
	}
	var idx Index
	if err := json.Unmarshal(enc, &idx); err != nil {
		return nil, err
	}
	// Sort entries to make sure our commit CID will be deterministic
	sort.Slice(idx.Entries, func(i, j int) bool {
		return idx.Entries[i].Cid.String() > idx.Entries[j].Cid.String()
	})

	return &idx, nil
}

// AddOptions describes how an add operation should be performed
type AddOptions struct {
	// Path is the exact filepath to a the file or directory to be added.
	Path string
	// ChunkSize is size by which to chunk the content when adding a file.
	ChunkSize int64
}

// Add adds the file contents of a file in the workdag
func (w *Workdag) Add(ctx context.Context, opts AddOptions) (cid.Cid, error) {
	link, err := w.doAdd(ctx, opts)
	if err != nil {
		return cid.Undef, err
	}
	root := link.(cidlink.Link).Cid
	return root, nil
}

func (w *Workdag) doAdd(ctx context.Context, opts AddOptions) (ipld.Link, error) {
	st, err := os.Stat(opts.Path)
	if err != nil {
		return nil, err
	}
	file, err := files.NewSerialFile(opts.Path, false, st)
	if err != nil {
		return nil, err
	}

	switch f := file.(type) {
	case files.Directory:
		return w.doAddDir(ctx, f, opts)
	case files.File:
		return w.doAddFile(ctx, f, opts)
	default:
		return nil, fmt.Errorf("unknown file type")
	}
}

func (w *Workdag) doAddFile(ctx context.Context, f files.File, opts AddOptions) (ipld.Link, error) {
	bufferedDS := ipldformat.NewBufferedDAG(ctx, w.store.DAG)

	prefix, err := merkledag.PrefixForCidVersion(1)
	if err != nil {
		return nil, err
	}
	prefix.MhType = DefaultHashFunction

	params := helpers.DagBuilderParams{
		Maxlinks:   1024,
		RawLeaves:  true,
		CidBuilder: prefix,
		Dagserv:    bufferedDS,
	}

	db, err := params.New(chunk.NewSizeSplitter(f, opts.ChunkSize))
	if err != nil {
		return nil, err
	}

	n, err := balanced.Layout(db)
	if err != nil {
		return nil, err
	}

	err = bufferedDS.Commit()
	if err != nil {
		return nil, err
	}
	idx, err := w.Index()
	if err != nil {
		return nil, err
	}
	// Only keep the file name
	_, name := filepath.Split(opts.Path)

	e, err := idx.Entry(name)
	if errors.Is(err, ErrEntryNotFound) {
		e = idx.Add(name)
	} else if err != nil {
		return nil, err
	}
	e.Cid = n.Cid()
	e.Size, err = f.Size()
	if err != nil {
		return nil, err
	}

	return cidlink.Link{Cid: n.Cid()}, w.SetIndex(idx)

}

func (w *Workdag) doAddDir(ctx context.Context, dir files.Directory, opts AddOptions) (ipld.Link, error) {
	return nil, fmt.Errorf("TODO")
}

// Status represents our staged files
type Status []*Entry

func (s Status) String() string {
	buf := bytes.NewBuffer(nil)
	// Format everything in a balanced table layout
	// we might want to move this with the cli
	w := new(tabwriter.Writer)
	w.Init(buf, 0, 4, 2, ' ', 0)
	var total int64 = 0
	for _, e := range s {
		fmt.Fprintf(
			w,
			"%s\t%s\t%s\n",
			e.Name,
			e.Cid,
			filecoin.SizeStr(filecoin.NewInt(uint64(e.Size))),
		)
		total += e.Size
	}
	if total > 0 {
		fmt.Fprintf(w, "Total\t-\t%s\n", filecoin.SizeStr(filecoin.NewInt(uint64(total))))
	}
	w.Flush()
	return buf.String()
}

// Status returns a list of the current entries
func (w *Workdag) Status() (Status, error) {
	idx, err := w.Index()
	if err != nil {
		return nil, err
	}
	return Status(idx.Entries), nil
}

// CommitOptions might be useful later to add authorship
type CommitOptions struct {
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

// assemble all the entries into a single dag Node
func (w *Workdag) assembleEntries(entries []*Entry) (ipld.Node, error) {
	// We need a single root CID so we make a list with the roots of all dagpb roots
	nb := basicnode.Prototype.List.NewBuilder()
	as, err := nb.BeginList(int64(len(entries)))
	if err != nil {
		return nil, err
	}

	for _, e := range entries {
		// Each entry is a map with 2 keys: Name and Link
		mas, err := as.AssembleValue().BeginMap(2)
		if err != nil {
			return nil, err
		}
		nas, err := mas.AssembleEntry("Name")
		if err != nil {
			return nil, err
		}
		err = nas.AssignString(e.Name)
		if err != nil {
			return nil, err
		}
		las, err := mas.AssembleEntry("Link")
		if err != nil {
			return nil, err
		}
		clk := cidlink.Link{Cid: e.Cid}
		err = las.AssignLink(clk)
		if err != nil {
			return nil, err
		}
		err = mas.Finish()
		if err != nil {
			return nil, err
		}
	}
	err = as.Finish()
	if err != nil {
		return nil, err
	}
	return nb.Build(), nil
}

// Commit stores the current contents of the index in an array to yield a single root CID
func (w *Workdag) Commit(ctx context.Context, opts CommitOptions) (*DataRef, error) {
	lb := cidlink.LinkBuilder{
		Prefix: cid.Prefix{
			Version:  1,
			Codec:    0x71, // dag-cbor as per multicodec
			MhType:   DefaultHashFunction,
			MhLength: -1,
		},
	}

	idx, err := w.Index()
	if err != nil {
		return nil, err
	}

	if len(idx.Entries) == 0 {
		return nil, errors.New("workdag clean, nothing to commit")
	}

	var size int64
	for _, e := range idx.Entries {
		size += e.Size
	}

	nd, err := w.assembleEntries(idx.Entries)
	if err != nil {
		return nil, err
	}
	lnk, err := lb.Build(
		ctx,
		ipld.LinkContext{},
		nd,
		w.store.Storer,
	)
	if err != nil {
		return nil, err
	}
	c := lnk.(cidlink.Link)

	ref := &DataRef{
		PayloadCID:  c.Cid,
		PayloadSize: size,
		StoreID:     w.storeID,
	}
	if err := w.SetRef(c.Cid.String(), ref); err != nil {
		return nil, err
	}
	// First we clear the entries once they'v been committed
	var emptyEntries []*Entry
	idx.Entries = emptyEntries
	// Rotate the store
	idx.StoreID = w.ms.Next()
	w.storeID = idx.StoreID
	w.store, err = w.ms.Get(w.storeID)
	if err != nil {
		return nil, err
	}

	return ref, w.SetIndex(idx)
}

// Unpack a DAG archive into a list of files given the data root and store ID
// TODO: need to measure the memory footprint of returning the entire list vs a specific file
// Right now it's useful because we can get multiple files from the same Unpack operation
func (w *Workdag) Unpack(ctx context.Context, root cid.Cid, s multistore.StoreID) (map[string]files.Node, error) {
	store, err := w.ms.Get(s)
	if err != nil {
		return nil, err
	}
	lk := cidlink.Link{Cid: root}
	nb := basicnode.Prototype.List.NewBuilder()

	err = lk.Load(ctx, ipld.LinkContext{}, nb, store.Loader)
	if err != nil {
		return nil, err
	}
	fls := make(map[string]files.Node)
	nd := nb.Build()
	itr := nd.ListIterator()

	for !itr.Done() {
		_, n, err := itr.Next()
		if err != nil {
			return nil, err
		}
		entry, err := n.LookupByString("Link")
		if err != nil {
			return nil, err
		}
		l, err := entry.AsLink()
		if err != nil {
			return nil, err
		}
		flk := l.(cidlink.Link).Cid
		dn, err := store.DAG.Get(ctx, flk)
		if err != nil {
			return nil, err
		}
		f, err := unixfile.NewUnixfsFile(ctx, store.DAG, dn)
		if err != nil {
			return nil, err
		}
		entry, err = n.LookupByString("Name")
		if err != nil {
			return nil, err
		}
		k, err := entry.AsString()
		if err != nil {
			return nil, err
		}
		fls[k] = f
	}
	return fls, nil
}

// SetRef adds a ref in the index and increments the LFU queue
func (w *Workdag) SetRef(key string, ref *DataRef) error {
	idx, err := w.Index()
	if err != nil {
		return err
	}
	idx.Refs[key] = ref
	w.size += uint64(ref.PayloadSize)
	if w.ub > 0 && w.lb > 0 {
		if w.size > w.ub {
			w.evict(w.size - w.lb)
		}
	}
	// We evict the item before adding the new one
	w.increment(ref)
	return w.SetIndex(idx)
}

// GetRef gets a ref in the index and increments the LFU queue registering as a Read
func (w *Workdag) GetRef(key string) (*DataRef, error) {
	idx, err := w.Index()
	if err != nil {
		return nil, err
	}
	ref, ok := idx.Refs[key]
	if !ok {
		return nil, ErrRefNotFound
	}
	w.increment(ref)
	return ref, w.SetIndex(idx)
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

func (w *Workdag) increment(ref *DataRef) {
	currentPlace := ref.freqNode
	var nextFreq int64
	var nextPlace *list.Element
	if currentPlace == nil {
		// new entry
		nextFreq = 1
		nextPlace = w.freqs.Front()
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
			nextPlace = w.freqs.InsertAfter(li, currentPlace)
		} else {
			nextPlace = w.freqs.PushFront(li)
		}
	}
	ref.Freq = nextFreq
	ref.freqNode = nextPlace
	nextPlace.Value.(*listEntry).entries[ref] = 1
	if currentPlace != nil {
		// remove from current position
		w.remEntry(currentPlace, ref)
	}
}

func (w *Workdag) remEntry(place *list.Element, entry *DataRef) {
	le := place.Value.(*listEntry)
	entries := le.entries
	delete(entries, entry)
	if len(entries) == 0 {
		w.freqs.Remove(place)
	}
}

func (w *Workdag) evict(size uint64) uint64 {
	// No lock here so it can be called
	// from within the lock (during Set)
	var evicted uint64
	for evicted < size {
		if place := w.freqs.Front(); place != nil {
			for entry := range place.Value.(*listEntry).entries {
				if evicted < size {
					idx, err := w.Index()
					if err != nil {
						continue
					}
					delete(idx.Refs, entry.PayloadCID.String())
					w.SetIndex(idx)

					err = w.ms.Delete(entry.StoreID)
					if err != nil {
						continue
					}

					w.remEntry(place, entry)
					evicted += uint64(entry.PayloadSize)
					w.size -= uint64(entry.PayloadSize)
				}
			}
		}
	}
	return evicted
}

// Index contains the information about which objects are currently checked out
// in the workdag, having information about the working files.
type Index struct {
	// StoreID is the store ID in which the indexed dags are stored
	StoreID multistore.StoreID
	// Entries is the collection of staged dags. The order of
	// this collection is not guaranteed
	Entries []*Entry
	// Refs is a collection of archived dags stored locally.
	// the key is a CID.String()
	Refs map[string]*DataRef
}

// Add creates a new Entry and returns it. The caller should first check that
// another entry with the same path does not exist.
func (i *Index) Add(path string) *Entry {
	e := &Entry{
		Name: path,
	}

	i.Entries = append(i.Entries, e)
	return e
}

// Entry returns the entry that match the given path, if any.
func (i *Index) Entry(path string) (*Entry, error) {
	for _, e := range i.Entries {
		if e.Name == path {
			return e, nil
		}
	}

	return nil, ErrEntryNotFound
}

// Remove remove the entry that match the give path and returns deleted entry.
func (i *Index) Remove(path string) (*Entry, error) {
	path = filepath.ToSlash(path)
	for index, e := range i.Entries {
		if e.Name == path {
			i.Entries = append(i.Entries[:index], i.Entries[index+1:]...)
			return e, nil
		}
	}

	return nil, ErrEntryNotFound
}

// Entry represents the merkle root of a single file
type Entry struct {
	// Cid is the content id of the represented
	Cid cid.Cid
	// Name is the entry path
	Name string
	// Size is the original file size
	Size int64
}
