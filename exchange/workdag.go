package exchange

import (
	"bytes"
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/filecoin-project/go-multistore"
	cid "github.com/ipfs/go-cid"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	chunk "github.com/ipfs/go-ipfs-chunker"
	files "github.com/ipfs/go-ipfs-files"
	ipldformat "github.com/ipfs/go-ipld-format"
	"github.com/ipfs/go-merkledag"
	unixfile "github.com/ipfs/go-unixfs/file"
	"github.com/ipfs/go-unixfs/importer/balanced"
	"github.com/ipfs/go-unixfs/importer/helpers"
	"github.com/ipld/go-ipld-prime"
	dagpb "github.com/ipld/go-ipld-prime-proto"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	basicnode "github.com/ipld/go-ipld-prime/node/basic"
	"github.com/ipld/go-ipld-prime/traversal"
	"github.com/ipld/go-ipld-prime/traversal/selector"
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

// Workdag manages our DAG operations, it combines multiple DAGs into a single one for storage.
type Workdag struct {
	ms *multistore.MultiStore
}

// NewWorkdag instanciates a workdag, checks if we have a store ID and loads the right store
func NewWorkdag(ms *multistore.MultiStore) *Workdag {
	return &Workdag{
		ms: ms,
	}
}

// Tx creates a new DAG transaction
func (w *Workdag) Tx(ctx context.Context) *Tx {
	storeID := w.ms.Next()
	store, err := w.ms.Get(storeID)
	return &Tx{
		storeID: storeID,
		store:   store,
		ctx:     ctx,
		entries: make(map[string]Entry),
		Err:     err,
	}
}

// Tx is a DAG transaction
// it is not safe for concurrent access. Concurrency requires separate transactions.
type Tx struct {
	storeID multistore.StoreID
	store   *multistore.Store

	ctx     context.Context
	entries map[string]Entry
	// Err exposes any error reported by the transaction during use
	Err error
}

// Entry represents the merkle root of a single DAG, usually a single file
type Entry struct {
	// Cid is the content id of the represented
	Cid cid.Cid
	// Size is the original file size
	Size int64
}

// Store exposes the underlying store
func (tx *Tx) Store() *multistore.Store {
	return tx.store
}

// StoreID exposes the ID of the underlying store
func (tx *Tx) StoreID() multistore.StoreID {
	return tx.storeID
}

// PutOptions describes how an add operation should be performed
type PutOptions struct {
	// Path is the exact filepath to a the file or directory to be added.
	Path string
	// ChunkSize is size by which to chunk the content when adding a file.
	ChunkSize int64
}

// Put adds or replaces a file into the transaction
func (tx *Tx) Put(key string, opts PutOptions) (cid.Cid, error) {
	if tx.Err != nil {
		return cid.Undef, tx.Err
	}
	link, err := tx.add(key, opts)
	if err != nil {
		return cid.Undef, err
	}
	root := link.(cidlink.Link).Cid
	return root, nil
}

func (tx *Tx) add(key string, opts PutOptions) (ipld.Link, error) {
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
		return tx.addDir(key, f, opts)
	case files.File:
		return tx.addFile(key, f, opts)
	default:
		return nil, fmt.Errorf("unknown file type")
	}
}

func (tx *Tx) addFile(key string, f files.File, opts PutOptions) (ipld.Link, error) {
	bufferedDS := ipldformat.NewBufferedDAG(tx.ctx, tx.store.DAG)

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

	e := Entry{}
	e.Cid = n.Cid()
	e.Size, err = f.Size()
	if err != nil {
		return nil, err
	}
	tx.entries[key] = e

	return cidlink.Link{Cid: n.Cid()}, nil

}

// KeyFromPath returns a key name from a file path
func KeyFromPath(p string) string {
	_, name := filepath.Split(p)
	return name
}

func (tx *Tx) addDir(key string, dir files.Directory, opts PutOptions) (ipld.Link, error) {
	return nil, fmt.Errorf("TODO")
}

// Status represents our staged entries
type Status map[string]Entry

func (s Status) String() string {
	buf := bytes.NewBuffer(nil)
	// Format everything in a balanced table layout
	// we might want to move this with the cli
	w := new(tabwriter.Writer)
	w.Init(buf, 0, 4, 2, ' ', 0)
	var total int64 = 0
	for k, v := range s {
		fmt.Fprintf(
			w,
			"%s\t%s\t%s\n",
			k,
			v.Cid,
			filecoin.SizeStr(filecoin.NewInt(uint64(v.Size))),
		)
		total += v.Size
	}
	if total > 0 {
		fmt.Fprintf(w, "Total\t-\t%s\n", filecoin.SizeStr(filecoin.NewInt(uint64(total))))
	}
	w.Flush()
	return buf.String()
}

