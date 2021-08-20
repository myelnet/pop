package exchange

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-multistore"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/ipfs/go-blockservice"
	cid "github.com/ipfs/go-cid"
	"github.com/ipfs/go-graphsync/storeutil"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	offline "github.com/ipfs/go-ipfs-exchange-offline"
	files "github.com/ipfs/go-ipfs-files"
	ipldformat "github.com/ipfs/go-ipld-format"
	"github.com/ipfs/go-merkledag"
	"github.com/ipfs/go-unixfs"
	unixfile "github.com/ipfs/go-unixfs/file"
	"github.com/ipld/go-ipld-prime"
	dagpb "github.com/ipld/go-ipld-prime-proto"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	basicnode "github.com/ipld/go-ipld-prime/node/basic"
	peer "github.com/libp2p/go-libp2p-core/peer"
	mh "github.com/multiformats/go-multihash"
	"github.com/myelnet/pop/filecoin"
	"github.com/myelnet/pop/internal/utils"
	"github.com/myelnet/pop/retrieval"
	"github.com/myelnet/pop/retrieval/client"
	"github.com/myelnet/pop/retrieval/deal"
	"github.com/myelnet/pop/selectors"
	"github.com/rs/zerolog/log"
)

// DefaultHashFunction used for generating CIDs of imported data
const DefaultHashFunction = uint64(mh.SHA2_256)

// ErrNoStrategy is returned when we try querying content without a read strategy
var ErrNoStrategy = errors.New("no strategy")

// Entry represents a link to an item in the DAG map
type Entry struct {
	// Key is string name of the entry
	Key string `json:"key"`
	// Value is the CID of the represented content
	Value cid.Cid `json:"value"`
	// Size is the original file size. Not encoded in the DAG
	Size int64 `json:"size"`
}

// TxResult returns metadata about the transaction including a potential error if something failed
type TxResult struct {
	Err   error
	Size  uint64 // Size is the total amount of bytes exchanged during this transaction
	Spent abi.TokenAmount
	PayCh address.Address
}

// Tx is an exchange transaction which may contain multiple DAGs to be exchanged with a set of connected peers
type Tx struct {
	ctx       context.Context
	cancelCtx context.CancelFunc
	// global blockstore to dump the content to when the work is over
	bs blockstore.Blockstore
	// MultiStore creates an isolated store for this transaction before it is migrated over to the main store
	ms *multistore.MultiStore
	// storeID is the ID of the store used to read or write the content of this session
	storeID multistore.StoreID
	// store is the isolated blockstore and DAG instances for this session
	store *multistore.Store
	// entries is the cached reference to values used during the session
	entries map[string]Entry
	// disco is the discovery mechanism for finding content offers
	rou *GossipRouting
	// retriever manages the state of the transfer once we have a good offer
	retriever *retrieval.Client
	// deals keeps track of ongoing provider offers to use instead of querying the network
	deals *deal.Mgr
	// index is the exchange content index
	index *Index
	// repl is the replication module
	repl *Replication
	// clientAddr is the address that will be used to make any payment for retrieving the content
	clientAddr address.Address
	// root is the root cid of the dag we are retrieving during this session
	root cid.Cid
	// size it the total size of content used in this session
	size int64
	// chunk size is the chunk size to use when adding files
	chunkSize int64
	// codec defines what IPLD multicodec to use for assembling the entries
	codec uint64
	// cacheRF is the cache replication factor used when committing to storage
	cacheRF int
	// sel is the selector used to select specific nodes only to retrieve. if not provided we select
	// all the nodes by default
	sel ipld.Node
	// we can also declare the nodes to retrieve using a list of paths
	paths []string
	// done is the final message telling us we have received all the blocks and all is well. if the error
	// is not nil we've run out of options and nothing we can do at this time will get us the content.
	done chan TxResult
	// errs receives any kind of error status from execution so we can try to fix it.
	errs chan deal.Status
	// unsubscribes is used to clear any subscriptions to our retrieval events when we have received
	// all the content
	unsub retrieval.Unsubscribe
	// worker executes retrieval over one or more offers
	worker OfferWorker
	// ongoing
	ongoing chan DealRef
	// triage is a stream of deals that requires manual confirmation
	// if it's nil we don't need confirmation
	triage chan DealSelection
	// dispatching is a stream of peer confirmations when dispatching updates
	dispatching chan PRecord
	// committed indicates whether this transaction was committed or not
	committed bool
	// Err exposes any error reported by the session during use
	Err error
	// closed keeps track whether the tx was already closed
	closed bool
}

