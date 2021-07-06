package utils

import (
	"bytes"
	"context"
	"fmt"
	"io"

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

// MapLoadableKeys returns all the keys of a Tx, given its cid and a loader
// this only returns the keys for entries where the blocks are available in the blockstore
func MapLoadableKeys(ctx context.Context, root cid.Cid, loader ipld.Loader) (KeyList, error) {
	// Turn the CID into an ipld Link interface, this will link to all the children
	lk := cidlink.Link{Cid: root}
	// Create an instance of map builder as we're looking to extract all the keys from an IPLD map
	nb := basicnode.Prototype.Map.NewBuilder()
	// Use a loader from the link to read all the children blocks from a given store
	err := lk.Load(ctx, ipld.LinkContext{}, nb, loader)
	if err != nil {
		return nil, err
	}
	// load the IPLD tree
	nd := nb.Build()
	// Gather the keys in an array
	entries := make([]string, nd.Length())
	it := nd.MapIterator()
	i := 0
	// Iterate over all the map entries
	for !it.Done() {
		k, v, err := it.Next()
		// all succeed or fail
		if err != nil {
			return nil, err
		}
		vnd, err := v.LookupByString("Value")
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
			// The block might not be available in the store
			continue
		}

		// The key IPLD node needs to be decoded as a string
		key, err := k.AsString()
		if err != nil {
			return nil, err
		}
		entries[i] = key
		i++
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
			fmt.Println("migrating", c)
			block, err := from.Get(c.Cid)
			if err != nil {
				return nil, err
			}
			if err := to.Put(block); err != nil {
				return nil, err
			}
			reader := bytes.NewReader(block.RawData())
			return reader, nil
		}
	}
	// Load the root node
	err = link.Load(ctx, ipld.LinkContext{}, builder, makeLoader(from))
	if err != nil {
		return fmt.Errorf("unable to load link: %v", err)
	}
	nd := builder.Build()

	s, err := selector.ParseSelector(sel)
	if err != nil {
		return err
	}
	// Traverse any links from the root node
	return traversal.Progress{
		Cfg: &traversal.Config{
			LinkLoader:                     makeLoader(from),
			LinkTargetNodePrototypeChooser: Chooser,
		},
	}.WalkMatching(nd, s, func(prog traversal.Progress, n ipld.Node) error {
		return nil
	})
}