// Status returns a list of the current entries
func (tx *Tx) Status() (Status, error) {
	if tx.Err != nil {
		return Status{}, tx.Err
	}
	return Status(tx.entries), nil
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
func (tx *Tx) assembleEntries() (ipld.Node, error) {
	// We need a single root CID so we make a list with the roots of all dagpb roots
	nb := basicnode.Prototype.List.NewBuilder()
	as, err := nb.BeginList(int64(len(tx.entries)))
	if err != nil {
		return nil, err
	}

	for k, v := range tx.entries {
		// Each entry is a map with 2 keys: Name and Link
		mas, err := as.AssembleValue().BeginMap(2)
		if err != nil {
			return nil, err
		}
		nas, err := mas.AssembleEntry("Name")
		if err != nil {
			return nil, err
		}
		err = nas.AssignString(k)
		if err != nil {
			return nil, err
		}
		las, err := mas.AssembleEntry("Link")
		if err != nil {
			return nil, err
		}
		clk := cidlink.Link{Cid: v.Cid}
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
func (tx *Tx) Commit() (*DataRef, error) {
	if tx.Err != nil {
		return nil, tx.Err
	}
	lb := cidlink.LinkBuilder{
		Prefix: cid.Prefix{
			Version:  1,
			Codec:    0x71, // dag-cbor as per multicodec
			MhType:   DefaultHashFunction,
			MhLength: -1,
		},
	}

	if len(tx.entries) == 0 {
		return nil, errors.New("tx empty, nothing to commit")
	}

	var size int64
	for _, e := range tx.entries {
		size += e.Size
	}

	nd, err := tx.assembleEntries()
	if err != nil {
		return nil, err
	}
	lnk, err := lb.Build(
		tx.ctx,
		ipld.LinkContext{},
		nd,
		tx.store.Storer,
	)
	if err != nil {
		return nil, err
	}
	c := lnk.(cidlink.Link)

	ref := &DataRef{
		PayloadCID:  c.Cid,
		PayloadSize: size,
		StoreID:     tx.storeID,
	}
	return ref, nil
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

// DAGStat describes a DAG
type DAGStat struct {
	Size      int
	NumBlocks int
}

// Stat returns stats about a selected part of DAG given a cid
// The cid must be registered in the index
func (w *Workdag) Stat(ctx context.Context, store *multistore.Store, root cid.Cid, sel ipld.Node) (DAGStat, error) {
	res := DAGStat{}
	link := cidlink.Link{Cid: root}
	chooser := dagpb.AddDagPBSupportToChooser(func(ipld.Link, ipld.LinkContext) (ipld.NodePrototype, error) {
		return basicnode.Prototype.Any, nil
	})
	// The root node could be a raw node so we need to select the builder accordingly
	nodeType, err := chooser(link, ipld.LinkContext{})
	if err != nil {
		return res, err
	}
	builder := nodeType.NewBuilder()
	// We make a custom loader to intercept when each block is read during the traversal
	makeLoader := func(bs blockstore.Blockstore) ipld.Loader {
		return func(lnk ipld.Link, lnkCtx ipld.LinkContext) (io.Reader, error) {
			c, ok := lnk.(cidlink.Link)
			if !ok {
				return nil, fmt.Errorf("incorrect Link Type")
			}
			block, err := bs.Get(c.Cid)
			if err != nil {
				return nil, err
			}
			reader := bytes.NewReader(block.RawData())
			res.Size += reader.Len()
			res.NumBlocks++
			return reader, nil
		}
	}
	// Load the root node
	err = link.Load(ctx, ipld.LinkContext{}, builder, makeLoader(store.Bstore))
	if err != nil {
		return res, fmt.Errorf("unable to load link: %v", err)
	}
	nd := builder.Build()

	s, err := selector.ParseSelector(sel)
	if err != nil {
		return res, err
	}
	// Traverse any links from the root node
	err = traversal.Progress{
		Cfg: &traversal.Config{
			LinkLoader:                     makeLoader(store.Bstore),
			LinkTargetNodePrototypeChooser: chooser,
		},
	}.WalkMatching(nd, s, func(prog traversal.Progress, n ipld.Node) error {
		return nil
	})
	return res, nil
}
