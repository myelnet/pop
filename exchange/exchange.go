package exchange

import (
	"bytes"
	"context"
	"fmt"

	"github.com/filecoin-project/go-address"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-multistore"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
	"github.com/libp2p/go-libp2p-core/host"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/myelnet/pop/filecoin"
	"github.com/myelnet/pop/payments"
	"github.com/myelnet/pop/retrieval"
	"github.com/myelnet/pop/retrieval/client"
	"github.com/myelnet/pop/retrieval/deal"
	"github.com/myelnet/pop/wallet"
)

// Exchange is a financially incentivized IPLD  block exchange
// powered by Filecoin and IPFS
type Exchange struct {
	h    host.Host
	ds   datastore.Batching
	opts Options
	// pubsub topics
	tops []*pubsub.Topic
	// wallet interface
	w wallet.Driver
	// retrieval handles all metered data transfers
	rtv retrieval.Manager
	// Messaging service
	mg *Messaging
	// Replication scheme
	rpl *Replication
	// Persist Metadata about content
	meta *MetadataStore
}

// New creates a long running exchange process from a libp2p host, an IPFS datastore and some optional
// modules which are provided by default
func New(ctx context.Context, h host.Host, ds datastore.Batching, opts Options) (*Exchange, error) {
	opts, err := opts.fillDefaults(ctx, h, ds)
	if err != nil {
		return nil, err
	}
	tops, subs, err := opts.joinRegions()
	if err != nil {
		return nil, err
	}
	metads := &MetadataStore{
		ds: namespace.Wrap(ds, datastore.NewKey("/metadata")),
		ms: opts.MultiStore,
	}
	// register a pubsub topic for each region
	exch := &Exchange{
		h:    h,
		ds:   ds,
		opts: opts,
		tops: tops,
		meta: metads,
		w:    wallet.NewFromKeystore(opts.Keystore, opts.FilecoinAPI),
		mg:   NewMessaging(h, NetMetadata(opts.GossipTracer)),
	}
	// Make a new default key to be sure we have an address where to receive our payments
	if exch.w.DefaultAddress() == address.Undef {
		_, err = exch.w.NewKey(ctx, wallet.KTSecp256k1)
		if err != nil {
			return nil, err
		}
	}
	exch.rpl = NewReplication(h, metads, opts.DataTransfer, exch.mg, opts.Regions)
	exch.rtv, err = retrieval.New(
		ctx,
		opts.MultiStore,
		ds,
		payments.New(ctx, opts.FilecoinAPI, exch.w, ds, opts.Blockstore),
		opts.DataTransfer,
		metads,
		h.ID(),
	)
	if err != nil {
		return nil, err
	}
	if err := exch.rpl.Start(ctx); err != nil {
		return nil, err
	}
	for _, sub := range subs {
		go exch.pump(ctx, sub)
	}
	return exch, nil
}

func (e *Exchange) pump(ctx context.Context, sub *pubsub.Subscription) {
	r := RegionFromTopic(sub.Topic())
	for {
		msg, err := sub.Next(ctx)
		if err != nil {
			return
		}
		if msg.ReceivedFrom == e.h.ID() {
			continue
		}
		m := new(deal.Query)
		if err := m.UnmarshalCBOR(bytes.NewReader(msg.Data)); err != nil {
			continue
		}

		store, err := e.meta.GetStore(m.PayloadCID)
		if err != nil {
			// TODO: we need to log when we couldn't find some content so we can try looking for it
			fmt.Println("no store found for", m.PayloadCID)
			continue
		}
		// DAGStat is both a way of checking if we have the blocks and returning its size
		// TODO: support selector in Query
		stats, err := DAGStat(ctx, store.Bstore, m.PayloadCID, AllSelector())
		if err != nil {
			fmt.Println("failed to get content stat", err, e.h.ID())
		}
		// We don't have the block we don't even reply to avoid taking bandwidth
		// On the client side we assume no response means they don't have it
		if err == nil && stats.Size > 0 {
			qs, err := e.mg.NewQueryStream(msg.ReceivedFrom)
			if err != nil {
				fmt.Println("failed to create response query stream", err)
				continue
			}
			addrs, err := e.mg.Addrs()
			if err != nil {
				continue
			}
			mid := pubsub.DefaultMsgIdFn(msg.Message)
			// Our message payload includes the message ID and the recipient peer address
			// The index indicates where to slice the string to extract both values
			p := fmt.Sprintf("%d%s%s", len(mid), mid, string(addrs[0].Bytes()))
			answer := deal.QueryResponse{
				Status:                     deal.QueryResponseAvailable,
				Size:                       uint64(stats.Size),
				PaymentAddress:             e.w.DefaultAddress(),
				MinPricePerByte:            r.PPB, // TODO: dynamic pricing
				MaxPaymentInterval:         deal.DefaultPaymentInterval,
				MaxPaymentIntervalIncrease: deal.DefaultPaymentIntervalIncrease,
				Message:                    p,
			}
			if err := qs.WriteQueryResponse(answer); err != nil {
				fmt.Printf("retrieval query: WriteCborRPC: %s\n", err)
				continue
			}
			// We need to remember the offer we made so we can validate against it once
			// clients start the retrieval
			e.rtv.Provider().SetAsk(m.PayloadCID, answer)
		}

	}
}

