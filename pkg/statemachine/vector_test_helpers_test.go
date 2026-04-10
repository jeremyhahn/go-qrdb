// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package statemachine

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/vector/hnsw"
)

// Helper function to create test logger
func newTestLogger() *logger.Logger {
	log, _ := logger.New(logger.LogConfig{
		Level:  "error",
		Format: "json",
		Output: "stdout",
	})
	return log
}

// newTestVectorStateMachine creates a VectorStateMachine with a mock HNSW factory for testing.
func newTestVectorStateMachine(adapter StorageAdapter, mockHNSW *MockHNSWIndex, cache CacheManager, log *logger.Logger) (*VectorStateMachine, error) {
	vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, cache, log)
	if err != nil {
		return nil, err
	}
	// Set custom factory to return the mock index for all namespaces
	vsm.SetHNSWFactory(func(config *hnsw.Config) (HNSWIndex, error) {
		return mockHNSW, nil
	})
	return vsm, nil
}

// MockStorageAdapter implements StorageAdapter for testing
type MockStorageAdapter struct {
	mu      sync.RWMutex
	data    map[string][]byte
	closed  bool
	getErr  error
	setErr  error
	delErr  error
	scanErr error
	vecStor *MockVectorStorageWrapper
}

func NewMockStorageAdapter() *MockStorageAdapter {
	return &MockStorageAdapter{
		data: make(map[string][]byte),
	}
}

func (m *MockStorageAdapter) Get(ctx context.Context, key []byte) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.getErr != nil {
		return nil, m.getErr
	}
	val, ok := m.data[string(key)]
	if !ok {
		return nil, api.NewNotFoundError("MockStorageAdapter.Get", "key")
	}
	return val, nil
}

func (m *MockStorageAdapter) Set(ctx context.Context, key, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.setErr != nil {
		return m.setErr
	}
	m.data[string(key)] = value
	return nil
}

func (m *MockStorageAdapter) Delete(ctx context.Context, key []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.delErr != nil {
		return m.delErr
	}
	delete(m.data, string(key))
	return nil
}

func (m *MockStorageAdapter) Scan(ctx context.Context, prefix []byte, fn func(key, value []byte) error) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.scanErr != nil {
		return m.scanErr
	}
	prefixStr := string(prefix)
	for k, v := range m.data {
		if len(prefixStr) == 0 || len(k) >= len(prefixStr) && k[:len(prefixStr)] == prefixStr {
			if err := fn([]byte(k), v); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *MockStorageAdapter) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *MockStorageAdapter) VectorStorage() vector.Storage {
	if m.vecStor == nil {
		m.vecStor = NewMockVectorStorageWrapper()
	}
	return m.vecStor
}

// MockVectorStorageWrapper wraps vector storage interface
type MockVectorStorageWrapper struct {
	mu          sync.RWMutex
	namespaces  map[string]*vector.NamespaceInfo
	vectors     map[string]map[uint64]*vector.VectorValue
	nextID      uint64
	createErr   error // Error to return from CreateNamespace
	deleteNsErr error // Error to return from DeleteNamespace
}

func NewMockVectorStorageWrapper() *MockVectorStorageWrapper {
	return &MockVectorStorageWrapper{
		namespaces: make(map[string]*vector.NamespaceInfo),
		vectors:    make(map[string]map[uint64]*vector.VectorValue),
		nextID:     1,
	}
}

func (m *MockVectorStorageWrapper) CreateNamespace(ctx context.Context, info *vector.NamespaceInfo) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createErr != nil {
		return m.createErr
	}
	if _, exists := m.namespaces[info.Name]; exists {
		return api.NewAlreadyExistsError("MockVectorStorage.CreateNamespace", "namespace", info.Name)
	}
	m.namespaces[info.Name] = info
	m.vectors[info.Name] = make(map[uint64]*vector.VectorValue)
	return nil
}

func (m *MockVectorStorageWrapper) DeleteNamespace(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deleteNsErr != nil {
		return m.deleteNsErr
	}
	delete(m.namespaces, name)
	delete(m.vectors, name)
	return nil
}

func (m *MockVectorStorageWrapper) GetNamespaceInfo(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	info, ok := m.namespaces[namespace]
	if !ok {
		return nil, api.NewNotFoundError("MockVectorStorage", "namespace")
	}
	return info, nil
}

func (m *MockVectorStorageWrapper) ListNamespaces(ctx context.Context) ([]vector.NamespaceInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]vector.NamespaceInfo, 0, len(m.namespaces))
	for _, ns := range m.namespaces {
		result = append(result, *ns)
	}
	return result, nil
}

func (m *MockVectorStorageWrapper) UpsertVector(ctx context.Context, namespace string, v *vector.VectorValue) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.vectors[namespace]; !exists {
		return api.NewNotFoundError("MockVectorStorage", "namespace")
	}
	m.vectors[namespace][v.ID] = v
	return nil
}

