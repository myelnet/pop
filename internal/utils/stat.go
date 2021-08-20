package utils

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/filecoin-project/go-multistore"
	blocks "github.com/ipfs/go-block-format"
	cid "github.com/ipfs/go-cid"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	"github.com/ipld/go-ipld-prime"
	dagpb "github.com/ipld/go-ipld-prime-proto"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	basicnode "github.com/ipld/go-ipld-prime/node/basic"
	"github.com/ipld/go-ipld-prime/traversal"
	"github.com/ipld/go-ipld-prime/traversal/selector"
)

// DAGStat describes a DAG
type DAGStat struct {
	Size      int
	NumBlocks int
}

// Chooser decides which node type to use when decoding IPLD nodes
var Chooser = dagpb.AddDagPBSupportToChooser(func(ipld.Link, ipld.LinkContext) (ipld.NodePrototype, error) {
	return basicnode.Prototype.Any, nil
})

// Stat returns stats about a selected part of DAG given a cid
// The cid must be registered in the index
func Stat(ctx context.Context, store *multistore.Store, root cid.Cid, sel ipld.Node) (DAGStat, error) {
	res := DAGStat{}

	err := WalkDAG(ctx, root, store.Bstore, sel, func(block blocks.Block) error {
		res.Size += len(block.RawData())
		res.NumBlocks++

		return nil
	})

	return res, err
}

// WalkDAG executes a DAG traversal for a given root and selector and calls a callback function for every block loaded during the traversal
func WalkDAG(
	ctx context.Context,
	root cid.Cid,
	bs blockstore.Blockstore,
	sel ipld.Node,
	f func(blocks.Block) error) error {
	link := cidlink.Link{Cid: root}
	// The root node could be a raw node so we need to select the builder accordingly
	nodeType, err := Chooser(link, ipld.LinkContext{})
	if err != nil {
		return err
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
			err = f(block)
			if err != nil {
				return nil, err
			}

			return reader, nil
		}
	}

	// Load the root node
	err = link.Load(ctx, ipld.LinkContext{}, builder, makeLoader(bs))
	if err != nil {
		return fmt.Errorf("unable to load link: %v", err)
	}
	nd := builder.Build()

	s, err := selector.ParseSelector(sel)
	if err != nil {
		return err
	}

	// Traverse any links from the root node
	err = traversal.Progress{
		Cfg: &traversal.Config{
			LinkLoader:                     makeLoader(bs),
			LinkTargetNodePrototypeChooser: Chooser,
		},
	}.WalkMatching(nd, s, func(prog traversal.Progress, n ipld.Node) error {
		return nil
	})
	if err != nil {
		return err
	}

	return nil
}

// KeyList is a list of strings representing all the keys in an IPLD Map
type KeyList []string

// AsBytes returns all the keys as byte slices
func (kl KeyList) AsBytes() [][]byte {
	out := make([][]byte, len(kl))
	for i, k := range kl {
		out[i] = []byte(k)
	}
	return out
}

// Sorted ensures the key list is sorted
func (kl KeyList) Sorted() KeyList {
	sort.Strings(kl)
	return kl
}

// MapLoadableKeys returns all the keys of a Tx, given its cid and a loader
// this only returns the keys for entries where the blocks are available in the blockstore
// it supports both dagpb and dagcbor nodes
func MapLoadableKeys(ctx context.Context, root cid.Cid, loader ipld.Loader) (KeyList, error) {
	// Turn the CID into an ipld Link interface, this will link to all the children
	lk := cidlink.Link{Cid: root}

	nodeType, err := Chooser(lk, ipld.LinkContext{})
	if err != nil {
		return nil, err
	}
	nb := nodeType.NewBuilder()

	err = lk.Load(ctx, ipld.LinkContext{}, nb, loader)
	if err != nil {
		return nil, err
	}
	// load the IPLD tree
	nd := nb.Build()

	// Gather the keys in an array
	var entries []string

	links, err := nd.LookupByString("Links")
	if err != nil {
		return nil, err
	}
	it := links.ListIterator()

	for !it.Done() {
		_, v, err := it.Next()
		if err != nil {
			return nil, err
		}
		ln, err := v.LookupByString("Hash")
		if err != nil {
			return nil, err
		}

		l, err := ln.AsLink()
		if err != nil {
			return nil, err
		}
		nt, err := Chooser(l, ipld.LinkContext{})
		if err != nil {
			return nil, err
		}
		builder := nt.NewBuilder()
		err = l.Load(ctx, ipld.LinkContext{}, builder, loader)
		if err != nil {
			continue
		}
		kn, err := v.LookupByString("Name")
		if err != nil {
			return nil, err
		}
		key, err := kn.AsString()
		if err != nil {
			return nil, err
		}
		entries = append(entries, key)
	}
	return entries, nil
}

// MapMissingKeys returns keys for values for which the links are not loadable
func MapMissingKeys(ctx context.Context, root cid.Cid, loader ipld.Loader) (KeyList, error) { // Turn the CID into an ipld Link interface, this will link to all the children
	lk := cidlink.Link{Cid: root}
	nodeType, err := Chooser(lk, ipld.LinkContext{})
	if err != nil {
		return nil, err
	}
	nb := nodeType.NewBuilder()

	err = lk.Load(ctx, ipld.LinkContext{}, nb, loader)
	if err != nil {
		return nil, err
	}

	// load the IPLD tree
	nd := nb.Build()
	// Gather the keys in an array
	var entries []string

	links, err := nd.LookupByString("Links")
	if err != nil {
		return nil, err
	}
	it := links.ListIterator()

	// Iterate over all the map entries
	for !it.Done() {
		_, v, err := it.Next()
		// all succeed or fail
		if err != nil {
			return nil, err
		}
		vnd, err := v.LookupByString("Hash")
		if err != nil {
			return nil, err
		}
		l, err := vnd.AsLink()
		if err != nil {
			return nil, err
		}
		nodeType, err := Chooser(l, ipld.LinkContext{})
		if err != nil {
			return nil, err
		}
		builder := nodeType.NewBuilder()
		err = l.Load(ctx, ipld.LinkContext{}, builder, loader)
		if err != nil {
			kn, err := v.LookupByString("Name")
			if err != nil {
				return nil, err
			}
			// The block is not available in the store
			key, err := kn.AsString()
			if err != nil {
				return nil, err
			}
			entries = append(entries, key)
		}

	}
	return KeyList(entries), nil
}

// MigrateBlocks transfers all blocks from a blockstore to another
func MigrateBlocks(ctx context.Context, from blockstore.Blockstore, to blockstore.Blockstore) error {
	kchan, err := from.AllKeysChan(ctx)
	if err != nil {
		return err
	}
	for k := range kchan {
		blk, err := from.Get(k)
		if err != nil {
			return err
		}
		err = to.Put(blk)
		if err != nil {
			return err
		}
	}
	return nil
}

// MigrateSelectBlocks transfers blocks from a blockstore to another for a given block selection
func MigrateSelectBlocks(ctx context.Context, from blockstore.Blockstore, to blockstore.Blockstore, root cid.Cid, sel ipld.Node) error {
	return WalkDAG(ctx, root, from, sel, func(block blocks.Block) error {
		return to.Put(block)
	})
}
