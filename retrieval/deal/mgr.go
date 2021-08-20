package deal

import (
	"errors"
	"sync"

	"github.com/filecoin-project/go-state-types/abi"
	"github.com/ipfs/go-cid"
)

// Mgr is a deal manager implementation which keeps track of offers and ongoing deal balances
// which may encompass multiple transfers.
type Mgr struct {
	// default provider price to be used if no offer was set for a dag root
	providerPrice abi.TokenAmount
	lk            sync.RWMutex
	offers        map[cid.Cid]Offer
	funds         map[cid.Cid]abi.TokenAmount
}

// NewManager creates a new deal manager instance for a given default provider price
// the provider price will define the baseline price per byte at which the provider node will serve deals
func NewManager(price abi.TokenAmount) *Mgr {
	return &Mgr{
		providerPrice: price,
		offers:        make(map[cid.Cid]Offer),
		funds:         make(map[cid.Cid]abi.TokenAmount),
	}
}

// SetOfferForCid stores an offer for a given content ID
func (mgr *Mgr) SetOfferForCid(k cid.Cid, offer Offer) error {
	mgr.lk.Lock()
	defer mgr.lk.Unlock()

	mgr.offers[k] = offer
	return nil
}

// FindOfferByCid returns an offer if available for a given cid or an error if none if found
func (mgr *Mgr) FindOfferByCid(k cid.Cid) (Offer, error) {
	mgr.lk.RLock()
	defer mgr.lk.RUnlock()

	offer, ok := mgr.offers[k]
	if !ok {
		return offer, errors.New("offer not found")
	}
	return offer, nil
}

// GetOfferForCid returns a specific offer for the given content ID. If no offer has been registered
// it will return a default offer based on the default provier price.
func (mgr *Mgr) GetOfferForCid(k cid.Cid) Offer {
	offer, err := mgr.FindOfferByCid(k)
	if err != nil {
		return Offer{
			MinPricePerByte:            mgr.providerPrice,
			MaxPaymentInterval:         DefaultPaymentInterval,
			MaxPaymentIntervalIncrease: DefaultPaymentIntervalIncrease,
		}
	}
	return offer
}

// RemoveOffer clear the offer for a given root CID for examplie if the offer is expired
func (mgr *Mgr) RemoveOffer(k cid.Cid) error {
	mgr.lk.Lock()
	defer mgr.lk.Unlock()

	_, ok := mgr.offers[k]
	if !ok {
		return errors.New("no existing offer")
	}

	delete(mgr.offers, k)
	return nil
}

// SetFundsForCid saves a token amount for a given root CID
// it will overried any existing amount
func (mgr *Mgr) SetFundsForCid(k cid.Cid, funds abi.TokenAmount) {
	mgr.funds[k] = funds
}

// GetFundsForCid returns a token amount available for a given cid or 0
func (mgr *Mgr) GetFundsForCid(k cid.Cid) abi.TokenAmount {
	amt, ok := mgr.funds[k]
	if !ok {
		return abi.NewTokenAmount(0)
	}
	return amt
}
