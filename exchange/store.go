package exchange

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/filecoin-project/go-multistore"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
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

// MetadataStore for content records
type MetadataStore struct {
	ds datastore.Batching
	ms *multistore.MultiStore
}

// NewMetadataStore sets up the metadata store at the right namespace
func NewMetadataStore(ds datastore.Batching, ms *multistore.MultiStore) *MetadataStore {
	return &MetadataStore{
		ds: namespace.Wrap(ds, datastore.NewKey("/metadata")),
		ms: ms,
	}
}

// Register a new content record in our supply
func (s *MetadataStore) Register(key cid.Cid, sid multistore.StoreID) error {
	// Store a record of the content in our supply
	return s.PutRecord(key, &ContentRecord{Labels: map[string]string{
		KStoreID: fmt.Sprintf("%d", sid),
	}})
}

// GetStoreID returns the StoreID of the store which has the given content
func (s *MetadataStore) GetStoreID(id cid.Cid) (multistore.StoreID, error) {
	rec, err := s.GetRecord(id)
	if err != nil {
		return 0, err
	}
	sid, ok := rec.Labels[KStoreID]
	if !ok {
		return 0, fmt.Errorf("storeID not found")
	}
	storeID, err := strconv.ParseUint(sid, 10, 64)
	if err != nil {
		return 0, err
	}
	return multistore.StoreID(storeID), nil
}

// GetStore returns the correct multistore associated with a data CID
func (s *MetadataStore) GetStore(id cid.Cid) (*multistore.Store, error) {
	storeID, err := s.GetStoreID(id)
	if err != nil {
		return nil, err
	}
	store, err := s.ms.Get(storeID)
	if err != nil {
		return nil, err
	}
	return store, nil
}

// RemoveContent removes all content linked to a root CID by completed dropping the store
func (s *MetadataStore) RemoveContent(root cid.Cid) error {
	storeID, err := s.GetStoreID(root)
	if err != nil {
		return err
	}
	err = s.ms.Delete(storeID)
	if err != nil {
		return err
	}
	return s.RemoveRecord(root)
}

// PutRecord creates a new record for a given content ID
func (s *MetadataStore) PutRecord(id cid.Cid, r *ContentRecord) error {
	rec, err := json.Marshal(r)
	if err != nil {
		return err
	}

	return s.ds.Put(datastore.NewKey(id.String()), rec)
}

// GetRecord returns a record for a given content ID
func (s *MetadataStore) GetRecord(id cid.Cid) (*ContentRecord, error) {
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
func (s *MetadataStore) AddLabel(id cid.Cid, key, value string) error {
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
func (s *MetadataStore) RemoveRecord(id cid.Cid) error {
	if err := s.ds.Delete(datastore.NewKey(id.String())); err != nil {
		return err
	}
	return nil
}