// TxOption sets optional fields on a Tx struct
type TxOption func(*Tx)

// WithStrategy starts a new strategy worker to handle incoming discovery results
func WithStrategy(strategy SelectionStrategy) TxOption {
	return func(tx *Tx) {
		tx.worker = strategy(tx)
		tx.worker.Start()
		tx.rou.SetReceiver(tx.worker.PushBack)
	}
}

// WithRoot assigns a new root to the transaction if we are working with a DAG that wasn't created
// during this transaction
func WithRoot(r cid.Cid) TxOption {
	return func(tx *Tx) {
		tx.root = r
	}
}

// WithTriage allows a transaction to manually prompt for external confirmation before executing an offer
func WithTriage() TxOption {
	return func(tx *Tx) {
		tx.triage = make(chan DealSelection)
	}
}

// WithCodec changes the codec to use for aggregating the entries in a single root
func WithCodec(codec uint64) TxOption {
	return func(tx *Tx) {
		tx.codec = codec
	}
}

// SetCacheRF sets the cache replication factor before committing
// we don't set it as an option as the value may only be known when committing
// Setting a replication factor of 0 will not trigger any network requests when committing
func (tx *Tx) SetCacheRF(rf int) {
	tx.cacheRF = rf
}

// Put a DAG for a given key in the transaction
func (tx *Tx) Put(key string, value cid.Cid, size int64) error {
	tx.entries[key] = Entry{
		Key:   key,
		Value: value,
		Size:  size,
	}
	return tx.buildRoot()
}

// Status represents our staged values
type Status map[string]Entry

