package retrieval

import (
	"sync"

	peer "github.com/libp2p/go-libp2p-peer"
	"github.com/myelnet/go-hop-exchange/retrieval/deal"
)

// AskStore is actually for storing QueryResponse objects
// we don't currently need to persist this beyond node restart
type AskStore struct {
	lk   sync.RWMutex
	asks map[peer.ID]deal.QueryResponse
}

// SetAsk stores retrieval provider's ask
func (s *AskStore) SetAsk(k peer.ID, ask deal.QueryResponse) error {
	s.lk.Lock()
	defer s.lk.Unlock()

	s.asks[k] = ask
	return nil
}

// GetAsk returns the current retrieval ask, or nil if one does not exist.
func (s *AskStore) GetAsk(k peer.ID) deal.QueryResponse {
	s.lk.RLock()
	defer s.lk.RUnlock()

	return s.asks[k]
}
