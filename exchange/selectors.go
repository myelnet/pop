package exchange

import (
	"github.com/ipld/go-ipld-prime"
	basicnode "github.com/ipld/go-ipld-prime/node/basic"
	"github.com/ipld/go-ipld-prime/traversal/selector"
	"github.com/ipld/go-ipld-prime/traversal/selector/builder"
)

// AllSelector is the default selector that reaches all the blocks
func AllSelector() ipld.Node {
	ssb := builder.NewSelectorSpecBuilder(basicnode.Prototype.Any)
	return ssb.ExploreRecursive(selector.RecursionLimitNone(),
		ssb.ExploreAll(ssb.ExploreRecursiveEdge())).Node()
}

// MapFieldSelector selects the link and all the children associated with a given key in a Map
func MapFieldSelector(key string) ipld.Node {
	ssb := builder.NewSelectorSpecBuilder(basicnode.Prototype.Any)
	return ssb.ExploreUnion(ssb.Matcher(),
		ssb.ExploreFields(func(efsb builder.ExploreFieldsSpecBuilder) {
			efsb.Insert(key, ssb.ExploreRecursive(selector.RecursionLimitNone(),
				ssb.ExploreAll(ssb.ExploreRecursiveEdge())))
		})).Node()
}

// IndexSelector is used to query an index without following the links
func IndexSelector() ipld.Node {
	ssb := builder.NewSelectorSpecBuilder(basicnode.Prototype.Any)
	return ssb.ExploreRecursive(selector.RecursionLimitNone(),
		ssb.ExploreUnion(
			ssb.ExploreFields(func(efsb builder.ExploreFieldsSpecBuilder) {
				efsb.Insert("PayloadCID", ssb.Matcher())
			}),
			ssb.ExploreAll(ssb.ExploreRecursiveEdge()),
		),
	).Node()
}
