package exchange

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-multistore"
	"github.com/filecoin-project/go-state-types/abi"
	cid "github.com/ipfs/go-cid"
	chunk "github.com/ipfs/go-ipfs-chunker"
	files "github.com/ipfs/go-ipfs-files"
	ipldformat "github.com/ipfs/go-ipld-format"
	"github.com/ipfs/go-merkledag"
	unixfile "github.com/ipfs/go-unixfs/file"
	"github.com/ipfs/go-unixfs/importer/balanced"
	"github.com/ipfs/go-unixfs/importer/helpers"
	"github.com/ipld/go-ipld-prime"
	iprime "github.com/ipld/go-ipld-prime"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	basicnode "github.com/ipld/go-ipld-prime/node/basic"
	peer "github.com/libp2p/go-libp2p-core/peer"
	mh "github.com/multiformats/go-multihash"
	"github.com/myelnet/pop/filecoin"
	"github.com/myelnet/pop/retrieval"
	"github.com/myelnet/pop/retrieval/deal"
)

// DefaultHashFunction used for generating CIDs of imported data
// although less convenient than SHA2, BLAKE2B seems to be more peformant in most cases
const DefaultHashFunction = uint64(mh.BLAKE2B_MIN + 31)

// ErrNoStrategy is returned when we try querying content without a read strategy
var ErrNoStrategy = errors.New("no strategy")

// Entry represents a link to an item in the DAG map
type Entry struct {
	// Cid is the content id of the represented
	Cid cid.Cid
	// Name is the entry path
	Name string
	// Size is the original file size
	Size int64
}

// Tx is an exchange transaction which may contain multiple DAGs to be exchanged with a set of connected peers
type Tx struct {
	ctx       context.Context
	cancelCtx context.CancelFunc
	// multistore is used to get a different store if this transaction is for content located in a
	// different store
	ms *multistore.MultiStore
	// storeID is the ID of the store used to read or write the content of this session
	storeID multistore.StoreID
	// store is the isolated blockstore and DAG instances for this session
	store *multistore.Store
	// entries is the cached reference to values used during the session
	entries map[string]Entry
	// disco is the discovery mechanism for finding content offers
	disco *GossipDisco
	// retriever manages the state of the transfer once we have a good offer
	retriever *retrieval.Client
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
	// sel is the selector used to select specific nodes only to retrieve. if not provided we select
	// all the nodes by default
	sel iprime.Node
	// done is the final message telling us we have received all the blocks and all is well. if the error
	// is not nil we've run out of options and nothing we can do at this time will get us the content.
	done chan error
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
	// Err exposes any error reported by the session during use
	Err error
}

// TxOption sets optional fields on a Tx struct
type TxOption func(*Tx)