func (m *MockVectorStorageWrapper) GetVector(ctx context.Context, namespace string, id uint64) (*vector.VectorValue, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	vectors, ok := m.vectors[namespace]
	if !ok {
		return nil, api.NewNotFoundError("MockVectorStorage", "namespace")
	}
	vec, ok := vectors[id]
	if !ok {
		return nil, api.NewNotFoundError("MockVectorStorage", "vector")
	}
	return vec, nil
}

func (m *MockVectorStorageWrapper) DeleteVector(ctx context.Context, namespace string, id uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if vectors, ok := m.vectors[namespace]; ok {
		if _, exists := vectors[id]; !exists {
			return api.NewNotFoundError("MockVectorStorage.DeleteVector", "vector")
		}
		delete(vectors, id)
	}
	return nil
}

func (m *MockVectorStorageWrapper) BatchUpsertVectors(ctx context.Context, namespace string, vecs []*vector.VectorValue) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.vectors[namespace]; !exists {
		return api.NewNotFoundError("MockVectorStorage", "namespace")
	}
	for _, v := range vecs {
		m.vectors[namespace][v.ID] = v
	}
	return nil
}

func (m *MockVectorStorageWrapper) BatchDeleteVectors(ctx context.Context, namespace string, ids []uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if vectors, ok := m.vectors[namespace]; ok {
		for _, id := range ids {
			delete(vectors, id)
		}
	}
	return nil
}

func (m *MockVectorStorageWrapper) ScanByMetadata(ctx context.Context, namespace, field, value string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return nil, nil
}

func (m *MockVectorStorageWrapper) ScanByMultipleFilters(ctx context.Context, namespace string, filters []vector.MetadataFilter, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return nil, nil
}

func (m *MockVectorStorageWrapper) ScanNamespace(ctx context.Context, namespace string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	vectors, ok := m.vectors[namespace]
	if !ok {
		return nil, api.NewNotFoundError("MockVectorStorage", "namespace")
	}
	result := make([]*vector.VectorValue, 0, len(vectors))
	for _, v := range vectors {
		result = append(result, v)
	}
	return result, nil
}

func (m *MockVectorStorageWrapper) GetStats(ctx context.Context, namespace string) (*vector.NamespaceStats, error) {
	return nil, nil
}

func (m *MockVectorStorageWrapper) NextID(namespace string) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.nextID
	m.nextID++
	return id, nil
}

func (m *MockVectorStorageWrapper) BatchAllocateIDs(namespace string, count int) ([]uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]uint64, count)
	for i := 0; i < count; i++ {
		ids[i] = m.nextID
		m.nextID++
	}
	return ids, nil
}

func (m *MockVectorStorageWrapper) Compact(ctx context.Context, namespace string) error {
	return nil
}

func (m *MockVectorStorageWrapper) CleanupTombstones(ctx context.Context, olderThan time.Duration) error {
	return nil
}

func (m *MockVectorStorageWrapper) Close() error {
	return nil
}

// MockHNSWIndex for vector FSM tests
type MockHNSWIndex struct {
	mu      sync.RWMutex
	vectors map[uint64][]float32
}

func NewMockHNSWIndex() *MockHNSWIndex {
	return &MockHNSWIndex{
		vectors: make(map[uint64][]float32),
	}
}

func (m *MockHNSWIndex) Add(id uint64, vector []float32) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.vectors[id] = vector
	return nil
}

func (m *MockHNSWIndex) Delete(id uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.vectors, id)
	return nil
}

func (m *MockHNSWIndex) Search(query []float32, k int, efSearch int) ([]HNSWSearchResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	results := make([]HNSWSearchResult, 0)
	for id := range m.vectors {
		results = append(results, HNSWSearchResult{ID: id, Distance: 0.1})
		if len(results) >= k {
			break
		}
	}
	return results, nil
}

func (m *MockHNSWIndex) Size() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.vectors)
}

func (m *MockHNSWIndex) SaveSnapshot(w io.Writer) error {
	return nil
}

func (m *MockHNSWIndex) LoadSnapshot(r io.Reader) error {
	return nil
}

func (m *MockHNSWIndex) Reset() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.vectors = make(map[uint64][]float32)
	return nil
}

// MockCache for vector FSM tests
type MockCache struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func NewMockCache() *MockCache {
	return &MockCache{
		data: make(map[string][]byte),
	}
}

func (m *MockCache) Get(ctx context.Context, key []byte) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	val, ok := m.data[string(key)]
	if !ok {
		return nil, api.NewCacheMissError("MockCache.Get")
	}
	return val, nil
}

func (m *MockCache) Set(ctx context.Context, key []byte, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[string(key)] = value
	return nil
}

func (m *MockCache) Delete(ctx context.Context, key []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, string(key))
	return nil
}

func (m *MockCache) Invalidate(ctx context.Context, pattern string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = make(map[string][]byte)
	return nil
}
