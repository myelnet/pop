package supply

import (
	"encoding/json"

	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
)

const (
	// KStoreID is the local store ID where that content is stored
	KStoreID = "store"
	// KSize is the full content size
	KSize = "size"
)

// ContentRecord is a map of labels associated with a content ID
// lind of like a mini database for that content activity
type ContentRecord struct {
	Labels map[string]string
}

// Store for content records
type Store struct {
	ds datastore.Batching
}

// PutRecord creates a new record for a given content ID
func (s *Store) PutRecord(id cid.Cid, r *ContentRecord) error {
	rec, err := json.Marshal(r)
	if err != nil {
		return err
	}

	return s.ds.Put(datastore.NewKey(id.String()), rec)
}

// GetRecord returns a record for a given content ID
func (s *Store) GetRecord(id cid.Cid) (*ContentRecord, error) {
	r, err := s.ds.Get(datastore.NewKey(id.String()))
	if err != nil {
		return nil, err
	}

	var rec ContentRecord
	if err := json.Unmarshal(r, &rec); err != nil {
		return nil, err
	}

	return &rec, nil
}

// AddLabel adds a label to a ContentRecord
func (s *Store) AddLabel(id cid.Cid, key, value string) error {
	dsk := datastore.NewKey(id.String())

	r, err := s.ds.Get(dsk)
	if err != nil {
		return err
	}

	var rec ContentRecord
	if err := json.Unmarshal(r, &rec); err != nil {
		return err
	}

	rec.Labels[key] = value

	r, err = json.Marshal(&rec)
	if err != nil {
		return err
	}

	return s.ds.Put(dsk, r)
}

// RemoveRecord removes a record entirely from our manifest
func (s *Store) RemoveRecord(id cid.Cid) error {
	if err := s.ds.Delete(datastore.NewKey(id.String())); err != nil {
		return err
	}
	return nil
}
