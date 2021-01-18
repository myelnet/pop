package testutil

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"testing"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	dtimpl "github.com/filecoin-project/go-data-transfer/impl"
	dtnet "github.com/filecoin-project/go-data-transfer/network"
	dtgstransport "github.com/filecoin-project/go-data-transfer/transport/graphsync"
	"github.com/filecoin-project/go-storedcounter"
	blocks "github.com/ipfs/go-block-format"
	"github.com/ipfs/go-blockservice"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
	dss "github.com/ipfs/go-datastore/sync"
	"github.com/ipfs/go-graphsync"
	graphsyncimpl "github.com/ipfs/go-graphsync/impl"
	"github.com/ipfs/go-graphsync/network"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	chunk "github.com/ipfs/go-ipfs-chunker"
	offline "github.com/ipfs/go-ipfs-exchange-offline"
	files "github.com/ipfs/go-ipfs-files"
	ipldformat "github.com/ipfs/go-ipld-format"
	"github.com/ipfs/go-merkledag"
	unixfile "github.com/ipfs/go-unixfs/file"
	"github.com/ipfs/go-unixfs/importer/balanced"
	"github.com/ipfs/go-unixfs/importer/helpers"
	"github.com/ipld/go-ipld-prime"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	"github.com/libp2p/go-libp2p-core/host"
	peer "github.com/libp2p/go-libp2p-peer"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/stretchr/testify/require"
)

//go:generate cbor-gen-for FakeDTType

// FakeDTType simple fake type for using with registries
type FakeDTType struct {
	Data string
}

// Type satisfies registry.Entry
func (ft FakeDTType) Type() datatransfer.TypeIdentifier {
	return "FakeDTType"
}

type TestNode struct {
	Ds              datastore.Batching
	Bs              blockstore.Blockstore
	DAG             ipldformat.DAGService
	Host            host.Host
	Loader          ipld.Loader
	Storer          ipld.Storer
	Gs              graphsync.GraphExchange
	DTNet           dtnet.DataTransferNetwork
	DTStore         datastore.Batching
	DTTmpDir        string
	DTStoredCounter *storedcounter.StoredCounter
	Dt              datatransfer.Manager
}

func NewTestNode(mn mocknet.Mocknet, t *testing.T) *TestNode {
	testNode := &TestNode{}

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
			return bytes.NewReader(block.RawData()), nil
		}
	}

	makeStorer := func(bs blockstore.Blockstore) ipld.Storer {
		return func(lnkCtx ipld.LinkContext) (io.Writer, ipld.StoreCommitter, error) {
			var buf bytes.Buffer
			var committer ipld.StoreCommitter = func(lnk ipld.Link) error {
				c, ok := lnk.(cidlink.Link)
				if !ok {
					return fmt.Errorf("incorrect Link Type")
				}
				block, err := blocks.NewBlockWithCid(buf.Bytes(), c.Cid)
				if err != nil {
					return err
				}
				return bs.Put(block)
			}
			return &buf, committer, nil
		}
	}
	var err error

	testNode.Ds = dss.MutexWrap(datastore.NewMapDatastore())

	testNode.Bs = blockstore.NewBlockstore(testNode.Ds)

	testNode.DAG = merkledag.NewDAGService(blockservice.New(testNode.Bs, offline.Exchange(testNode.Bs)))

	testNode.Loader = makeLoader(testNode.Bs)
	testNode.Storer = makeStorer(testNode.Bs)

	testNode.Host, err = mn.GenPeer()
	require.NoError(t, err)

	return testNode
}

func (tn *TestNode) SetupDataTransfer(ctx context.Context, t *testing.T) {
	var err error
	tn.DTStoredCounter = storedcounter.New(tn.Ds, datastore.NewKey("nextDTID"))
	tn.DTNet = dtnet.NewFromLibp2pHost(tn.Host)
	tn.DTStore = namespace.Wrap(tn.Ds, datastore.NewKey("DataTransfer"))
	tn.DTTmpDir, err = ioutil.TempDir("", "dt-tmp")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.RemoveAll(tn.DTTmpDir)
	})
	tn.Gs = graphsyncimpl.New(ctx, network.NewFromLibp2pHost(tn.Host), tn.Loader, tn.Storer)
	dtTransport := dtgstransport.NewTransport(tn.Host.ID(), tn.Gs)
	tn.Dt, err = dtimpl.NewDataTransfer(tn.DTStore, tn.DTTmpDir, tn.DTNet, dtTransport, tn.DTStoredCounter)
	require.NoError(t, err)

	ready := make(chan error, 1)
	tn.Dt.OnReady(func(err error) {
		ready <- err
	})
	require.NoError(t, tn.Dt.Start(ctx))
	select {
	case <-ctx.Done():
		t.Fatal("startup interrupted")
	case err := <-ready:
		require.NoError(t, err)
	}
}

const unixfsChunkSize uint64 = 1 << 10
const unixfsLinksPerLevel = 1024

func (tn *TestNode) LoadUnixFSFileToStore(ctx context.Context, t *testing.T, dirPath string) (ipld.Link, []byte) {
	fpath, err := filepath.Abs(filepath.Join(thisDir(t), "..", dirPath))
	require.NoError(t, err)

	f, err := os.Open(fpath)
	require.NoError(t, err)

	var buf bytes.Buffer
	tr := io.TeeReader(f, &buf)
	file := files.NewReaderFile(tr)

	// import to UnixFS
	bufferedDS := ipldformat.NewBufferedDAG(ctx, tn.DAG)

	params := helpers.DagBuilderParams{
		Maxlinks:   unixfsLinksPerLevel,
		RawLeaves:  true,
		CidBuilder: nil,
		Dagserv:    bufferedDS,
	}

	db, err := params.New(chunk.NewSizeSplitter(file, int64(unixfsChunkSize)))
	require.NoError(t, err)

	nd, err := balanced.Layout(db)
	require.NoError(t, err)

	err = bufferedDS.Commit()
	require.NoError(t, err)

	// save the original files bytes
	return cidlink.Link{Cid: nd.Cid()}, buf.Bytes()
}

func (tn *TestNode) VerifyFileTransferred(ctx context.Context, t *testing.T, link cid.Cid, origBytes []byte) {

	n, err := tn.DAG.Get(ctx, link)
	require.NoError(t, err)

	ufile, err := unixfile.NewUnixfsFile(ctx, tn.DAG, n)
	require.NoError(t, err)

	fn, ok := ufile.(files.File)
	require.True(t, ok)

	b := make([]byte, len(origBytes))
	_, err = fn.Read(b)
	if err != nil {
		require.Equal(t, "EOF", err.Error())
	}

	require.EqualValues(t, origBytes, b)
}

func thisDir(t *testing.T) string {
	_, fname, _, ok := runtime.Caller(1)
	require.True(t, ok)
	return path.Dir(fname)
}

type FakeDTValidator struct{}

func (v *FakeDTValidator) ValidatePush(sender peer.ID, voucher datatransfer.Voucher, baseCid cid.Cid, selector ipld.Node) (datatransfer.VoucherResult, error) {
	return nil, nil
}

func (v *FakeDTValidator) ValidatePull(receiver peer.ID, voucher datatransfer.Voucher, baseCid cid.Cid, selector ipld.Node) (datatransfer.VoucherResult, error) {
	return nil, nil
}