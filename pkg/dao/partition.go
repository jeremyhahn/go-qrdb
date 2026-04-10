// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-dragondb.
//
// go-dragondb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package dao

import (
	"context"
	"errors"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// PartitionManager defines the interface for managing storage partitions.
// This is implemented by partition.Manager but defined here to avoid
// import cycles.
type PartitionManager interface {
	// GetFromPartition retrieves a value from a specific partition.
	GetFromPartition(partitionName string, key []byte) ([]byte, error)

	// SetToPartition stores a value in a specific partition.
	SetToPartition(partitionName string, key, value []byte) error

	// DeleteFromPartition removes a key from a specific partition.
	DeleteFromPartition(partitionName string, key []byte) error

	// GetPartition returns the underlying storage for a partition.
	GetPartition(name string) (storage.Storage, error)

	// DefaultPartition returns the default partition name.
	DefaultPartition() string

	// PartitionNames returns all partition names.
	PartitionNames() []string
}

// PartitionedKVStore wraps a partition manager to provide the KVStore interface.
// It routes all operations to a specific partition.
type PartitionedKVStore struct {
	manager   PartitionManager
	partition string
}

// Ensure PartitionedKVStore implements KVStore interface.
var _ kvstore.KVStore = (*PartitionedKVStore)(nil)

// NewPartitionedKVStore creates a new PartitionedKVStore for the specified partition.
//
// Example:
//
//	mgr, _ := partition.NewManager(...)
//	kvStore := dao.NewPartitionedKVStore(mgr, "users")
func NewPartitionedKVStore(mgr PartitionManager, partitionName string) *PartitionedKVStore {
	return &PartitionedKVStore{
		manager:   mgr,
		partition: partitionName,
	}
}

// PartitionName returns the name of the partition this store routes to.
func (s *PartitionedKVStore) PartitionName() string {
	return s.partition
}

// Put stores a key-value pair in the partition.
func (s *PartitionedKVStore) Put(_ context.Context, key string, value []byte) error {
	if s.manager == nil {
		return &PartitionNotConfiguredError{Operation: "Put"}
	}
	return s.manager.SetToPartition(s.partition, []byte(key), value)
}

// Get retrieves a value by key from the partition.
func (s *PartitionedKVStore) Get(_ context.Context, key string) ([]byte, error) {
	if s.manager == nil {
		return nil, &PartitionNotConfiguredError{Operation: "Get"}
	}
	return s.manager.GetFromPartition(s.partition, []byte(key))
}

// Delete removes a key-value pair from the partition.
func (s *PartitionedKVStore) Delete(_ context.Context, key string) error {
	if s.manager == nil {
		return &PartitionNotConfiguredError{Operation: "Delete"}
	}
	return s.manager.DeleteFromPartition(s.partition, []byte(key))
}

// Scan iterates over all key-value pairs matching the given prefix in the partition.
func (s *PartitionedKVStore) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	if s.manager == nil {
		return &PartitionNotConfiguredError{Operation: "Scan"}
	}

	store, err := s.manager.GetPartition(s.partition)
	if err != nil {
		return err
	}

	prefixBytes := []byte(prefix)

	// Create iterator with prefix bounds
	opts := &storage.IteratorOptions{
		LowerBound: prefixBytes,
		UpperBound: prefixEndKey(prefixBytes),
	}

	iter := store.NewIterator(opts)
	defer func() { _ = iter.Close() }()

	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := fn(string(iter.Key()), iter.Value()); err != nil {
			return err
		}
	}

	return iter.Error()
}

// List returns all keys matching the given prefix in the partition.
func (s *PartitionedKVStore) List(ctx context.Context, prefix string) ([]string, error) {
	if s.manager == nil {
		return nil, &PartitionNotConfiguredError{Operation: "List"}
	}

	var keys []string
	err := s.Scan(ctx, prefix, func(key string, _ []byte) error {
		keys = append(keys, key)
		return nil
	})

	return keys, err
}

// Exists checks whether a key exists in the partition.
func (s *PartitionedKVStore) Exists(ctx context.Context, key string) (bool, error) {
	if s.manager == nil {
		return false, &PartitionNotConfiguredError{Operation: "Exists"}
	}

	data, err := s.Get(ctx, key)
	if err != nil {
		return false, err
	}
	return data != nil, nil
}

// RegisterEntityIndexes is a no-op for partitioned stores.
// Partition-based storage does not support engine-level indexes.
func (s *PartitionedKVStore) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error {
	return nil
}

// QueryIndex returns nil for partitioned stores that lack index support.
func (s *PartitionedKVStore) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}

// ScanIndex returns nil for partitioned stores that lack index support.
func (s *PartitionedKVStore) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

// QueryCompoundIndex returns nil for partitioned stores that lack index support.
func (s *PartitionedKVStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}

// ScanCompoundIndex returns nil for partitioned stores that lack index support.
func (s *PartitionedKVStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

// prefixEndKey returns the end key for a prefix scan (exclusive upper bound).
// It increments the last byte of the prefix, or returns nil if all bytes are 0xFF.
func prefixEndKey(prefix []byte) []byte {
	if len(prefix) == 0 {
		return nil
	}

	end := make([]byte, len(prefix))
	copy(end, prefix)

	// Find rightmost byte that can be incremented
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] < 0xFF {
			end[i]++
			return end[:i+1]
		}
	}

	// All bytes are 0xFF, no upper bound
	return nil
}

// PartitionNotConfiguredError indicates the partition manager is not configured.
type PartitionNotConfiguredError struct {
	Operation string
}

func (e *PartitionNotConfiguredError) Error() string {
	return "dao: partition manager not configured for operation: " + e.Operation
}

// PartitionNotFoundError indicates the requested partition does not exist.
type PartitionNotFoundError struct {
	Partition string
}

func (e *PartitionNotFoundError) Error() string {
	return "dao: partition not found: " + e.Partition
}

// CreatePartitionedProvider creates a PartitionedKVStoreProvider from a PartitionManager.
// This is a convenience function for creating the provider function needed by FactoryConfig.
//
// Example:
//
//	mgr, _ := partition.NewManager(...)
//	factory := dao.NewFactoryWithConfig(&dao.FactoryConfig{
//	    PartitionedProvider: dao.CreatePartitionedProvider(mgr),
//	    DefaultPartition:    mgr.DefaultPartition(),
//	})
func CreatePartitionedProvider(mgr PartitionManager) PartitionedKVStoreProvider {
	// Cache stores per partition to avoid creating new wrappers on each call
	stores := make(map[string]*PartitionedKVStore)

	return func(partitionName string, _ uint64) kvstore.KVStore {
		if store, ok := stores[partitionName]; ok {
			return store
		}

		store := NewPartitionedKVStore(mgr, partitionName)
		stores[partitionName] = store
		return store
	}
}

// IsPartitionError returns true if the error is a partition-related error.
func IsPartitionError(err error) bool {
	if err == nil {
		return false
	}

	// Use errors.As to handle wrapped errors (standard library for custom types)
	var partNotConfigured *PartitionNotConfiguredError
	if errors.As(err, &partNotConfigured) {
		return true
	}
	var partNotFound *PartitionNotFoundError
	if errors.As(err, &partNotFound) {
		return true
	}

	// Check for wrapped partition errors from the partition package
	var dbErr *dberrors.DragonError
	if ok := dberrors.As(err, &dbErr); ok {
		return dbErr.Code == dberrors.ErrNotFound
	}

	return false
}
