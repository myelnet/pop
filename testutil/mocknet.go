package testutil

import (
	"testing"

	"github.com/ipfs/go-blockservice"
	"github.com/ipfs/go-datastore"
	dss "github.com/ipfs/go-datastore/sync"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	offline "github.com/ipfs/go-ipfs-exchange-offline"
	ipldformat "github.com/ipfs/go-ipld-format"
	"github.com/ipfs/go-merkledag"
	"github.com/libp2p/go-libp2p-core/host"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/stretchr/testify/require"
)

type TestNode struct {
	Ds   datastore.Batching
	Bs   blockstore.Blockstore
	DAG  ipldformat.DAGService
	Host host.Host
}

func NewTestNode(mn mocknet.Mocknet, t *testing.T) *TestNode {
	testNode := &TestNode{}
	var err error

	testNode.Ds = dss.MutexWrap(datastore.NewMapDatastore())

	testNode.Bs = blockstore.NewBlockstore(testNode.Ds)

	testNode.DAG = merkledag.NewDAGService(blockservice.New(testNode.Bs, offline.Exchange(testNode.Bs)))

	testNode.Host, err = mn.GenPeer()
	require.NoError(t, err)

	return testNode
}
