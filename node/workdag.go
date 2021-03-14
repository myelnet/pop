package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/filecoin-project/go-commp-utils/writer"
	"github.com/filecoin-project/go-multistore"
	"github.com/filecoin-project/go-state-types/abi"
	cid "github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
	chunk "github.com/ipfs/go-ipfs-chunker"
	files "github.com/ipfs/go-ipfs-files"
	ipldformat "github.com/ipfs/go-ipld-format"
	"github.com/ipfs/go-merkledag"
	"github.com/ipfs/go-unixfs/importer/balanced"
	"github.com/ipfs/go-unixfs/importer/helpers"
	"github.com/ipld/go-car"
	"github.com/ipld/go-ipld-prime"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	basicnode "github.com/ipld/go-ipld-prime/node/basic"
	"github.com/myelnet/pop/filecoin"
)

var (
	// ErrEntryNotFound is returned by Index.Entry, if an entry is not found.
	ErrEntryNotFound = errors.New("entry not found")
)

// KStoreID is datastore key for persisting the last ID of a store for the current workdag
const KStoreID = "storeid"

// KIndex is the datastore key for persisting the index of a workdag
const KIndex = "index"

// Workdag represents any local content that hasn't been committed into a car file yet.
type Workdag struct {
	storeID multistore.StoreID
	store   *multistore.Store
	ms      *multistore.MultiStore
	ds      datastore.Batching
}

// NewWorkdag instanciates a workdag, checks if we have a store ID and loads the right store
func NewWorkdag(ms *multistore.MultiStore, ds datastore.Batching) (*Workdag, error) {
	w := &Workdag{
		ds: namespace.Wrap(ds, datastore.NewKey("/workdag")),
		ms: ms,
	}
	idx, err := w.Index()
	if err != nil && errors.Is(err, datastore.ErrNotFound) {
		idx = &Index{
			StoreID: ms.Next(),
		}
	} else if err != nil {
		return nil, err
	}
	w.storeID = idx.StoreID
	w.store, err = ms.Get(idx.StoreID)
	if err != nil {
		return nil, err
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
	enc, err := json.Marshal(idx)
	if err != nil {
		return err
	}

	return w.ds.Put(datastore.NewKey(KIndex), enc)
}

// Index decodes and returns the workdag index from the datastore
func (w *Workdag) Index() (*Index, error) {
	var idx Index
	enc, err := w.ds.Get(datastore.NewKey(KIndex))
	if err != nil {
		return nil, err
	}
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
		Maxlinks:   unixfsLinksPerLevel,
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
	e, err := idx.Entry(opts.Path)
	if errors.Is(err, ErrEntryNotFound) {
		e = idx.Add(opts.Path)
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
	w.Init(buf, 0, 4, 0, '\t', 0)
	for _, e := range s {
		fmt.Fprintf(
			w,
			"%s\t%s\t%s\n",
			e.Name,
			e.Cid,
			filecoin.SizeStr(filecoin.NewInt(uint64(e.Size))),
		)
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
	// Piece is a Filecoin unit of storage
	PieceCID  cid.Cid
	PieceSize abi.PaddedPieceSize
	StoreID   multistore.StoreID
}

// Commit stores the current contents of the index in an array to yield a single root CID
func (w *Workdag) Commit(ctx context.Context, opts CommitOptions) (*DataRef, error) {
	idx, err := w.Index()
	if err != nil {
		return nil, err
	}

	if len(idx.Entries) == 0 {
		return nil, errors.New("workdag clean, nothing to commit")
	}

	// We need a single root CID so we make a list with the roots of all
	// dagpb roots and use that in our CAR generation
	nb := basicnode.Prototype.List.NewBuilder()

	as, err := nb.BeginList(int64(len(idx.Entries)))
	if err != nil {
		return nil, err
	}

	for _, e := range idx.Entries {
		lk := cidlink.Link{Cid: e.Cid}
		err := as.AssembleValue().AssignLink(lk)
		if err != nil {
			return nil, err
		}
	}
	err = as.Finish()
	if err != nil {
		return nil, err
	}

	lb := cidlink.LinkBuilder{
		Prefix: cid.Prefix{
			Version:  1,
			Codec:    0x71, // dag-cbor as per multicodec
			MhType:   DefaultHashFunction,
			MhLength: -1,
		},
	}

	lnk, err := lb.Build(
		ctx,
		ipld.LinkContext{},
		nb.Build(),
		w.store.Storer,
	)
	if err != nil {
		return nil, err
	}
	c := lnk.(cidlink.Link)

	wr := &writer.Writer{}
	bw := bufio.NewWriterSize(wr, int(writer.CommPBuf))

	err = car.WriteCar(ctx, w.store.DAG, []cid.Cid{c.Cid}, wr)
	if err != nil {
		return nil, err
	}

	if err := bw.Flush(); err != nil {
		return nil, err
	}

	dataCIDSize, err := wr.Sum()
	if err != nil {
		return nil, err
	}

	ref := &DataRef{
		PayloadCID:  c.Cid,
		PayloadSize: dataCIDSize.PayloadSize,
		PieceSize:   dataCIDSize.PieceSize,
		PieceCID:    dataCIDSize.PieceCID,
		StoreID:     w.storeID,
	}
	// First we clear the entries once they'v been committed
	var emptyEntries []*Entry
	idx.Entries = emptyEntries
	// Add our new commit
	idx.Commits = append(idx.Commits, ref)
	// Rotate the store
	idx.StoreID = w.ms.Next()
	w.storeID = idx.StoreID
	w.store, err = w.ms.Get(w.storeID)
	if err != nil {
		return nil, err
	}

	return ref, w.SetIndex(idx)
}

// Index contains the information about which objects are currently checked out
// in the workdag, having information about the working files.
type Index struct {
	// StoreID is the store ID in which the indexed dags are stored
	StoreID multistore.StoreID
	// Entries is the collection of staged dags. The order of
	// this collection is not guaranteed
	Entries []*Entry
	// Commits is a collection of archived dags ready to be stored.
	Commits []*DataRef
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
