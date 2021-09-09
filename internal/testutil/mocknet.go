package testutil

import (
	"bytes"
	"context"
	"io"
	"math/rand"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/filecoin-project/go-address"
	cborutil "github.com/filecoin-project/go-cbor-util"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	dtimpl "github.com/filecoin-project/go-data-transfer/impl"
	dtnet "github.com/filecoin-project/go-data-transfer/network"
	dtgstransport "github.com/filecoin-project/go-data-transfer/transport/graphsync"
	init2 "github.com/filecoin-project/specs-actors/v5/actors/builtin/init"
	blocks "github.com/ipfs/go-block-format"
	"github.com/ipfs/go-blockservice"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
	dss "github.com/ipfs/go-datastore/sync"
	"github.com/ipfs/go-graphsync"
	graphsyncimpl "github.com/ipfs/go-graphsync/impl"
	"github.com/ipfs/go-graphsync/network"
	"github.com/ipfs/go-graphsync/storeutil"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	blocksutil "github.com/ipfs/go-ipfs-blocksutil"
	chunk "github.com/ipfs/go-ipfs-chunker"
	offline "github.com/ipfs/go-ipfs-exchange-offline"
	files "github.com/ipfs/go-ipfs-files"
	ipldformat "github.com/ipfs/go-ipld-format"
	"github.com/ipfs/go-merkledag"
	unixfile "github.com/ipfs/go-unixfs/file"
	"github.com/ipfs/go-unixfs/importer/balanced"
	"github.com/ipfs/go-unixfs/importer/helpers"
	"github.com/ipld/go-ipld-prime"
	"github.com/ipld/go-ipld-prime/codec/dagcbor"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	basicnode "github.com/ipld/go-ipld-prime/node/basic"
	"github.com/libp2p/go-libp2p-core/host"
	peer "github.com/libp2p/go-libp2p-peer"
	tnet "github.com/libp2p/go-libp2p-testing/net"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	mh "github.com/multiformats/go-multihash"
	"github.com/myelnet/go-multistore"
	"github.com/myelnet/pop/filecoin"
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
	Ds        datastore.Batching
	Bs        blockstore.Blockstore
	DAG       ipldformat.DAGService
	Host      host.Host
	LinkSys   ipld.LinkSystem
	Gs        graphsync.GraphExchange
	DTNet     dtnet.DataTransferNetwork
	DTStore   datastore.Batching
	DTTmpDir  string
	Dt        datatransfer.Manager
	Ms        *multistore.MultiStore
	OrigBytes []byte
}

func NewTestNode(mn mocknet.Mocknet, t testing.TB, opts ...func(tn *TestNode)) *TestNode {
	var err error
	testNode := &TestNode{}

	testNode.DTTmpDir = t.TempDir()
	testNode.Ds = dss.MutexWrap(datastore.NewMapDatastore())
	testNode.Bs = blockstore.NewGCBlockstore(blockstore.NewBlockstore(testNode.Ds), blockstore.NewGCLocker())
	testNode.DAG = merkledag.NewDAGService(blockservice.New(testNode.Bs, offline.Exchange(testNode.Bs)))
	testNode.Ms, err = multistore.NewMultiDstore(testNode.Ds)
	require.NoError(t, err)

	testNode.LinkSys = storeutil.LinkSystemForBlockstore(testNode.Bs)
	// We generate our own peer to avoid the default bogus private key
	peer, err := tnet.RandPeerNetParams()
	require.NoError(t, err)

	testNode.Host, err = mn.AddPeer(peer.PrivKey, peer.Addr)
	require.NoError(t, err)

	for _, opt := range opts {
		opt(testNode)
	}

	return testNode
}

func (tn *TestNode) SetupGraphSync(ctx context.Context) {
	tn.Gs = graphsyncimpl.New(ctx, network.NewFromLibp2pHost(tn.Host), tn.LinkSys)
}

