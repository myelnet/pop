package exchange

import (
	"errors"
	"sync"

	"github.com/ipfs/go-cid"
	"github.com/myelnet/pop/retrieval/deal"
)

// Offers stores offers for a given content Cid
// used by providers for storing self generated offers or clients
// it may also be used for determining pricing etc
type Offers struct {
	regions []Region
	lk      sync.RWMutex
	byCid   map[cid.Cid]deal.Offer
}

// NewOffers creates a new instance of the Offer store for a given list of regions
// (at this point regions are used for determining pricing
func NewOffers(regions []Region) *Offers {
	return &Offers{
		regions: regions,
		byCid:   make(map[cid.Cid]deal.Offer),
	}
}

// SetOfferForCid stores an offer for a given content ID
func (o *Offers) SetOfferForCid(k cid.Cid, offer deal.Offer) error {
	o.lk.Lock()
	defer o.lk.Unlock()

	o.byCid[k] = offer
	return nil
}

// FindOfferByCid returns an offer if available for a given cid or an error if none if found
func (o *Offers) FindOfferByCid(k cid.Cid) (deal.Offer, error) {
	o.lk.RLock()
	defer o.lk.RUnlock()

	offer, ok := o.byCid[k]
	if !ok {
		return offer, errors.New("offer not found")
	}
	return offer, nil
}

// GetOfferForCid returns a specific offer for the given content ID. If no offer has been registered
// it will return a default offer based on the regions.
func (o *Offers) GetOfferForCid(k cid.Cid) deal.Offer {
	offer, err := o.FindOfferByCid(k)
	if err != nil {
		return deal.Offer{
			MinPricePerByte:            o.regions[0].PPB,
			MaxPaymentInterval:         deal.DefaultPaymentInterval,
			MaxPaymentIntervalIncrease: deal.DefaultPaymentIntervalIncrease,
		}
	}
	return offer
}

// RemoveOffer clear the offer for a given root CID for examplie if the offer is expired
func (o *Offers) RemoveOffer(k cid.Cid) error {
	o.lk.Lock()
	defer o.lk.Unlock()

	_, ok := o.byCid[k]
	if !ok {
		return errors.New("no existing offer")
	}

	delete(o.byCid, k)
	return nil
}