func (s Status) String() string {
	buf := bytes.NewBuffer(nil)
	// Format everything in a balanced table layout
	// we might want to move this with the cli
	w := new(tabwriter.Writer)
	w.Init(buf, 0, 4, 2, ' ', 0)
	var total int64 = 0
	for _, e := range s {
		fmt.Fprintf(
			w,
			"%s\t%s\t%s\n",
			e.Key,
			e.Value,
			filecoin.SizeStr(filecoin.NewInt(uint64(e.Size))),
		)
		total += e.Size
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

// rootProto returns the node prototype to use for assembling entries
func (tx *Tx) rootProto() (ipld.NodePrototype, error) {
	var proto ipld.NodePrototype
	switch tx.codec {
	case 0x70:
		proto = dagpb.Type.PBNode
	case 0x71:
		proto = basicnode.Prototype.Map
	default:
		return nil, errors.New("codec invalid")
	}
	return proto, nil
}

// EntrySlice is a slice of entries exposing method for sorting
type EntrySlice []Entry

func (es EntrySlice) Len() int           { return len(es) }
func (es EntrySlice) Swap(a, b int)      { es[a], es[b] = es[b], es[a] }
func (es EntrySlice) Less(a, b int) bool { return es[a].Key < es[b].Key }

// assemble all the entries into a single dag Node
func (tx *Tx) assembleEntries() (ipld.Node, error) {
	proto, err := tx.rootProto()
	if err != nil {
		return nil, err
	}
	nb := proto.NewBuilder()
	as, err := nb.BeginMap(len(tx.entries))
	if err != nil {
		return nil, err
	}
	// Create an entry for all the links
	lks, err := as.AssembleEntry("Links")
	if err != nil {
		return nil, err
	}
	// Create a list of entries
	lm, err := lks.BeginList(len(tx.entries))
	if err != nil {
		return nil, err
	}
	entries, err := tx.Entries()
	if err != nil {
		return nil, err
	}
	for _, v := range entries {
		mas, err := lm.AssembleValue().BeginMap(3)
		if err != nil {
			return nil, err
		}
		nas, err := mas.AssembleEntry("Name")
		if err != nil {
			return nil, err
		}
		if err := nas.AssignString(v.Key); err != nil {
			return nil, err
		}
		has, err := mas.AssembleEntry("Hash")
		if err != nil {
			return nil, err
		}
		if err := has.AssignLink(cidlink.Link{Cid: v.Value}); err != nil {
			return nil, err
		}
		sas, err := mas.AssembleEntry("Tsize")
		if err != nil {
			return nil, err
		}
		if err := sas.AssignInt(int(v.Size)); err != nil {
			return nil, err
		}
		if err := mas.Finish(); err != nil {
			return nil, err
		}
	}
	if err := lm.Finish(); err != nil {
		return nil, err
	}
	// Add folder pb data to stay compatible with unixfs
	dt, err := as.AssembleEntry("Data")
	if err != nil {
		return nil, err
	}
	if err := dt.AssignBytes(unixfs.FolderPBData()); err != nil {
		return nil, err
	}
	if err := as.Finish(); err != nil {
		return nil, err
	}
	return nb.Build(), nil
}

// updateDAG stores the current contents of the index in an array to yield a single root CID
func (tx *Tx) buildRoot() error {
	var size int64
	for _, e := range tx.entries {
		size += e.Size
	}

	lb := cidlink.LinkBuilder{
		Prefix: cid.Prefix{
			Version:  1,
			Codec:    tx.codec,
			MhType:   DefaultHashFunction,
			MhLength: -1,
		},
	}

	nd, err := tx.assembleEntries()
	if err != nil {
		return err
	}
	lnk, err := lb.Build(tx.ctx, ipld.LinkContext{}, nd, tx.store.Storer)
	if err != nil {
		return err
	}
	c := lnk.(cidlink.Link)
	tx.root = c.Cid
	tx.size = size
	return nil
}

// Ref returns the DataRef associated with this transaction
func (tx *Tx) Ref() *DataRef {
	var keys [][]byte

	if len(tx.entries) > 0 {
		for k := range tx.entries {
			keys = append(keys, []byte(k))
		}
	} else {
		kl, err := utils.MapLoadableKeys(context.TODO(), tx.root, tx.Store().Loader)
		if err != nil {
			tx.Err = err
		} else {
			keys = kl.AsBytes()
		}
	}

	return &DataRef{
		PayloadCID:  tx.root,
		PayloadSize: tx.size,
		Keys:        keys,
	}
}

// Commit sends the transaction on the exchange
func (tx *Tx) Commit() error {
	if tx.Err != nil {
		return tx.Err
	}

	tx.committed = true

	opts := DefaultDispatchOptions
	if tx.cacheRF > 0 {
		opts.RF = tx.cacheRF
		opts.StoreID = tx.storeID
		var err error
		tx.dispatching, err = tx.repl.Dispatch(tx.root, uint64(tx.size), opts)
		if err != nil {
			return err
		}
	} else {
		// Do not block WatchDispatch
		tx.dispatching = make(chan PRecord)
		close(tx.dispatching)
	}
	return nil
}

func (tx *Tx) getUnixDAG(k cid.Cid, DAG ipldformat.DAGService) (files.Node, error) {
	dn, err := DAG.Get(tx.ctx, k)
	if err != nil {
		return nil, err
	}
	return unixfile.NewUnixfsFile(tx.ctx, DAG, dn)

}

// GetFile retrieves a file associated with the given key from the cache
func (tx *Tx) GetFile(k string) (files.Node, error) {
	// If the key is in our cached entries we can use the current DAG
	if e, ok := tx.entries[k]; ok {
		return tx.getUnixDAG(e.Value, tx.store.DAG)
	}
	// Check the index if we may already have it from a different transaction
	if _, err := tx.index.GetRef(tx.root); err == nil {
		return tx.loadFileEntry(k, &multistore.Store{
			Loader: storeutil.LoaderForBlockstore(tx.bs),
			DAG:    merkledag.NewDAGService(blockservice.New(tx.bs, offline.Exchange(tx.bs))),
		})
	}
	return tx.loadFileEntry(k, tx.store)
}

// IsLocal tells us if this node is storing the content of this transaction or if it needs to retrieve it
func (tx *Tx) IsLocal(key string) bool {
	_, exists := tx.entries[key]
	if exists {
		return true
	}

	ref, err := tx.index.GetRef(tx.root)
	if err != nil {
		return false
	}
	if ref != nil && key == "" {
		return true
	}
	if ref != nil {
		has := ref.Has(key)
		if has {
			return has
		}
		// If we don't have it, let's warm up the mutistore with the index so we don't pay for it twice
		err := utils.MigrateSelectBlocks(tx.ctx, tx.bs, tx.store.Bstore, tx.root, selectors.Entries())
		if err != nil {
			log.Error().Err(err).Msg("warming up index blocks")
		}
		return false
	}

	return false
}

// Keys lists the keys for all the entries in the root map of this transaction
func (tx *Tx) Keys() ([]string, error) {
	// If this transaction has entries we just return them otherwise
	// we're looking at a different transaction
	if len(tx.entries) > 0 {
		entries := make([]string, len(tx.entries))
		i := 0
		for k := range tx.entries {
			entries[i] = k
			i++
		}
		return entries, nil
	}

	loader := storeutil.LoaderForBlockstore(tx.bs)
	if _, err := tx.index.GetRef(tx.root); err != nil {
		// Keys might still be in multistore
		loader = tx.store.Loader
	}

	keys, err := utils.MapLoadableKeys(
		tx.ctx,
		tx.root,
		loader,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get keys: %w", err)
	}
	return keys, nil
}

func (tx *Tx) getEntry(key string, loader ipld.Loader) (en Entry, err error) {
	lk := cidlink.Link{Cid: tx.root}
	proto, err := tx.rootProto()
	if err != nil {
		return en, err
	}
	nb := proto.NewBuilder()

	err = lk.Load(tx.ctx, ipld.LinkContext{}, nb, loader)
	if err != nil {
		return en, err
	}
	nd := nb.Build()
	links, err := nd.LookupByString("Links")
	if err != nil {
		return en, err
	}
	it := links.ListIterator()

	for !it.Done() {
		_, v, err := it.Next()
		if err != nil {
			return en, err
		}
		nn, err := v.LookupByString("Name")
		if err != nil {
			return en, err
		}
		name, err := nn.AsString()
		if err != nil {
			return en, err
		}
		if name != key {
			continue
		}
		ln, err := v.LookupByString("Hash")
		if err != nil {
			return en, err
		}
		l, err := ln.AsLink()
		if err != nil {
			return en, err
		}
		sn, err := v.LookupByString("Tsize")
		if err != nil {
			return en, err
		}
		s, err := sn.AsInt()
		if err != nil {
			return en, err
		}
		return Entry{
			Key:   key,
			Value: l.(cidlink.Link).Cid,
			Size:  int64(s),
		}, nil
	}
	return en, errors.New("entry not found")
}

// RootFor returns the root of a given key
// @TODO: improve scaling and performance for accessing subroots
func (tx *Tx) RootFor(key string) (cid.Cid, error) {
	if key == "*" {
		return tx.root, nil
	}
	loader := storeutil.LoaderForBlockstore(tx.bs)
	if _, err := tx.index.GetRef(tx.root); err != nil {
		// Keys might still be in multistore
		loader = tx.store.Loader
	}
	entry, err := tx.getEntry(key, loader)
	if err != nil {
		return cid.Undef, err
	}
	return entry.Value, nil
}

// Entries returns all the entries in the root map of this transaction sorted by key
func (tx *Tx) Entries() ([]Entry, error) {
	// If this transaction has entries we just return them otherwise
	// we're looking at a different transaction
	if len(tx.entries) > 0 {
		entries := make([]Entry, len(tx.entries))
		i := 0
		for _, v := range tx.entries {
			entries[i] = v
			i++
		}
		sort.Stable(EntrySlice(entries))
		return entries, nil
	}

	loader := storeutil.LoaderForBlockstore(tx.bs)
	if _, err := tx.index.GetRef(tx.root); err != nil {
		// Keys might still be in multistore
		loader = tx.store.Loader
	}

	lk := cidlink.Link{Cid: tx.root}
	proto, err := tx.rootProto()
	if err != nil {
		return nil, err
	}
	// Create an instance of map builder as we're looking to extract all the keys from an IPLD map
	nb := proto.NewBuilder()
	// Use a loader from the link to read all the children blocks from the global store
	err = lk.Load(tx.ctx, ipld.LinkContext{}, nb, loader)
	if err != nil {
		return nil, err
	}
	// load the IPLD tree
	nd := nb.Build()

	links, err := nd.LookupByString("Links")
	if err != nil {
		return nil, err
	}
	// Gather the keys in an array
	entries := make([]Entry, links.Length())
	it := links.ListIterator()
	i := 0
	// Iterate over all the map entries
	for !it.Done() {
		_, v, err := it.Next()
		// all succeed or fail
		if err != nil {
			return nil, err
		}

		nn, err := v.LookupByString("Name")
		if err != nil {
			return nil, err
		}
		key, err := nn.AsString()
		if err != nil {
			return nil, err
		}

		// An entry with no value should fail
		vn, err := v.LookupByString("Hash")
		if err != nil {
			return nil, err
		}
		l, err := vn.AsLink()
		if err != nil {
			return nil, err
		}

		entries[i] = Entry{
			Key:   key,
			Value: l.(cidlink.Link).Cid,
		}
		i++

		// An entry with no size is still fine
		sn, err := v.LookupByString("Tsize")
		if err != nil {
			log.Debug().Str("key", key).Msg("no size present in entry")
			continue
		}
		size, err := sn.AsInt()
		if err != nil {
			continue
		}
		entries[i-1].Size = int64(size)

	}
	return entries, nil

}

func (tx *Tx) loadFileEntry(k string, store *multistore.Store) (files.Node, error) {
	entry, err := tx.getEntry(k, store.Loader)
	if err != nil {
		return nil, err
	}
	return tx.getUnixDAG(entry.Value, store.DAG)
}

// WatchDispatch registers a function to be called every time
// the content is received by a peer
func (tx *Tx) WatchDispatch(fn func(r PRecord)) {
	for rec := range tx.dispatching {
		fn(rec)
	}
}

// Root returns the current root CID of the transaction
func (tx *Tx) Root() cid.Cid {
	return tx.root
}

// Size returns the current size of content cached by the transaction
func (tx *Tx) Size() int64 {
	return tx.size
}

// Store exposes the underlying store
func (tx *Tx) Store() *multistore.Store {
	return tx.store
}

// StoreID exposes the ID of the underlying store
func (tx *Tx) StoreID() multistore.StoreID {
	return tx.storeID
}

// DealRef is the reference to an ongoing deal
type DealRef struct {
	ID    deal.ID
	Offer deal.Offer
}

// DealExecParams are params to apply when executing a selected deal
// Can be used to assign different parameters than the defaults in the offer
// while respecting the offer conditions otherwise it will fail
type DealExecParams struct {
	Accepted   bool
	Selector   ipld.Node
	TotalFunds abi.TokenAmount
}

// DealParam is a functional paramter to set a value on DealExecParams
type DealParam func(*DealExecParams)

// DealSel sets a Deal Selector parameter when executing an offer
func DealSel(sel ipld.Node) DealParam {
	return func(params *DealExecParams) {
		params.Selector = sel
	}
}

// DealFunds sets a total amount to load in payment channel when executing an offer
func DealFunds(amount abi.TokenAmount) DealParam {
	return func(params *DealExecParams) {
		params.TotalFunds = amount
	}
}

// DealSelection sends the selected offer with a channel to expect confirmation on
type DealSelection struct {
	Offer   deal.Offer
	confirm chan DealExecParams
}

// Exec accepts execution for an offer
func (ds DealSelection) Exec(pms ...DealParam) {
	params := DealExecParams{
		Accepted:   true,
		TotalFunds: ds.Offer.RetrievalPrice(),
	}
	for _, p := range pms {
		p(&params)
	}
	ds.confirm <- params
}

// Next declines an offer and moves on to the next one if available
func (ds DealSelection) Next() {
	ds.confirm <- DealExecParams{
		Accepted: false,
	}
}

// Query the discovery service for offers
func (tx *Tx) Query(paths ...string) error {
	if tx.worker == nil {
		return ErrNoStrategy
	}
	// For now we assume providers must have all the blocks from a given root
	// to send a valid offer. When selectors improve we may be able to query for partial offers.
	tx.sel = selectors.All()
	// clean paths
	for _, p := range paths {
		if p == "" {
			continue
		}
		tx.paths = append(tx.paths, p)
	}
	offer, err := tx.deals.FindOfferByCid(tx.root)
	if err == nil {
		info, err := offer.AddrInfo()
		if err == nil {
			newoffer, err := tx.QueryOffer(*info, tx.sel)
			if err == nil {
				tx.ApplyOffer(newoffer)
				return nil
			}
		}
	}
	return tx.rou.Query(tx.ctx, tx.root, selectors.All())
}

// QueryOffer allows querying directly from a given peer
func (tx *Tx) QueryOffer(info peer.AddrInfo, sel ipld.Node) (deal.Offer, error) {
	tx.sel = sel
	return tx.rou.QueryProvider(info, tx.root, sel)
}

// ApplyOffer allows executing a transaction based on an existing offer without querying the routing service
// By the default the offer is added at the front of the queue. If there is already an offer in progress it will
// thus execute after it or if not will execute immediately.
func (tx *Tx) ApplyOffer(offer deal.Offer) {
	tx.worker.PushFront(offer)
}

// Execute starts a retrieval operation for a given offer and returns the deal ID for that operation
func (tx *Tx) Execute(of deal.Offer, p DealExecParams) TxResult {
	info, err := of.AddrInfo()
	if err != nil {
		return TxResult{
			Err: err,
		}
	}
	// Make sure our provider is in our peerstore
	tx.rou.AddAddrs(info.ID, info.Addrs)

	// number of transfers is 1 by default
	transferCount := 1

	var root cid.Cid
	if len(tx.paths) > 0 && tx.paths[0] != "*" {
		// number of transfers based on path + initial index if needed
		transferCount = len(tx.paths)
		// check if we can already get the root of the first path
		root, err = tx.RootFor(tx.paths[0])
		if err != nil {
			// we need to retrieve the entries first
			transferCount++
		}
	}

	results := make(chan TxResult, transferCount)
	tx.unsub = tx.retriever.SubscribeToEvents(func(event client.Event, state deal.ClientState) {
		switch state.Status {
		case deal.StatusCompleted:
			payCh := address.Undef
			if state.PaymentInfo != nil {
				payCh = state.PaymentInfo.PayCh
			}
			select {
			case results <- TxResult{
				Size:  state.TotalReceived,
				Spent: state.FundsSpent,
				PayCh: payCh,
			}:
			default:
			}
		case deal.StatusCancelled, deal.StatusErrored:
			select {
			case results <- TxResult{
				Err: errors.New(deal.Statuses[state.Status]),
			}:
			default:
			}
			return
		}
	})

	final := TxResult{
		Spent: abi.NewTokenAmount(0),
	}

	for i := 0; i < transferCount; i++ {

		funds := tx.deals.GetFundsForCid(tx.root)

		sel := selectors.All()
		if root == cid.Undef {
			root = tx.root
			if len(tx.paths) == 0 || tx.paths[i] != "*" {
				// if we will be fetching some paths we need to retrieve the entries first
				sel = selectors.Entries()
			}
			// set the funds as for the total size of the DAG
			funds = of.RetrievalPrice()

			fmt.Println("--- fetching entries")
		} else {
			fmt.Println("--- fetching entry")
		}

		// we've fetched the entries already or the root from the first path,
		// switch the root to the next path
		if i > 0 {
			root, err = tx.RootFor(tx.paths[i-1])
			if err != nil {
				return TxResult{
					Err: err,
				}
			}
		}
		params, err := deal.NewParams(
			of.MinPricePerByte,
			of.MaxPaymentInterval,
			of.MaxPaymentIntervalIncrease,
			sel,
			nil,
			of.UnsealPrice,
		)
		if err != nil {
			return TxResult{
				Err: err,
			}
		}

		id, err := tx.retriever.Retrieve(
			tx.ctx,
			root,
			params,
			funds,
			info.ID,
			tx.clientAddr,
			of.PaymentAddress,
			&tx.storeID,
		)
		if err != nil {
			final.Err = err
			return final
		}
		tx.ongoing <- DealRef{
			ID:    id,
			Offer: of,
		}
		select {
		case res := <-results:
			if res.Err == nil {
				tx.committed = true
			}
			if root.Equals(tx.root) {
				err := tx.deals.SetOfferForCid(root, of)
				if err != nil {
					log.Error().Err(err).Msg("failed to set offer")
				}
			}
			// For now we just return the error and assume the transfer is failed
			// we do have access to the status in order to try and restart the deal or something else
			final.Size += res.Size
			final.Spent = filecoin.BigAdd(final.Spent, res.Spent)
			final.PayCh = res.PayCh

			remaining := filecoin.BigSub(funds, res.Spent)
			tx.deals.SetFundsForCid(tx.root, remaining)

			continue
		case <-tx.ctx.Done():
			final.Err = tx.ctx.Err()
			return final
		}
	}
	return final
}

// Confirm takes an offer and blocks to wait for user confirmation before returning true or false
func (tx *Tx) Confirm(of deal.Offer) DealExecParams {
	if tx.triage != nil {
		dch := make(chan DealExecParams, 1)
		tx.triage <- DealSelection{
			Offer:   of,
			confirm: dch,
		}
		select {
		case d := <-dch:
			return d
		case <-tx.ctx.Done():
			return DealExecParams{
				Accepted: false,
			}
		}
	}
	return DealExecParams{
		Accepted:   true,
		Selector:   tx.sel,
		TotalFunds: of.RetrievalPrice(),
	}
}

// Triage allows manually triaging the next selection
func (tx *Tx) Triage() (DealSelection, error) {
	select {
	case dc := <-tx.triage:
		return dc, nil
	case <-tx.ctx.Done():
		return DealSelection{}, tx.ctx.Err()
	}
}

// Finish tells the tx all operations have been completed
func (tx *Tx) Finish(res TxResult) {
	tx.done <- res
}

// Done returns a channel that receives any resulting error from the latest operation
func (tx *Tx) Done() <-chan TxResult {
	return tx.done
}

// Ongoing exposes the ongoing channel to get the reference of any in progress deals
func (tx *Tx) Ongoing() <-chan DealRef {
	return tx.ongoing
}

// Close removes any listeners and stream handlers related to a session
// If the transaction was not committed, any staged content will be deleted
func (tx *Tx) Close() error {
	if tx.closed {
		return tx.Err
	}
	tx.closed = true
	if tx.worker != nil {
		_ = tx.worker.Close()
	}
	if tx.unsub != nil {
		tx.unsub()
	}
	err := tx.dumpStore()
	if err != nil {
		return err
	}
	tx.cancelCtx()
	return tx.Err
}

// SetAddress to use for funding the retriebal
func (tx *Tx) SetAddress(addr address.Address) {
	tx.clientAddr = addr
}

// dumpStore transfers all the content from the tx store to the global blockstore
// then deletes the store
func (tx *Tx) dumpStore() error {
	// If we dump before the transaction is committed all the content is lost
	if tx.committed {
		gcbs, ok := tx.bs.(blockstore.GCBlockstore)
		if !ok {
			return errors.New("blockstore is not a GCBlockstore")
		}

		unlock := gcbs.GCLock()
		defer unlock.Unlock()

		err := utils.MigrateBlocks(tx.ctx, tx.store.Bstore, tx.bs)
		if err != nil {
			return err
		}
	}

	return tx.ms.Delete(tx.storeID)
}

// ErrUserDeniedOffer is returned when a user denies an offer
var ErrUserDeniedOffer = errors.New("user denied offer")

// OfferWorker is a generic interface to manage the lifecycle of offers
type OfferWorker interface {
	Start()
	PushFront(deal.Offer)
	PushBack(deal.Offer)
	Close() []deal.Offer
}

// OfferExecutor exposes the methods required to execute offers
type OfferExecutor interface {
	Execute(deal.Offer, DealExecParams) TxResult
	Confirm(deal.Offer) DealExecParams
	Finish(TxResult)
}

// SelectionStrategy is a function that returns an OfferWorker with a defined strategy
// for selecting offers over a given session
type SelectionStrategy func(OfferExecutor) OfferWorker

// We offer a useful presets

// SelectFirst executes the first offer received and buffers other offers during the
// duration of the transfer. If the transfer hard fails it tries continuing with the following offer and so on.
func SelectFirst(oe OfferExecutor) OfferWorker {
	return sessionWorker{
		executor:      oe,
		offersFront:   make(chan deal.Offer),
		offersBack:    make(chan deal.Offer),
		closing:       make(chan chan []deal.Offer, 1),
		numThreshold:  -1,
		timeThreshold: -1,
		priceCeiling:  abi.NewTokenAmount(-1),
	}
}

// SelectCheapest waits for a given amount of offers or delay whichever comes first and selects the cheapest then continues
// receiving offers while the transfer executes. If the transfer fails it will select the next cheapest
// given the buffered offers
func SelectCheapest(after int, t time.Duration) func(OfferExecutor) OfferWorker {
	return func(oe OfferExecutor) OfferWorker {
		return sessionWorker{
			executor:      oe,
			offersFront:   make(chan deal.Offer),
			offersBack:    make(chan deal.Offer),
			closing:       make(chan chan []deal.Offer, 1),
			numThreshold:  after,
			timeThreshold: t,
			priceCeiling:  abi.NewTokenAmount(-1),
		}
	}
}

// SelectFirstLowerThan returns the first offer which price is lower than given amount
// it keeps collecting offers below price threshold to fallback on before completing execution
func SelectFirstLowerThan(amount abi.TokenAmount) func(oe OfferExecutor) OfferWorker {
	return func(oe OfferExecutor) OfferWorker {
		return sessionWorker{
			executor:      oe,
			offersFront:   make(chan deal.Offer),
			offersBack:    make(chan deal.Offer),
			closing:       make(chan chan []deal.Offer, 1),
			numThreshold:  -1,
			timeThreshold: -1,
			priceCeiling:  amount,
		}
	}
}

type sessionWorker struct {
	executor    OfferExecutor
	offersFront chan deal.Offer
	offersBack  chan deal.Offer
	closing     chan chan []deal.Offer
	// numThreshold is the number of offers after which we can start execution
	// if -1 we execute as soon as the first offer gets in
	numThreshold int
	// timeThreshold is the duration after which we can start execution
	// if -1 we execute as soon as the first offer gets in
	timeThreshold time.Duration
	// priceCeiling is the price over which we are ignoring an offer for this session
	priceCeiling abi.TokenAmount
}

func (s sessionWorker) exec(offer deal.Offer, result chan TxResult) {
	// Confirm may block until user sends a response
	// if we are blocking for a while the worker acts as if we'd started the transfer and will continue
	// buffering offers according to the given rules
	if params := s.executor.Confirm(offer); params.Accepted {
		result <- s.executor.Execute(offer, params)
		return
	}
	result <- TxResult{
		Err: ErrUserDeniedOffer,
	}
}

// Start a background routine which can be shutdown by sending a channel to the closing channel
func (s sessionWorker) Start() {
	// nil by default if we have a timeThreshold we assign it
	var delay <-chan time.Time
	if s.timeThreshold >= 0 {
		// delay after which we can start executing the first offer
		delay = time.After(s.timeThreshold)
	}
	// Use the price ceiling if the value is not -1
	useCeiling := !s.priceCeiling.Equals(abi.NewTokenAmount(-1))
	// Start a routine to collect a set of offers
	go func() {
		// Offers are queued in this slice
		// TODO: replace with "container/list"
		var q []deal.Offer
		var execDone chan TxResult
		for {
			select {
			case resc := <-s.closing:
				resc <- q
				return
			case of := <-s.offersBack:
				if useCeiling && of.MinPricePerByte.GreaterThan(s.priceCeiling) {
					continue
				}
				if s.numThreshold < 0 && s.timeThreshold < 0 && execDone == nil {
					execDone = make(chan TxResult, 1)
					go s.exec(of, execDone)
					continue
				}

				q = append(q, of)
				// We're already executing an offer we can ignore the rest
				if execDone != nil {
					continue
				}
				// If after this one we've reached the threshold let's execute the cheapest offer
				if len(q) == s.numThreshold {
					execDone = make(chan TxResult, 1)
					sortOffers(q)
					go s.exec(q[0], execDone)
					q = q[1:]
				}
			case of := <-s.offersFront:
				if execDone == nil {
					execDone = make(chan TxResult, 1)
					go s.exec(of, execDone)
					continue
				}

				q = append([]deal.Offer{of}, q...)
			case <-delay:
				// We may already be executing if we've reached another threshold
				if execDone != nil {
					continue
				}
				execDone = make(chan TxResult, 1)
				sortOffers(q)
				go s.exec(q[0], execDone)
				q = q[1:]
			case res := <-execDone:
				// If the execution returns an error we assume it is not fixable
				// and automatically try the next offer
				if res.Err != nil && len(q) > 0 {
					execDone = make(chan TxResult, 1)
					go s.exec(q[0], execDone)
					q = q[1:]
					continue
				}
				if res.Err == nil || len(q) == 0 {
					s.executor.Finish(res)
				}
			}
		}
	}()
}

// Close the selection returns the last unused offers if any
func (s sessionWorker) Close() []deal.Offer {
	resc := make(chan []deal.Offer)
	s.closing <- resc
	res := <-resc
	return res
}

// PushBack sends a new offer to the end of the queue
func (s sessionWorker) PushBack(offer deal.Offer) {
	// This never blocks as our queue is always receiving and decides when to drop offers
	s.offersBack <- offer
}

// PushFront sends a new offer to the front of the queue
func (s sessionWorker) PushFront(offer deal.Offer) {
	s.offersFront <- offer
}

func sortOffers(offers []deal.Offer) {
	sort.Slice(offers, func(i, j int) bool {
		return offers[i].MinPricePerByte.LessThan(offers[j].MinPricePerByte)
	})
}

// KeyFromPath returns a key name from a file path
func KeyFromPath(p string) string {
	_, name := filepath.Split(p)
	return name
}
