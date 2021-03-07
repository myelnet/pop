package node

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/filecoin-project/go-multistore"
	cid "github.com/ipfs/go-cid"
	chunk "github.com/ipfs/go-ipfs-chunker"
	files "github.com/ipfs/go-ipfs-files"
	ipldformat "github.com/ipfs/go-ipld-format"
	"github.com/ipfs/go-merkledag"
	"github.com/ipfs/go-unixfs/importer/balanced"
	"github.com/ipfs/go-unixfs/importer/helpers"
	"github.com/ipld/go-car"
	"github.com/ipld/go-ipld-prime"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	"github.com/myelnet/go-hop-exchange"
)

var (
	// ErrEntryNotFound is returned by Index.Entry, if an entry is not found.
	ErrEntryNotFound = errors.New("entry not found")
)

// Workdag represents any local content that hasn't been committed into a car file yet.
type Workdag struct {
	store *multistore.Store
	index *Index
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
	e, err := w.index.Entry(opts.Path)
	if errors.Is(err, ErrEntryNotFound) {
		e = w.index.Add(opts.Path)
	} else if err != nil {
		return nil, err
	}
	e.Cid = n.Cid()
	e.Size, err = f.Size()
	if err != nil {
		return nil, err
	}

	return cidlink.Link{Cid: n.Cid()}, nil

}

func (w *Workdag) doAddDir(ctx context.Context, dir files.Directory, opts AddOptions) (ipld.Link, error) {
	return nil, fmt.Errorf("TODO")
}

// Status represents our staged files, the key is a string path
type Status map[string]cid.Cid

func (s Status) String() string {
	buf := bytes.NewBuffer(nil)
	for path, c := range s {
		fmt.Fprintf(buf, "%s %s\n", c, path)
	}

	return buf.String()
}

// Status returns the current Status
func (w *Workdag) Status() (Status, error) {
	s := make(Status)
	for _, e := range w.index.Entries {
		s[e.Name] = e.Cid
	}
	return s, nil
}

// CommitOptions might be useful later to add authorship
type CommitOptions struct {
}

// Commit stores the current contents of the index in a new car and loads it in the blockstore
func (w *Workdag) Commit(ctx context.Context, opts CommitOptions) ([]cid.Cid, error) {
	buf := new(bytes.Buffer)

	var dags []car.Dag
	for _, e := range w.index.Entries {
		dags = append(dags, car.Dag{Root: e.Cid, Selector: hop.AllSelector()})
	}

	sc := car.NewSelectiveCar(ctx, w.store.Bstore, dags)
	if err := sc.Write(buf); err != nil {
		return nil, err
	}
	ch, err := car.LoadCar(w.store.Bstore, buf)
	if err != nil {
		return nil, err
	}
	return ch.Roots, nil
}

// Index contains the information about which objects are currently checked out
// in the workdag, having information about the working files.
type Index struct {
	// Entries collection of entries represented by this Index. The order of
	// this collection is not guaranteed
	Entries []*Entry
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