// WithStrategy starts a new strategy worker to handle incoming discovery results
func WithStrategy(strategy SelectionStrategy) TxOption {
	return func(tx *Tx) {
		tx.worker = strategy(tx)
		tx.worker.Start()
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

// SetChunkSize allows changing the chunk size between put operation so different chunk sizes
// can be applied for different types of content in the same transaction
func (tx *Tx) SetChunkSize(size int64) {
	tx.chunkSize = size
}

// PutFile adds or replaces a file into the transaction
// it is _not_ thread safe
func (tx *Tx) PutFile(path string) error {
	if tx.Err != nil {
		return tx.Err
	}
	err := tx.add(path)
	if err != nil {
		return err
	}
	return tx.buildRoot()
}

func (tx *Tx) add(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	file, err := files.NewSerialFile(path, false, st)
	if err != nil {
		return err
	}
	key := KeyFromPath(path)

	switch f := file.(type) {
	case files.Directory:
		return tx.addDir(key, f)
	case files.File:
		return tx.addFile(key, f)
	default:
		return fmt.Errorf("unknown file type")
	}
}

func (tx *Tx) addFile(key string, f files.File) error {
	bufferedDS := ipldformat.NewBufferedDAG(tx.ctx, tx.store.DAG)

	prefix, err := merkledag.PrefixForCidVersion(1)
	if err != nil {
		return err
	}
	prefix.MhType = DefaultHashFunction

	params := helpers.DagBuilderParams{
		Maxlinks:   1024,
		RawLeaves:  true,
		CidBuilder: prefix,
		Dagserv:    bufferedDS,
	}

	db, err := params.New(chunk.NewSizeSplitter(f, tx.chunkSize))
	if err != nil {
		return err
	}

	n, err := balanced.Layout(db)
	if err != nil {
		return err
	}

	err = bufferedDS.Commit()
	if err != nil {
		return err
	}

	e := Entry{}
	e.Cid = n.Cid()
	e.Size, err = f.Size()
	if err != nil {
		return err
	}
	tx.entries[key] = e

	return nil
}

func (tx *Tx) addDir(key string, dir files.Directory) error {
	return fmt.Errorf("TODO")
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
			e.Name,
			e.Cid,
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

// assemble all the entries into a single dag Node
func (tx *Tx) assembleEntries() (ipld.Node, error) {
	// We need a single root CID so we make a list with the roots of all dagpb roots
	nb := basicnode.Prototype.Map.NewBuilder()
	as, err := nb.BeginMap(int64(len(tx.entries)))
	if err != nil {
		return nil, err
	}

	for k, v := range tx.entries {
		eas, err := as.AssembleEntry(k)
		if err != nil {
			return nil, err
		}
		// Each entry is also a map with 2 keys: Name and Link
		mas, err := eas.BeginMap(2)
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

// updateDAG stores the current contents of the index in an array to yield a single root CID
func (tx *Tx) buildRoot() error {
	lb := cidlink.LinkBuilder{
		Prefix: cid.Prefix{
			Version:  1,
			Codec:    0x71, // dag-cbor as per multicodec
			MhType:   DefaultHashFunction,
			MhLength: -1,
		},
	}

	var size int64
	for _, e := range tx.entries {
		size += e.Size
	}

	nd, err := tx.assembleEntries()
	if err != nil {
		return err
	}
	lnk, err := lb.Build(
		tx.ctx,
		ipld.LinkContext{},
		nd,
		tx.store.Storer,
	)
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
	return &DataRef{
		PayloadCID:  tx.root,
		StoreID:     tx.storeID,
		PayloadSize: tx.size,
	}
}

// Commit sends the transaction on the exchange
func (tx *Tx) Commit() error {
	if tx.Err != nil {
		return tx.Err
	}
	err := tx.index.SetRef(&DataRef{
		PayloadCID:  tx.root,
		StoreID:     tx.storeID,
		PayloadSize: tx.size,
	})
	if err != nil {
		return err
	}
	tx.dispatching = tx.repl.Dispatch(Request{
		PayloadCID: tx.root,
		Size:       uint64(tx.size),
	}, DefaultDispatchOptions)
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
		return tx.getUnixDAG(e.Cid, tx.store.DAG)
	}
	// Check the index if we may already have it from a different transaction
	if ref, err := tx.index.GetRef(tx.root); err == nil {
		// In this case we need to access a different store
		store, err := tx.ms.Get(ref.StoreID)
		if err != nil {
			return nil, err
		}
		return tx.loadFileEntry(k, store)
	}
	return tx.loadFileEntry(k, tx.store)
}

func (tx *Tx) loadFileEntry(k string, store *multistore.Store) (files.Node, error) {
	lk := cidlink.Link{Cid: tx.root}
	nb := basicnode.Prototype.Map.NewBuilder()

	err := lk.Load(tx.ctx, ipld.LinkContext{}, nb, store.Loader)
	if err != nil {
		return nil, err
	}
	nd := nb.Build()
	entry, err := nd.LookupByString(k)
	if err != nil {
		return nil, err
	}
	ln, err := entry.LookupByString("Link")
	if err != nil {
		return nil, err
	}
	l, err := ln.AsLink()
	if err != nil {
		return nil, err
	}
	flk := l.(cidlink.Link).Cid
	return tx.getUnixDAG(flk, store.DAG)
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

// DealSelection sends the selected offer with a channel to expect confirmation on
type DealSelection struct {
	Offer   deal.Offer
	confirm chan bool
}

// Incline accepts execution for an offer
func (ds DealSelection) Incline() {
	ds.confirm <- true
}

// Decline an offer
func (ds DealSelection) Decline() {
	ds.confirm <- false
}

// Query the discovery service for offers
func (tx *Tx) Query(key string) error {
	if tx.worker != nil {
		return tx.disco.Query(tx.ctx, tx.root, tx.worker.ReceiveResponse)
	}
	return ErrNoStrategy
}

// QueryFrom allows querying directly from a given peer
func (tx *Tx) QueryFrom(info peer.AddrInfo, key string) error {
	if tx.worker != nil {
		return tx.disco.QueryPeer(info, tx.root, tx.worker.ReceiveResponse)
	}
	return ErrNoStrategy
}

// Execute starts a retrieval operation for a given offer and returns the deal ID for that operation
func (tx *Tx) Execute(of deal.Offer) error {
	// Make sure our provider is in our peerstore
	tx.disco.AddAddrs(of.Provider.ID, of.Provider.Addrs)
	params, err := deal.NewParams(
		of.Response.MinPricePerByte,
		of.Response.MaxPaymentInterval,
		of.Response.MaxPaymentIntervalIncrease,
		AllSelector(),
		nil,
		of.Response.UnsealPrice,
	)
	if err != nil {
		return err
	}

	id, err := tx.retriever.Retrieve(
		tx.ctx,
		tx.root,
		params,
		of.Response.PieceRetrievalPrice(),
		of.Provider.ID,
		tx.clientAddr,
		of.Response.PaymentAddress,
		&tx.storeID,
	)
	if err != nil {
		return err
	}
	tx.ongoing <- DealRef{
		ID:    id,
		Offer: of,
	}
	select {
	case status := <-tx.errs:
		// For now we just return the error and assume the transfer is failed
		// we do have access to the status in order to try and restart the deal or something else
		return errors.New(deal.Statuses[status])
	case <-tx.ctx.Done():
		return tx.ctx.Err()
	}
}

// Confirm takes an offer and blocks to wait for user confirmation before returning true or false
func (tx *Tx) Confirm(of deal.Offer) bool {
	if tx.triage != nil {
		dch := make(chan bool, 1)
		tx.triage <- DealSelection{
			Offer:   of,
			confirm: dch,
		}
		select {
		case d := <-dch:
			return d
		case <-tx.ctx.Done():
			return false
		}
	}
	return true
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

// Done returns a channel that receives any resulting error from the latest operation
func (tx *Tx) Done() <-chan error {
	return tx.done
}

// Ongoing exposes the ongoing channel to get the reference of any in progress deals
func (tx *Tx) Ongoing() <-chan DealRef {
	return tx.ongoing
}

// Close removes any listeners and stream handlers related to a session
func (tx *Tx) Close() {
	tx.unsub()
	tx.cancelCtx()
}

// SetAddress to use for funding the retriebal
func (tx *Tx) SetAddress(addr address.Address) {
	tx.clientAddr = addr
}

// ErrUserDeniedOffer is returned when a user denies an offer
var ErrUserDeniedOffer = errors.New("user denied offer")

// OfferWorker is a generic interface to manage the lifecycle of offers
type OfferWorker interface {
	Start()
	ReceiveResponse(peer.AddrInfo, deal.QueryResponse)
	Close() []deal.Offer
}

// OfferExecutor exposes the methods required to execute offers
type OfferExecutor interface {
	Execute(deal.Offer) error
	Confirm(deal.Offer) bool
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
		offersIn:      make(chan deal.Offer),
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
			offersIn:      make(chan deal.Offer),
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
			offersIn:      make(chan deal.Offer),
			closing:       make(chan chan []deal.Offer, 1),
			numThreshold:  -1,
			timeThreshold: -1,
			priceCeiling:  amount,
		}
	}
}

type sessionWorker struct {
	executor OfferExecutor
	offersIn chan deal.Offer
	closing  chan chan []deal.Offer
	// numThreshold is the number of offers after which we can start execution
	// if -1 we execute as soon as the first offer gets in
	numThreshold int
	// timeThreshold is the duration after which we can start execution
	// if -1 we execute as soon as the first offer gets in
	timeThreshold time.Duration
	// priceCeiling is the price over which we are ignoring an offer for this session
	priceCeiling abi.TokenAmount
}

func (s sessionWorker) exec(offer deal.Offer, result chan error) {
	// Confirm may block until user sends a response
	// if we are blocking for a while the worker acts as if we'd started the transfer and will continue
	// buffering offers according to the given rules
	if s.executor.Confirm(offer) {
		result <- s.executor.Execute(offer)
		return
	}
	result <- ErrUserDeniedOffer
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
		var q []deal.Offer
		var execDone chan error
		for {
			var updates chan error
			if len(q) > 0 {
				// We only want to receive updates when we have some offers in the queue
				// otherwise we have no way to pick up execution with the next offer
				updates = execDone
			}
			select {
			case resc := <-s.closing:
				resc <- q
				return
			case of := <-s.offersIn:
				if useCeiling && of.Response.MinPricePerByte.LessThan(s.priceCeiling) {
					continue
				}
				if s.numThreshold < 0 && s.timeThreshold < 0 && execDone == nil {
					execDone = make(chan error, 1)
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
					execDone = make(chan error, 1)
					sortOffers(q)
					go s.exec(q[0], execDone)
					q = q[1:]
				}
			case <-delay:
				// We may already be executing if we've reached another threshold
				if execDone != nil {
					continue
				}
				execDone = make(chan error, 1)
				sortOffers(q)
				go s.exec(q[0], execDone)
				q = q[1:]
			case err := <-updates:
				// If the execution returns an error we assume it is not fixable
				// and automatically try the next offer
				if err != nil && len(q) > 0 {
					execDone = make(chan error, 1)
					go s.exec(q[0], execDone)
					q = q[1:]
				}
			}
		}
	}()
}

// Close the selection returns the last unused offers if any
func (s sessionWorker) Close() []deal.Offer {
	resc := make(chan []deal.Offer)
	s.closing <- resc
	return <-resc
}

// ReceiveResponse sends a new offer to the queue
func (s sessionWorker) ReceiveResponse(p peer.AddrInfo, res deal.QueryResponse) {
	// This never blocks as our queue is always receiving and decides when to drop offers
	s.offersIn <- deal.Offer{
		Provider: p,
		Response: res,
	}
}

func sortOffers(offers []deal.Offer) {
	sort.Slice(offers, func(i, j int) bool {
		return offers[i].Response.MinPricePerByte.LessThan(offers[j].Response.MinPricePerByte)
	})
}

// KeyFromPath returns a key name from a file path
func KeyFromPath(p string) string {
	_, name := filepath.Split(p)
	return name
}