func (tn *TestNode) SetupDataTransfer(ctx context.Context, t testing.TB) {
	var err error
	tn.DTNet = dtnet.NewFromLibp2pHost(tn.Host)
	tn.DTStore = namespace.Wrap(tn.Ds, datastore.NewKey("DataTransfer"))
	tn.Gs = graphsyncimpl.New(ctx, network.NewFromLibp2pHost(tn.Host), tn.LinkSys)
	dtTransport := dtgstransport.NewTransport(tn.Host.ID(), tn.Gs)
	tn.Dt, err = dtimpl.NewDataTransfer(tn.DTStore, tn.DTTmpDir, tn.DTNet, dtTransport)
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

func (tn *TestNode) CreateRandomFile(t testing.TB, size int) string {
	file, err := os.CreateTemp("", "data")
	require.NoError(t, err)
	t.Cleanup(func() {
		defer os.Remove(file.Name())
	})

	data := make([]byte, size)
	rand.New(rand.NewSource(time.Now().UnixNano())).Read(data)
	_, err = file.Write(data)
	require.NoError(t, err)

	tn.OrigBytes = data
	return file.Name()
}

func CreateRandomBlock(t testing.TB, bs blockstore.Blockstore) *blocks.BasicBlock {
	lsys := storeutil.LinkSystemForBlockstore(bs)

	lp := cidlink.LinkPrototype{
		Prefix: cid.Prefix{
			Version:  1,
			Codec:    0x71, // dag-cbor as per multicodec
			MhType:   uint64(mh.BLAKE2B_MIN + 31),
			MhLength: -1,
		},
	}

	data := make([]byte, 10)
	rand.New(rand.NewSource(time.Now().UnixNano())).Read(data)

	b1 := basicnode.NewBytes(data)
	lnk, err := lsys.Store(
		ipld.LinkContext{},
		lp,
		b1,
	)
	var buffer bytes.Buffer
	require.NoError(t, dagcbor.Encode(b1, &buffer))
	require.NoError(t, err)

	blk, err := blocks.NewBlockWithCid(buffer.Bytes(), lnk.(cidlink.Link).Cid)
	require.NoError(t, err)

	return blk
}

const unixfsChunkSize uint64 = 1 << 10
const unixfsLinksPerLevel = 1024

func (tn *TestNode) ThisDir(t testing.TB, p string) string {
	fpath, err := filepath.Abs(filepath.Join(ThisDir(t), "..", p))
	require.NoError(t, err)
	return fpath
}

func (tn *TestNode) LoadFileToStore(ctx context.Context, t testing.TB, store *multistore.Store, path string) (ipld.Link, []byte) {
	f, err := os.Open(path)
	require.NoError(t, err)

	var buf bytes.Buffer
	tr := io.TeeReader(f, &buf)
	file := files.NewReaderFile(tr)

	prefix, err := merkledag.PrefixForCidVersion(1)
	if err != nil {
		require.NoError(t, err)
	}
	prefix.MhType = uint64(mh.BLAKE2B_MIN + 31)

	// import to UnixFS
	bufferedDS := ipldformat.NewBufferedDAG(ctx, store.DAG)

	params := helpers.DagBuilderParams{
		Maxlinks:   unixfsLinksPerLevel,
		RawLeaves:  true,
		CidBuilder: prefix,
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

func (tn *TestNode) LoadFileToNewStore(ctx context.Context, t testing.TB, dirPath string) (ipld.Link, multistore.StoreID, []byte) {
	storeID := tn.Ms.Next()
	store, err := tn.Ms.Get(storeID)
	require.NoError(t, err)

	link, b := tn.LoadFileToStore(ctx, t, store, dirPath)
	return link, storeID, b
}

func (tn *TestNode) VerifyFileTransferred(ctx context.Context, t testing.TB, dag ipldformat.DAGService, link cid.Cid, origBytes []byte) {
	n, err := dag.Get(ctx, link)
	require.NoError(t, err)

	ufile, err := unixfile.NewUnixfsFile(ctx, dag, n)
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

func (tn *TestNode) NukeBlockstore(ctx context.Context, t testing.TB) {
	cids, err := tn.Bs.AllKeysChan(ctx)
	require.NoError(t, err)

	for i := 0; i < cap(cids); i++ {
		err := tn.Bs.DeleteBlock(<-cids)
		require.NoError(t, err)
	}
}

func ThisDir(t testing.TB) string {
	_, fname, _, ok := runtime.Caller(1)
	require.True(t, ok)
	return path.Dir(fname)
}

type FakeDTValidator struct{}

func (v *FakeDTValidator) ValidatePush(isRestart bool, chid datatransfer.ChannelID, sender peer.ID, voucher datatransfer.Voucher, baseCid cid.Cid, selector ipld.Node) (datatransfer.VoucherResult, error) {
	return nil, nil
}

func (v *FakeDTValidator) ValidatePull(isRestart bool, chid datatransfer.ChannelID, receiver peer.ID, voucher datatransfer.Voucher, baseCid cid.Cid, selector ipld.Node) (datatransfer.VoucherResult, error) {
	return nil, nil
}

func Connect(tn1, tn2 *TestNode) error {
	pinfo := host.InfoFromHost(tn1.Host)
	return tn2.Host.Connect(context.Background(), *pinfo)
}

var blockGen = blocksutil.NewBlockGenerator()

func FormatMsgLookup(t *testing.T, chAddr address.Address) *filecoin.MsgLookup {
	createChannelRet := init2.ExecReturn{
		IDAddress:     chAddr,
		RobustAddress: chAddr,
	}
	createChannelRetBytes, err := cborutil.Dump(&createChannelRet)
	require.NoError(t, err)
	lookup := &filecoin.MsgLookup{
		Message: blockGen.Next().Cid(),
		Receipt: filecoin.MessageReceipt{
			ExitCode: 0,
			Return:   createChannelRetBytes,
			GasUsed:  10,
		},
	}

	return lookup
}

type BytesGetter struct {
	b []byte
}

func (bg *BytesGetter) UnmarshalCBOR(r io.Reader) error {
	buf := new(bytes.Buffer)
	buf.ReadFrom(r)
	bg.b = buf.Bytes()
	return nil
}

func (bg *BytesGetter) Bytes() []byte {
	return bg.b
}
