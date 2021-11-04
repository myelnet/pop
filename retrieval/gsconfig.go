package retrieval

import (
	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/ipld/go-ipld-prime"
	peer "github.com/libp2p/go-libp2p-peer"
	"github.com/myelnet/go-multistore"
	"github.com/myelnet/pop/retrieval/deal"
	"github.com/rs/zerolog/log"
)

// StoreGetter retrieves the store for a given proposal cid
type StoreGetter interface {
	Get(otherPeer peer.ID, dealID deal.ID) (*multistore.Store, error)
}

// StoreConfigurableTransport defines the methods needed to
// configure a data transfer transport use a unique store for a given request
type StoreConfigurableTransport interface {
	UseStore(datatransfer.ChannelID, ipld.LinkSystem) error
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
			return
		}
		if store == nil {
			return
		}
		err = gsTransport.UseStore(channelID, store.LinkSystem)
		if err != nil {
			log.Error().Err(err).Msg("attempting to configure data store")
		}
	}
}

type dualStoreGetter struct {
	c *Client
	p *Provider
}

// Our transport handles both client and provider as a result we need to try both states see which one works
// TODO: figure out how to improve so we don't cause unnecessary reads on the client side
func (dsg *dualStoreGetter) Get(pid peer.ID, did deal.ID) (*multistore.Store, error) {
	var cstate deal.ClientState
	err := dsg.c.stateMachines.Get(did).Get(&cstate)
	if err == nil {
		return dsg.c.multiStore.Get(*cstate.StoreID)
	}
	return nil, err
}
