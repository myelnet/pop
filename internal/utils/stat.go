package utils

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/filecoin-project/go-multistore"
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

// Stat returns stats about a selected part of DAG given a cid
// The cid must be registered in the index
func Stat(ctx context.Context, store *multistore.Store, root cid.Cid, sel ipld.Node) (DAGStat, error) {
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

// MapKeys returns all the keys of a Tx, given its cid and a datastore
func MapKeys(ctx context.Context, root cid.Cid, loader ipld.Loader) ([][]byte, error) {
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
	entries := make([][]byte, nd.Length())
	it := nd.MapIterator()
	i := 0
	// Iterate over all the map entries
	for !it.Done() {
		k, _, err := it.Next()
		// all succeed or fail
		if err != nil {
			return nil, err
		}
		// The key IPLD node needs to be decoded as bytes
		key, err := k.AsBytes()
		if err != nil {
			return nil, err
		}
		entries[i] = key
		i++
	}
	return entries, nil
}
