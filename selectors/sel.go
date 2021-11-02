package selectors

import (
	"github.com/ipld/go-ipld-prime"
	basicnode "github.com/ipld/go-ipld-prime/node/basic"
	"github.com/ipld/go-ipld-prime/traversal/selector"
	"github.com/ipld/go-ipld-prime/traversal/selector/builder"
)

// All the default selector that reaches all the blocks
func All() ipld.Node {
	ssb := builder.NewSelectorSpecBuilder(basicnode.Prototype.Any)
	return ssb.ExploreRecursive(selector.RecursionLimitNone(),
		ssb.ExploreAll(ssb.ExploreRecursiveEdge())).Node()
}

// Entries selects all entries of an IPLD collection without traversing any data linked in the entries
// Limitting the recursion depth to 1 will reach all the entries of a map but not beyond.
func Entries() ipld.Node {
	ssb := builder.NewSelectorSpecBuilder(basicnode.Prototype.Any)
	return ssb.ExploreRecursive(selector.RecursionLimitDepth(1),
		ssb.ExploreAll(ssb.ExploreRecursiveEdge())).Node()
}

// Key selects the link and all the children associated with a given key in a Map
func Key(key string) ipld.Node {
	ssb := builder.NewSelectorSpecBuilder(basicnode.Prototype.Any)
	return ssb.ExploreUnion(ssb.Matcher(),
		ssb.ExploreFields(func(efsb builder.ExploreFieldsSpecBuilder) {
			efsb.Insert(key, ssb.ExploreRecursive(selector.RecursionLimitNone(),
				ssb.ExploreAll(ssb.ExploreRecursiveEdge())))
		})).Node()
}

// Hamt is used to query a HAMT without following the links in deferred nodes
func Hamt() ipld.Node {
	ssb := builder.NewSelectorSpecBuilder(basicnode.Prototype.Any)
	return ssb.ExploreRecursive(selector.RecursionLimitNone(),
		ssb.ExploreAll(
			ssb.ExploreFields(func(efsb builder.ExploreFieldsSpecBuilder) {
				efsb.Insert("Value", ssb.ExploreRecursiveEdge())
			}),
		),
	).Node()
}