// NewSession returns a new retrieval session
func (e *Exchange) NewSession(ctx context.Context, root cid.Cid, strategy SelectionStrategy) *Session {
	// This cancel allows us to shutdown the retrieval process with the session if needed
	ctx, cancel := context.WithCancel(ctx)
	// Track when the session is completed
	done := make(chan error)
	// Track any issues with the transfer
	err := make(chan deal.Status)
	// Subscribe to client events to send to the channel
	cl := e.rtv.Client()
	unsubscribe := cl.SubscribeToEvents(func(event client.Event, state deal.ClientState) {
		switch state.Status {
		case deal.StatusCompleted:
			done <- nil
			return
		case deal.StatusCancelled, deal.StatusErrored:
			err <- state.Status
			return
		}
	})
	session := &Session{
		ctx:          ctx,
		cancelCtx:    cancel,
		regionTopics: e.tops,
		net:          e.mg,
		root:         root,
		retriever:    cl,
		clientAddr:   e.w.DefaultAddress(),
		done:         done,
		err:          err,
		ongoing:      make(chan DealRef),
		selecting:    make(chan DealSelection),
		unsub:        unsubscribe,
		// We create a fresh new store for this session
		storeID: e.opts.MultiStore.Next(),
	}
	session.worker = strategy(session)
	session.worker.Start()
	session.net.SetOfferReceiver(session.worker)
	return session
}

// Wallet returns the wallet API
func (e *Exchange) Wallet() wallet.Driver {
	return e.w
}

// Registrar returns the metadata store API
func (e *Exchange) Registrar() *MetadataStore {
	return e.meta
}

// DataTransfer returns the data transfer manager instance for this exchange
func (e *Exchange) DataTransfer() datatransfer.Manager {
	return e.opts.DataTransfer
}

// FilecoinAPI returns the FilecoinAPI instance for this exchange
// may be nil so check with IsFilecoinOnline first
func (e *Exchange) FilecoinAPI() filecoin.API {
	return e.opts.FilecoinAPI
}

// IsFilecoinOnline returns whether we are connected to a Filecoin blockchain gateway
func (e *Exchange) IsFilecoinOnline() bool {
	return e.opts.FilecoinAPI != nil
}

// Retrieval exposes the retrieval manager module
func (e *Exchange) Retrieval() retrieval.Manager {
	return e.rtv
}

// ListMiners returns a list of miners based on the regions this exchange is part of
// We keep a context as this could also query a remote service or API
func (e *Exchange) ListMiners(ctx context.Context) ([]address.Address, error) {
	var strList []string
	for _, r := range e.opts.Regions {
		// Global region is already a list of miners in all regions
		if r.Name == "Global" {
			strList = r.StorageMiners
			break
		}
		strList = append(strList, r.StorageMiners...)
	}
	var addrList []address.Address
	for _, s := range strList {
		addr, err := address.NewFromString(s)
		if err != nil {
			return addrList, err
		}
		addrList = append(addrList, addr)
	}
	return addrList, nil
}

// GetStoreID exposes a method to get the store ID used by a given CID
func (e *Exchange) GetStoreID(id cid.Cid) (multistore.StoreID, error) {
	return e.meta.GetStoreID(id)
}

// Get sends a Get request and executes the receive offers
func (e *Exchange) Get(ctx context.Context, root cid.Cid) error {
	return nil
}

// PutOptions describes how the Put action should be performed
type PutOptions struct {
	// Local only caches the given content on the local node
	Local bool
	// StoreID is the ID of the store used to import the content
	StoreID multistore.StoreID
}

// Put sends a Put request and allows recipients to pull the blocks
func (e *Exchange) Put(ctx context.Context, root cid.Cid, opts PutOptions) error {
	if opts.Local {
		err := e.meta.Register(root, opts.StoreID)
		if err != nil {
			return err
		}
	}
	return nil
}
