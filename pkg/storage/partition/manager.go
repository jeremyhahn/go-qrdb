// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package partition

import (
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// Compile-time interface assertion.
var _ storage.Storage = (*Manager)(nil)

// Manager implements storage.Storage by routing operations to the correct
// partition based on key hash. Partitions are created eagerly at construction.
type Manager struct {
	router     Router
	partitions []storage.Storage
	config     *Config
	closed     atomic.Bool
}

// NewManager creates a new partition manager. All partitions are created eagerly
// using the provided factory and configuration.
func NewManager(router Router, factory Factory, config *Config) (*Manager, error) {
	if config == nil {
		return nil, &ConfigError{Field: "config", Message: "is required"}
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if router == nil {
		return nil, &ConfigError{Field: "router", Message: "is required"}
	}
	if factory == nil {
		return nil, &ConfigError{Field: "factory", Message: "is required"}
	}

	count := router.PartitionCount()
	partitions := make([]storage.Storage, count)

	for i := uint32(0); i < count; i++ {
		s, err := factory.Create(i, config)
		if err != nil {
			// Close any already-created partitions.
			for j := uint32(0); j < i; j++ {
				_ = partitions[j].Close()
			}
			return nil, &PartitionCreateError{PartitionID: i, Err: err}
		}
		partitions[i] = s
	}

	return &Manager{
		router:     router,
		partitions: partitions,
		config:     config,
	}, nil
}

// partition returns the storage for the given key.
func (m *Manager) partition(key []byte) storage.Storage {
	id := m.router.Route(key)
	return m.partitions[id]
}

// partitionByID returns the storage for the given partition ID.
func (m *Manager) partitionByID(id uint32) (storage.Storage, error) {
	if id >= uint32(len(m.partitions)) {
		return nil, &PartitionNotFoundError{PartitionID: id}
	}
	return m.partitions[id], nil
}

// Get retrieves a value by key from the appropriate partition.
func (m *Manager) Get(key []byte) ([]byte, error) {
	if m.closed.Load() {
		return nil, &storage.ClosedError{}
	}
	return m.partition(key).Get(key)
}

// Set stores a key-value pair in the appropriate partition.
func (m *Manager) Set(key, value []byte) error {
	if m.closed.Load() {
		return &storage.ClosedError{}
	}
	return m.partition(key).Set(key, value)
}

// Delete removes a key from the appropriate partition.
func (m *Manager) Delete(key []byte) error {
	if m.closed.Load() {
		return &storage.ClosedError{}
	}
	return m.partition(key).Delete(key)
}

// List returns all keys matching the given prefix across all partitions.
// Results are collected from all partitions since hash-based routing does not
// preserve key ordering across partitions.
func (m *Manager) List(prefix []byte) ([][]byte, error) {
	if m.closed.Load() {
		return nil, &storage.ClosedError{}
	}

	var allKeys [][]byte
	for _, p := range m.partitions {
		keys, err := p.List(prefix)
		if err != nil {
			return nil, err
		}
		allKeys = append(allKeys, keys...)
	}
	return allKeys, nil
}

// Exists reports whether a key exists in the appropriate partition.
func (m *Manager) Exists(key []byte) (bool, error) {
	if m.closed.Load() {
		return false, &storage.ClosedError{}
	}
	return m.partition(key).Exists(key)
}

// Batch creates a multi-partition batch that routes writes to the correct partitions.
func (m *Manager) Batch() storage.Batch {
	return newPartitionBatch(m)
}

// NewIterator creates a merge iterator that combines iterators from all partitions.
func (m *Manager) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	if m.closed.Load() {
		return newClosedMergeIterator()
	}

	iters := make([]storage.Iterator, len(m.partitions))
	for i, p := range m.partitions {
		iters[i] = p.NewIterator(opts)
	}
	return newMergeIterator(iters, opts)
}

// Snapshot creates a composite snapshot across all partitions.
func (m *Manager) Snapshot() (storage.Snapshot, error) {
	if m.closed.Load() {
		return nil, &storage.ClosedError{}
	}

	snapshots := make([]storage.Snapshot, len(m.partitions))
	for i, p := range m.partitions {
		snap, err := p.Snapshot()
		if err != nil {
			// Close already-created snapshots.
			for j := 0; j < i; j++ {
				_ = snapshots[j].Close()
			}
			return nil, &storage.SnapshotError{Op: "create", Err: err}
		}
		snapshots[i] = snap
	}
	return newPartitionSnapshot(m.router, snapshots), nil
}

// Close closes all partition storages. Returns the first error encountered.
func (m *Manager) Close() error {
	if !m.closed.CompareAndSwap(false, true) {
		return nil
	}

	var firstErr error
	for i, p := range m.partitions {
		if err := p.Close(); err != nil && firstErr == nil {
			firstErr = &PartitionCloseError{PartitionID: uint32(i), Err: err}
		}
	}
	return firstErr
}

// PartitionCount returns the number of partitions managed.
func (m *Manager) PartitionCount() uint32 {
	return uint32(len(m.partitions))
}
