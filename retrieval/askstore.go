package retrieval

import (
	"sync"

	"github.com/ipfs/go-cid"
	"github.com/myelnet/pop/retrieval/deal"
)

// AskStore is actually for storing QueryResponse objects
// we don't currently need to persist this beyond node restart
type AskStore struct {
	lk   sync.RWMutex
	asks map[cid.Cid]deal.QueryResponse
}

// SetAsk stores retrieval provider's ask for a given content root
func (s *AskStore) SetAsk(k cid.Cid, ask deal.QueryResponse) error {
	s.lk.Lock()
	defer s.lk.Unlock()

	s.asks[k] = ask
	return nil
}

// GetAsk returns the current retrieval ask for a given content root, or nil if one does not exist.
func (s *AskStore) GetAsk(k cid.Cid) deal.QueryResponse {
	s.lk.RLock()
	defer s.lk.RUnlock()

	return s.asks[k]
}
