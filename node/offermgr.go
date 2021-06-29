package node

import (
	"errors"
	"sync"

	"github.com/ipfs/go-cid"
	"github.com/myelnet/pop/retrieval/deal"
)

// OfferMgr keeps track of offers so we can reuse existing connections
// and payment channels with peers
type OfferMgr struct {
	mu     sync.Mutex
	offers map[cid.Cid]deal.Offer
}

// NewOfferMgr makes a new inmemory offer store
func NewOfferMgr() *OfferMgr {
	return &OfferMgr{
		offers: make(map[cid.Cid]deal.Offer),
	}
}

// SetOffer stores a retrieval provider's offer for a given root CID.
// it currently assumes the Offer is valid for the entire DAG.
func (omg *OfferMgr) SetOffer(k cid.Cid, o deal.Offer) error {
	omg.mu.Lock()
	defer omg.mu.Unlock()

	omg.offers[k] = o
	return nil
}

// GetOffer returns a valid offer for a given root CID if we have queries about it before
func (omg *OfferMgr) GetOffer(k cid.Cid) (deal.Offer, error) {
	omg.mu.Lock()
	defer omg.mu.Unlock()

	o, ok := omg.offers[k]
	if !ok {
		return deal.Offer{}, errors.New("no existing offer")
	}

	return o, nil
}
