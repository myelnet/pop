package retrieval

import (
	"context"
	"fmt"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-multistore"
	"github.com/ipld/go-ipld-prime"
	peer "github.com/libp2p/go-libp2p-peer"
	"github.com/myelnet/go-hop-exchange/retrieval/deal"
)

// StoreGetter retrieves the store for a given proposal cid
type StoreGetter interface {
	Get(otherPeer peer.ID, dealID deal.ID) (*multistore.Store, error)
}

// StoreConfigurableTransport defines the methods needed to
// configure a data transfer transport use a unique store for a given request
type StoreConfigurableTransport interface {
	UseStore(datatransfer.ChannelID, ipld.Loader, ipld.Storer) error
}

// TransportConfigurer configurers the graphsync transport to use a custom blockstore per deal
func TransportConfigurer(thisPeer peer.ID, storeGetter StoreGetter) datatransfer.TransportConfigurer {
	return func(channelID datatransfer.ChannelID, voucher datatransfer.Voucher, transport datatransfer.Transport) {
		dealProposal, ok := deal.ProposalFromVoucher(voucher)
		if !ok {
			return
		}
		gsTransport, ok := transport.(StoreConfigurableTransport)
		if !ok {
			return
		}
		otherPeer := channelID.OtherParty(thisPeer)
		store, err := storeGetter.Get(otherPeer, dealProposal.ID)
		if err != nil {
			fmt.Println("attempting to configure data store:", err)
			return
		}
		if store == nil {
			return
		}
		err = gsTransport.UseStore(channelID, store.Loader, store.Storer)
		if err != nil {
			fmt.Println("attempting to configure data store:", err)
		}
	}
}

type clientStoreGetter struct {
	c *Client
}

func (csg *clientStoreGetter) Get(pid peer.ID, did deal.ID) (*multistore.Store, error) {
	var state deal.ClientState
	err := csg.c.stateMachines.Get(did).Get(&state)
	if err != nil {
		return nil, err
	}
	if state.StoreID == nil {
		return nil, nil
	}
	return csg.c.multiStore.Get(*state.StoreID)
}

type providerStoreGetter struct {
	p *Provider
}

func (psg *providerStoreGetter) Get(pid peer.ID, did deal.ID) (*multistore.Store, error) {
	var state deal.ProviderState
	err := psg.p.stateMachines.GetSync(context.TODO(), deal.ProviderDealIdentifier{Receiver: pid, DealID: did}, &state)
	if err != nil {
		return nil, err
	}
	return psg.p.multiStore.Get(state.StoreID)
}
