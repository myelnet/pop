package retrieval

import (
	"sync"

	"github.com/filecoin-project/go-state-types/big"
	"github.com/ipfs/go-cid"
	"github.com/myelnet/pop/retrieval/deal"
)

// AskStore is actually for storing Offers we made so we can validate requests against them
// we don't currently need to persist this beyond node restart
type AskStore struct {
	lk   sync.RWMutex
	asks map[cid.Cid]deal.Offer
}

// SetAsk stores retrieval provider's ask for a given content root
func (s *AskStore) SetAsk(k cid.Cid, ask deal.Offer) error {
	s.lk.Lock()
	defer s.lk.Unlock()

	s.asks[k] = ask
	return nil
}

// GetAsk returns the current retrieval ask for a given content root, or nil if one does not exist.
func (s *AskStore) GetAsk(k cid.Cid) deal.Offer {
	s.lk.RLock()
	defer s.lk.RUnlock()

	a, ok := s.asks[k]
	if !ok {
		// we don't have a specific ask for this CID so let's returns a default
		// response. TODO: need more context as to why this content is being retrieved,
		// which region, peer etc.
		return deal.Offer{
			MinPricePerByte:            big.Zero(),
			MaxPaymentInterval:         deal.DefaultPaymentInterval,
			MaxPaymentIntervalIncrease: deal.DefaultPaymentIntervalIncrease,
		}
	}
	return a
}
