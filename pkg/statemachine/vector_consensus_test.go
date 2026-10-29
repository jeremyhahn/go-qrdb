//go:build raft

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
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vmihailenco/msgpack/v5"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	vectorStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

// Test sentinel errors for mock error injection.
var (
	errStorageUnavailable = errors.New("storage unavailable")
	errScanFailed         = errors.New("scan failed")
	errStorageError       = errors.New("storage error")
	errUpsertFailed       = errors.New("upsert failed")
	errBatchDeleteFailed  = errors.New("batch delete failed")
	errDeleteVecFailed    = errors.New("delete vec failed")
	errBatchUpsertFailed  = errors.New("batch upsert failed")
)

// mockVectorStorage is a mock implementation of vectorStorage.Storage for testing.
type mockVectorStorage struct {
	namespaces map[string]*vectorStorage.NamespaceInfo
	vectors    map[string]map[uint64]*vectorStorage.VectorValue

	// Error injection
	createNSErr    error
	deleteNSErr    error
	listNSErr      error
	upsertVecErr   error
	deleteVecErr   error
	batchUpsertErr error
	batchDeleteErr error
	getVecErr      error
	scanNSErr      error
	getStatsErr    error
	nextIDErr      error
	batchAllocErr  error
	compactErr     error
	cleanupErr     error
	getNSInfoErr   error
	scanMetaErr    error
	scanMultiErr   error
}

func newMockVectorStorage() *mockVectorStorage {
	return &mockVectorStorage{
		namespaces: make(map[string]*vectorStorage.NamespaceInfo),
		vectors:    make(map[string]map[uint64]*vectorStorage.VectorValue),
	}
}

func (m *mockVectorStorage) UpdateNamespace(_ context.Context, namespace string, update *vectorStorage.NamespaceUpdate) error {
	info, ok := m.namespaces[namespace]
	if !ok {
		return vectorStorage.ErrNamespaceNotFound
	}
	info.IndexConfig = update.IndexConfig
	info.Metadata = update.Metadata
	info.UpdatedAt = update.UpdatedAt
	return nil
}

func (m *mockVectorStorage) CreateNamespace(_ context.Context, info *vectorStorage.NamespaceInfo) error {
	if m.createNSErr != nil {
		return m.createNSErr
	}
	m.namespaces[info.Name] = info
	if m.vectors[info.Name] == nil {
		m.vectors[info.Name] = make(map[uint64]*vectorStorage.VectorValue)
	}
	return nil
}

func (m *mockVectorStorage) DeleteNamespace(_ context.Context, namespace string) error {
	if m.deleteNSErr != nil {
		return m.deleteNSErr
	}
	delete(m.namespaces, namespace)
	delete(m.vectors, namespace)
	return nil
}

func (m *mockVectorStorage) ListNamespaces(_ context.Context) ([]vectorStorage.NamespaceInfo, error) {
	if m.listNSErr != nil {
		return nil, m.listNSErr
	}
	result := make([]vectorStorage.NamespaceInfo, 0, len(m.namespaces))
	for _, ns := range m.namespaces {
		result = append(result, *ns)
	}
	return result, nil
}

func (m *mockVectorStorage) GetNamespaceInfo(_ context.Context, namespace string) (*vectorStorage.NamespaceInfo, error) {
	if m.getNSInfoErr != nil {
		return nil, m.getNSInfoErr
	}
	ns, ok := m.namespaces[namespace]
	if !ok {
		return nil, &dberrors.MessageError{Message: "namespace " + namespace + " not found"}
	}
	return ns, nil
}

func (m *mockVectorStorage) UpsertVector(_ context.Context, namespace string, vec *vectorStorage.VectorValue) error {
	if m.upsertVecErr != nil {
		return m.upsertVecErr
	}
	if m.vectors[namespace] == nil {
		m.vectors[namespace] = make(map[uint64]*vectorStorage.VectorValue)
	}
	m.vectors[namespace][vec.ID] = vec
	return nil
}

func (m *mockVectorStorage) GetVector(_ context.Context, namespace string, id uint64) (*vectorStorage.VectorValue, error) {
	if m.getVecErr != nil {
		return nil, m.getVecErr
	}
	if vecs, ok := m.vectors[namespace]; ok {
		if vec, ok := vecs[id]; ok {
			return vec, nil
		}
	}
	return nil, &dberrors.MessageError{Message: "vector not found in namespace " + namespace}
}

func (m *mockVectorStorage) DeleteVector(_ context.Context, namespace string, id uint64) error {
	if m.deleteVecErr != nil {
		return m.deleteVecErr
	}
	if vecs, ok := m.vectors[namespace]; ok {
		delete(vecs, id)
	}
	return nil
}

func (m *mockVectorStorage) BatchUpsertVectors(_ context.Context, namespace string, vecs []*vectorStorage.VectorValue) error {
	if m.batchUpsertErr != nil {
		return m.batchUpsertErr
	}
	if m.vectors[namespace] == nil {
		m.vectors[namespace] = make(map[uint64]*vectorStorage.VectorValue)
	}
	for _, vec := range vecs {
		m.vectors[namespace][vec.ID] = vec
	}
	return nil
}

func (m *mockVectorStorage) BatchDeleteVectors(_ context.Context, namespace string, ids []uint64) error {
	if m.batchDeleteErr != nil {
		return m.batchDeleteErr
	}
	if vecs, ok := m.vectors[namespace]; ok {
		for _, id := range ids {
			delete(vecs, id)
		}
	}
	return nil
}

func (m *mockVectorStorage) ScanNamespace(_ context.Context, namespace string, _ *vectorStorage.ScanOptions) ([]*vectorStorage.VectorValue, error) {
	if m.scanNSErr != nil {
		return nil, m.scanNSErr
	}
	var result []*vectorStorage.VectorValue
	if vecs, ok := m.vectors[namespace]; ok {
		for _, v := range vecs {
			result = append(result, v)
		}
	}
	return result, nil
}

func (m *mockVectorStorage) ScanByMetadata(_ context.Context, _, _, _ string, _ *vectorStorage.ScanOptions) ([]*vectorStorage.VectorValue, error) {
	if m.scanMetaErr != nil {
		return nil, m.scanMetaErr
	}
	return nil, nil
}

func (m *mockVectorStorage) ScanByMultipleFilters(_ context.Context, _ string, _ []vectorStorage.MetadataFilter, _ *vectorStorage.ScanOptions) ([]*vectorStorage.VectorValue, error) {
	if m.scanMultiErr != nil {
		return nil, m.scanMultiErr
	}
	return nil, nil
}

func (m *mockVectorStorage) GetStats(_ context.Context, _ string) (*vectorStorage.NamespaceStats, error) {
	if m.getStatsErr != nil {
		return nil, m.getStatsErr
	}
	return &vectorStorage.NamespaceStats{}, nil
}

func (m *mockVectorStorage) NextID(_ string) (uint64, error) {
	if m.nextIDErr != nil {
		return 0, m.nextIDErr
	}
	return 1, nil
}

func (m *mockVectorStorage) BatchAllocateIDs(_ string, count int) ([]uint64, error) {
	if m.batchAllocErr != nil {
		return nil, m.batchAllocErr
	}
	ids := make([]uint64, count)
	for i := range ids {
		ids[i] = uint64(i + 1)
	}
	return ids, nil
}

func (m *mockVectorStorage) Compact(_ context.Context, _ string) error { return m.compactErr }

func (m *mockVectorStorage) CleanupTombstones(_ context.Context, _ time.Duration) error {
	return m.cleanupErr
}

func (m *mockVectorStorage) Close() error { return nil }

// createTestVectorLogger creates a logger for vector consensus tests.
func createTestVectorLogger(t *testing.T) *logger.Logger {
	t.Helper()
	log, err := logger.New(logger.Default())
	require.NoError(t, err)
	return log
}

// encodeVectorCmd encodes a VectorCommand to bytes for use in Update entries.
func encodeVectorCmd(t *testing.T, cmd *VectorCommand) []byte {
	t.Helper()
	data, err := msgpack.Marshal(cmd)
	require.NoError(t, err)
	return data
}

// =============================================================================
// NewVectorConsensusFSM Tests
// =============================================================================

func TestNewVectorConsensusFSM(t *testing.T) {
	t.Run("creates FSM without storage", func(t *testing.T) {
		log := createTestVectorLogger(t)
		fsm := NewVectorConsensusFSM(VectorShardID, 1, log)
		require.NotNil(t, fsm)

		vfsm, ok := fsm.(*VectorConsensusFSM)
		require.True(t, ok)
		assert.Equal(t, VectorShardID, vfsm.shardID)
		assert.Equal(t, uint64(1), vfsm.replicaID)
		assert.Nil(t, vfsm.storage)
		assert.NotNil(t, vfsm.hnswIndexes)
		assert.NotNil(t, vfsm.namespaces)

		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("creates FSM with nil logger uses default", func(t *testing.T) {
		fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, nil, nil)
		require.NotNil(t, fsm)
		require.NoError(t, fsm.Close(context.Background()))
	})
}

func TestNewVectorConsensusFSMWithStorage(t *testing.T) {
	t.Run("creates FSM with mock storage", func(t *testing.T) {
		log := createTestVectorLogger(t)
		storage := newMockVectorStorage()

		fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, storage, log)
		require.NotNil(t, fsm)

		vfsm := fsm.(*VectorConsensusFSM)
		assert.NotNil(t, vfsm.storage)
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("loads existing namespaces from storage on init", func(t *testing.T) {
		log := createTestVectorLogger(t)
		storage := newMockVectorStorage()

		// Pre-populate storage with a namespace and vectors
		storage.namespaces["test-ns"] = &vectorStorage.NamespaceInfo{
			Name:           "test-ns",
			Dimension:      128,
			DistanceMetric: "cosine",
		}
		storage.vectors["test-ns"] = map[uint64]*vectorStorage.VectorValue{
			1: {ID: 1, Embedding: make([]float32, 128)},
		}

		fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, storage, log)
		require.NotNil(t, fsm)

		vfsm := fsm.(*VectorConsensusFSM)
		assert.Contains(t, vfsm.namespaces, "test-ns")
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("handles ListNamespaces error gracefully on init", func(t *testing.T) {
		log := createTestVectorLogger(t)
		storage := newMockVectorStorage()
		storage.listNSErr = errStorageUnavailable

		fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, storage, log)
		require.NotNil(t, fsm)
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("handles ScanNamespace error gracefully on init", func(t *testing.T) {
		log := createTestVectorLogger(t)
		storage := newMockVectorStorage()

		// Pre-populate namespace but make scan fail
		storage.namespaces["broken-ns"] = &vectorStorage.NamespaceInfo{
			Name:           "broken-ns",
			Dimension:      64,
			DistanceMetric: "L2",
		}
		storage.scanNSErr = errScanFailed

		fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, storage, log)
		require.NotNil(t, fsm)
		require.NoError(t, fsm.Close(context.Background()))
	})
}

// =============================================================================
// Update Tests
// =============================================================================

func TestVectorConsensusFSM_Update(t *testing.T) {
	t.Run("empty command is no-op", func(t *testing.T) {
		log := createTestVectorLogger(t)
		fsm := NewVectorConsensusFSM(VectorShardID, 1, log)

		entries := []sm.Entry{{Index: 1, Cmd: nil}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		result := results[0]
		require.NoError(t, err)
		assert.Equal(t, uint64(0), result.Value)
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("invalid command data is handled gracefully", func(t *testing.T) {
		log := createTestVectorLogger(t)
		fsm := NewVectorConsensusFSM(VectorShardID, 1, log)

		// Pass invalid data that can't be decoded as VectorCommand
		entries := []sm.Entry{{Index: 1, Cmd: []byte("not-valid-msgpack")}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		result := results[0]
		require.NoError(t, err) // Should not error, to prevent cluster panic
		assert.Equal(t, uint64(0), result.Value)
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("read-only operation returns success", func(t *testing.T) {
		log := createTestVectorLogger(t)
		fsm := NewVectorConsensusFSM(VectorShardID, 1, log)

		cmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpSearchSimilar, // Read-only op
			Namespace: "test-ns",
		})

		entries := []sm.Entry{{Index: 1, Cmd: cmd}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		result := results[0]
		require.NoError(t, err)
		assert.Equal(t, uint64(1), result.Value)
		require.NoError(t, fsm.Close(context.Background()))
	})
}

// =============================================================================
// applyCreateNamespace Tests
// =============================================================================

func TestVectorConsensusFSM_CreateNamespace(t *testing.T) {
	t.Run("creates namespace with storage", func(t *testing.T) {
		log := createTestVectorLogger(t)
		storage := newMockVectorStorage()
		fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, storage, log)

		cmd := encodeVectorCmd(t, &VectorCommand{
			Op:             OpCreateNamespace,
			Namespace:      "test-ns",
			Dimension:      128,
			DistanceMetric: "cosine",
			IndexType:      "HNSW",
		})

		entries := []sm.Entry{{Index: 1, Cmd: cmd}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		result := results[0]
		require.NoError(t, err)
		assert.Equal(t, uint64(1), result.Value)

		// Verify namespace created in storage
		assert.Contains(t, storage.namespaces, "test-ns")
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("creates namespace without storage", func(t *testing.T) {
		log := createTestVectorLogger(t)
		fsm := NewVectorConsensusFSM(VectorShardID, 1, log)

		cmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpCreateNamespace,
			Namespace: "test-ns",
			Dimension: 64,
		})

		entries := []sm.Entry{{Index: 1, Cmd: cmd}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		result := results[0]
		require.NoError(t, err)
		assert.Equal(t, uint64(1), result.Value)

		vfsm := fsm.(*VectorConsensusFSM)
		assert.Contains(t, vfsm.namespaces, "test-ns")
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("uses defaults when distance metric and index type empty", func(t *testing.T) {
		log := createTestVectorLogger(t)
		storage := newMockVectorStorage()
		fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, storage, log)

		cmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpCreateNamespace,
			Namespace: "default-ns",
			Dimension: 64,
			// DistanceMetric and IndexType left empty for defaults
		})

		entries := []sm.Entry{{Index: 1, Cmd: cmd}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		result := results[0]
		require.NoError(t, err)
		assert.Equal(t, uint64(1), result.Value)

		ns := storage.namespaces["default-ns"]
		require.NotNil(t, ns)
		assert.Equal(t, "L2", ns.DistanceMetric)
		assert.Equal(t, "HNSW", ns.IndexType)
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("handles storage create error gracefully", func(t *testing.T) {
		log := createTestVectorLogger(t)
		storage := newMockVectorStorage()
		storage.createNSErr = errStorageError
		fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, storage, log)

		cmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpCreateNamespace,
			Namespace: "fail-ns",
			Dimension: 64,
		})

		// Should still succeed (non-fatal, may already exist)
		entries := []sm.Entry{{Index: 1, Cmd: cmd}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		result := results[0]
		require.NoError(t, err)
		assert.Equal(t, uint64(1), result.Value)
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("creates namespace with dot_product distance metric", func(t *testing.T) {
		log := createTestVectorLogger(t)
		storage := newMockVectorStorage()
		fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, storage, log)

		cmd := encodeVectorCmd(t, &VectorCommand{
			Op:             OpCreateNamespace,
			Namespace:      "dotprod-ns",
			Dimension:      128,
			DistanceMetric: "dot_product",
		})

		entries := []sm.Entry{{Index: 1, Cmd: cmd}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		result := results[0]
		require.NoError(t, err)
		assert.Equal(t, uint64(1), result.Value)
		require.NoError(t, fsm.Close(context.Background()))
	})
}

// =============================================================================
// applyDeleteNamespace Tests
// =============================================================================

func TestVectorConsensusFSM_DeleteNamespace(t *testing.T) {
	t.Run("deletes namespace with storage", func(t *testing.T) {
		log := createTestVectorLogger(t)
		storage := newMockVectorStorage()
		fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, storage, log)

		// First create a namespace
		createCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpCreateNamespace,
			Namespace: "to-delete",
			Dimension: 64,
		})
		entries := []sm.Entry{{Index: 1, Cmd: createCmd}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		require.NoError(t, err)

		// Now delete it
		deleteCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpDeleteNamespace,
			Namespace: "to-delete",
		})
		entries = []sm.Entry{{Index: 2, Cmd: deleteCmd}}
		results = make([]sm.Result, 1)
		err = fsm.Update(context.Background(), entries, results)
		result := results[0]
		require.NoError(t, err)
		assert.Equal(t, uint64(1), result.Value)

		// Verify deleted
		vfsm := fsm.(*VectorConsensusFSM)
		assert.NotContains(t, vfsm.namespaces, "to-delete")
		assert.NotContains(t, storage.namespaces, "to-delete")
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("deletes namespace without storage", func(t *testing.T) {
		log := createTestVectorLogger(t)
		fsm := NewVectorConsensusFSM(VectorShardID, 1, log)

		// Create then delete
		createCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpCreateNamespace,
			Namespace: "to-delete",
			Dimension: 64,
		})
		entries := []sm.Entry{{Index: 1, Cmd: createCmd}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		require.NoError(t, err)

		deleteCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpDeleteNamespace,
			Namespace: "to-delete",
		})
		entries = []sm.Entry{{Index: 2, Cmd: deleteCmd}}
		results = make([]sm.Result, 1)
		err = fsm.Update(context.Background(), entries, results)
		result := results[0]
		require.NoError(t, err)
		assert.Equal(t, uint64(1), result.Value)
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("handles storage delete error gracefully", func(t *testing.T) {
		log := createTestVectorLogger(t)
		storage := newMockVectorStorage()
		fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, storage, log)

		storage.deleteNSErr = errors.New("delete failed")

		deleteCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpDeleteNamespace,
			Namespace: "nonexistent",
		})
		entries := []sm.Entry{{Index: 1, Cmd: deleteCmd}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		result := results[0]
		require.NoError(t, err)
		assert.Equal(t, uint64(1), result.Value)
		require.NoError(t, fsm.Close(context.Background()))
	})
}

// =============================================================================
// applyUpsertVector Tests
// =============================================================================

func TestVectorConsensusFSM_UpsertVector(t *testing.T) {
	t.Run("upserts vector with storage and HNSW index", func(t *testing.T) {
		log := createTestVectorLogger(t)
		storage := newMockVectorStorage()
		fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, storage, log)

		// Create namespace first
		createCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpCreateNamespace,
			Namespace: "vec-ns",
			Dimension: 4,
		})
		entries := []sm.Entry{{Index: 1, Cmd: createCmd}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		require.NoError(t, err)

		// Upsert vector
		upsertCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpUpsertVector,
			Namespace: "vec-ns",
			ID:        42,
			Vector:    []float32{1.0, 2.0, 3.0, 4.0},
			Metadata:  map[string]interface{}{"key": "value"},
		})
		entries = []sm.Entry{{Index: 2, Cmd: upsertCmd}}
		results = make([]sm.Result, 1)
		err = fsm.Update(context.Background(), entries, results)
		result := results[0]
		require.NoError(t, err)
		assert.Equal(t, uint64(1), result.Value)

		// Verify stored
		vec, err := storage.GetVector(context.Background(), "vec-ns", 42)
		require.NoError(t, err)
		assert.Equal(t, uint64(42), vec.ID)
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("upserts vector without storage (no HNSW index yet, lazy creates)", func(t *testing.T) {
		log := createTestVectorLogger(t)
		fsm := NewVectorConsensusFSM(VectorShardID, 1, log)

		// Upsert vector to namespace that doesn't have HNSW index yet
		upsertCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpUpsertVector,
			Namespace: "lazy-ns",
			ID:        1,
			Vector:    []float32{1.0, 2.0, 3.0},
		})
		entries := []sm.Entry{{Index: 1, Cmd: upsertCmd}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		result := results[0]
		require.NoError(t, err)
		assert.Equal(t, uint64(1), result.Value)
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("handles storage upsert error", func(t *testing.T) {
		log := createTestVectorLogger(t)
		storage := newMockVectorStorage()
		storage.upsertVecErr = errUpsertFailed
		fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, storage, log)

		upsertCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpUpsertVector,
			Namespace: "err-ns",
			ID:        1,
			Vector:    []float32{1.0},
		})
		entries := []sm.Entry{{Index: 1, Cmd: upsertCmd}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		require.Error(t, err)
		require.NoError(t, fsm.Close(context.Background()))
	})
}

// =============================================================================
// applyDeleteVector Tests
// =============================================================================

func TestVectorConsensusFSM_DeleteVector(t *testing.T) {
	t.Run("deletes single vector with storage", func(t *testing.T) {
		log := createTestVectorLogger(t)
		storage := newMockVectorStorage()
		fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, storage, log)

		// Create namespace and upsert a vector
		createCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpCreateNamespace,
			Namespace: "del-ns",
			Dimension: 3,
		})
		entries := []sm.Entry{{Index: 1, Cmd: createCmd}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		require.NoError(t, err)

		upsertCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpUpsertVector,
			Namespace: "del-ns",
			ID:        10,
			Vector:    []float32{1.0, 2.0, 3.0},
		})
		entries = []sm.Entry{{Index: 2, Cmd: upsertCmd}}
		results = make([]sm.Result, 1)
		err = fsm.Update(context.Background(), entries, results)
		require.NoError(t, err)

		// Delete the vector
		deleteCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpDeleteVector,
			Namespace: "del-ns",
			ID:        10,
		})
		entries = []sm.Entry{{Index: 3, Cmd: deleteCmd}}
		results = make([]sm.Result, 1)
		err = fsm.Update(context.Background(), entries, results)
		result := results[0]
		require.NoError(t, err)
		assert.Equal(t, uint64(1), result.Value)
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("batch deletes vectors with storage", func(t *testing.T) {
		log := createTestVectorLogger(t)
		storage := newMockVectorStorage()
		fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, storage, log)

		// Create namespace
		createCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpCreateNamespace,
			Namespace: "batch-del-ns",
			Dimension: 3,
		})
		entries := []sm.Entry{{Index: 1, Cmd: createCmd}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		require.NoError(t, err)

		// Upsert some vectors
		for i := uint64(1); i <= 3; i++ {
			upsertCmd := encodeVectorCmd(t, &VectorCommand{
				Op:        OpUpsertVector,
				Namespace: "batch-del-ns",
				ID:        i,
				Vector:    []float32{float32(i), float32(i), float32(i)},
			})
			entries = []sm.Entry{{Index: 1 + i, Cmd: upsertCmd}}
			results = make([]sm.Result, 1)
			err = fsm.Update(context.Background(), entries, results)
			require.NoError(t, err)
		}

		// Batch delete
		deleteCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpDeleteVector,
			Namespace: "batch-del-ns",
			IDs:       []uint64{1, 2, 3},
		})
		entries = []sm.Entry{{Index: 5, Cmd: deleteCmd}}
		results = make([]sm.Result, 1)
		err = fsm.Update(context.Background(), entries, results)
		result := results[0]
		require.NoError(t, err)
		assert.Equal(t, uint64(1), result.Value)
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("delete vector without storage or HNSW index", func(t *testing.T) {
		log := createTestVectorLogger(t)
		fsm := NewVectorConsensusFSM(VectorShardID, 1, log)

		deleteCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpDeleteVector,
			Namespace: "missing-ns",
			ID:        999,
		})
		entries := []sm.Entry{{Index: 1, Cmd: deleteCmd}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		result := results[0]
		require.NoError(t, err)
		assert.Equal(t, uint64(1), result.Value)
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("handles batch delete storage error gracefully", func(t *testing.T) {
		log := createTestVectorLogger(t)
		storage := newMockVectorStorage()
		storage.batchDeleteErr = errBatchDeleteFailed
		fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, storage, log)

		deleteCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpDeleteVector,
			Namespace: "err-ns",
			IDs:       []uint64{1, 2},
		})
		entries := []sm.Entry{{Index: 1, Cmd: deleteCmd}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		result := results[0]
		require.NoError(t, err)
		assert.Equal(t, uint64(1), result.Value)
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("handles single delete storage error gracefully", func(t *testing.T) {
		log := createTestVectorLogger(t)
		storage := newMockVectorStorage()
		storage.deleteVecErr = errDeleteVecFailed
		fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, storage, log)

		deleteCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpDeleteVector,
			Namespace: "err-ns",
			ID:        42,
		})
		entries := []sm.Entry{{Index: 1, Cmd: deleteCmd}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		result := results[0]
		require.NoError(t, err)
		assert.Equal(t, uint64(1), result.Value)
		require.NoError(t, fsm.Close(context.Background()))
	})
}

// =============================================================================
// applyBatchUpsert Tests
// =============================================================================

func TestVectorConsensusFSM_BatchUpsert(t *testing.T) {
	t.Run("batch upserts vectors with storage", func(t *testing.T) {
		log := createTestVectorLogger(t)
		storage := newMockVectorStorage()
		fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, storage, log)

		// Create namespace
		createCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpCreateNamespace,
			Namespace: "batch-ns",
			Dimension: 3,
		})
		entries := []sm.Entry{{Index: 1, Cmd: createCmd}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		require.NoError(t, err)

		// Batch upsert
		batchCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpBatchUpsert,
			Namespace: "batch-ns",
			IDs:       []uint64{10, 20, 30},
			Vectors: [][]float32{
				{1.0, 1.0, 1.0},
				{2.0, 2.0, 2.0},
				{3.0, 3.0, 3.0},
			},
		})
		entries = []sm.Entry{{Index: 2, Cmd: batchCmd}}
		results = make([]sm.Result, 1)
		err = fsm.Update(context.Background(), entries, results)
		result := results[0]
		require.NoError(t, err)
		assert.Equal(t, uint64(3), result.Value)

		// Verify all vectors in storage
		assert.Len(t, storage.vectors["batch-ns"], 3)
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("batch upsert without existing HNSW index lazily creates one", func(t *testing.T) {
		log := createTestVectorLogger(t)
		fsm := NewVectorConsensusFSM(VectorShardID, 1, log)

		batchCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpBatchUpsert,
			Namespace: "lazy-batch-ns",
			IDs:       []uint64{1, 2},
			Vectors: [][]float32{
				{1.0, 2.0},
				{3.0, 4.0},
			},
		})
		entries := []sm.Entry{{Index: 1, Cmd: batchCmd}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		result := results[0]
		require.NoError(t, err)
		assert.Equal(t, uint64(2), result.Value)
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("batch upsert with mismatched IDs and Vectors returns error", func(t *testing.T) {
		log := createTestVectorLogger(t)
		fsm := NewVectorConsensusFSM(VectorShardID, 1, log)

		batchCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpBatchUpsert,
			Namespace: "mismatch-ns",
			IDs:       []uint64{1, 2, 3},
			Vectors: [][]float32{
				{1.0}, // only 1 vector for 3 IDs
			},
		})
		entries := []sm.Entry{{Index: 1, Cmd: batchCmd}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mismatch")
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("batch upsert with storage error", func(t *testing.T) {
		log := createTestVectorLogger(t)
		storage := newMockVectorStorage()
		storage.batchUpsertErr = errBatchUpsertFailed
		fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, storage, log)

		batchCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpBatchUpsert,
			Namespace: "fail-ns",
			IDs:       []uint64{1},
			Vectors:   [][]float32{{1.0}},
		})
		entries := []sm.Entry{{Index: 1, Cmd: batchCmd}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		require.Error(t, err)
		require.NoError(t, fsm.Close(context.Background()))
	})
}

// =============================================================================
// applyUpdateMetadata Tests
// =============================================================================

func TestVectorConsensusFSM_UpdateMetadata(t *testing.T) {
	t.Run("merges metadata for existing vector", func(t *testing.T) {
		log := createTestVectorLogger(t)
		storage := newMockVectorStorage()
		fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, storage, log)

		// Create namespace and upsert vector
		createCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpCreateNamespace,
			Namespace: "meta-ns",
			Dimension: 3,
		})
		entries := []sm.Entry{{Index: 1, Cmd: createCmd}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		require.NoError(t, err)

		upsertCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpUpsertVector,
			Namespace: "meta-ns",
			ID:        1,
			Vector:    []float32{1.0, 2.0, 3.0},
			Metadata:  map[string]interface{}{"existing": "value"},
		})
		entries = []sm.Entry{{Index: 2, Cmd: upsertCmd}}
		results = make([]sm.Result, 1)
		err = fsm.Update(context.Background(), entries, results)
		require.NoError(t, err)

		// Merge metadata
		updateCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpUpdateMetadata,
			Namespace: "meta-ns",
			ID:        1,
			Metadata:  map[string]interface{}{"new_key": "new_value"},
			Merge:     true,
		})
		entries = []sm.Entry{{Index: 3, Cmd: updateCmd}}
		results = make([]sm.Result, 1)
		err = fsm.Update(context.Background(), entries, results)
		result := results[0]
		require.NoError(t, err)
		assert.Equal(t, uint64(1), result.Value)

		// Verify both old and new metadata present
		vec, err := storage.GetVector(context.Background(), "meta-ns", 1)
		require.NoError(t, err)
		assert.Contains(t, vec.Metadata, "existing")
		assert.Contains(t, vec.Metadata, "new_key")
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("replaces metadata when merge is false", func(t *testing.T) {
		log := createTestVectorLogger(t)
		storage := newMockVectorStorage()
		fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, storage, log)

		// Create namespace and upsert vector
		createCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpCreateNamespace,
			Namespace: "replace-ns",
			Dimension: 3,
		})
		entries := []sm.Entry{{Index: 1, Cmd: createCmd}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		require.NoError(t, err)

		upsertCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpUpsertVector,
			Namespace: "replace-ns",
			ID:        1,
			Vector:    []float32{1.0, 2.0, 3.0},
			Metadata:  map[string]interface{}{"old": "data"},
		})
		entries = []sm.Entry{{Index: 2, Cmd: upsertCmd}}
		results = make([]sm.Result, 1)
		err = fsm.Update(context.Background(), entries, results)
		require.NoError(t, err)

		// Replace metadata
		updateCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpUpdateMetadata,
			Namespace: "replace-ns",
			ID:        1,
			Metadata:  map[string]interface{}{"new": "data"},
			Merge:     false,
		})
		entries = []sm.Entry{{Index: 3, Cmd: updateCmd}}
		results = make([]sm.Result, 1)
		err = fsm.Update(context.Background(), entries, results)
		result := results[0]
		require.NoError(t, err)
		assert.Equal(t, uint64(1), result.Value)

		// Verify only new metadata present
		vec, err := storage.GetVector(context.Background(), "replace-ns", 1)
		require.NoError(t, err)
		assert.NotContains(t, vec.Metadata, "old")
		assert.Contains(t, vec.Metadata, "new")
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("update metadata without storage returns success", func(t *testing.T) {
		log := createTestVectorLogger(t)
		fsm := NewVectorConsensusFSM(VectorShardID, 1, log)

		updateCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpUpdateMetadata,
			Namespace: "no-storage-ns",
			ID:        1,
			Metadata:  map[string]interface{}{"k": "v"},
		})
		entries := []sm.Entry{{Index: 1, Cmd: updateCmd}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		result := results[0]
		require.NoError(t, err)
		assert.Equal(t, uint64(1), result.Value)
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("update metadata for nonexistent vector returns error", func(t *testing.T) {
		log := createTestVectorLogger(t)
		storage := newMockVectorStorage()
		fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, storage, log)

		updateCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpUpdateMetadata,
			Namespace: "ns",
			ID:        999,
			Metadata:  map[string]interface{}{"k": "v"},
		})
		entries := []sm.Entry{{Index: 1, Cmd: updateCmd}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		require.Error(t, err)
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("update metadata storage upsert error", func(t *testing.T) {
		log := createTestVectorLogger(t)
		storage := newMockVectorStorage()
		fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, storage, log)

		// Upsert a vector first (no namespace needed for mock)
		storage.vectors["ns"] = map[uint64]*vectorStorage.VectorValue{
			1: {ID: 1, Embedding: []float32{1.0}, Metadata: map[string]string{"a": "b"}},
		}

		// Now fail on upsert
		storage.upsertVecErr = errUpsertFailed

		updateCmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpUpdateMetadata,
			Namespace: "ns",
			ID:        1,
			Metadata:  map[string]interface{}{"k": "v"},
		})
		entries := []sm.Entry{{Index: 1, Cmd: updateCmd}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		require.Error(t, err)
		require.NoError(t, fsm.Close(context.Background()))
	})
}

// =============================================================================
// Lookup Tests
// =============================================================================

func TestVectorConsensusFSM_Lookup(t *testing.T) {
	t.Run("nil query returns namespace list", func(t *testing.T) {
		log := createTestVectorLogger(t)
		fsm := NewVectorConsensusFSM(VectorShardID, 1, log)

		// Create some namespaces
		for _, ns := range []string{"ns-a", "ns-b"} {
			cmd := encodeVectorCmd(t, &VectorCommand{
				Op:        OpCreateNamespace,
				Namespace: ns,
				Dimension: 4,
			})
			entries := []sm.Entry{{Index: 1, Cmd: cmd}}
			results := make([]sm.Result, 1)
			err := fsm.Update(context.Background(), entries, results)
			require.NoError(t, err)
		}

		result, err := fsm.Lookup(context.Background(), nil)
		require.NoError(t, err)

		names, ok := result.([]string)
		require.True(t, ok)
		assert.Len(t, names, 2)
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("string query returns namespace info", func(t *testing.T) {
		log := createTestVectorLogger(t)
		fsm := NewVectorConsensusFSM(VectorShardID, 1, log)

		cmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpCreateNamespace,
			Namespace: "lookup-ns",
			Dimension: 128,
		})
		entries := []sm.Entry{{Index: 1, Cmd: cmd}}
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), entries, results)
		require.NoError(t, err)

		result, err := fsm.Lookup(context.Background(), "lookup-ns")
		require.NoError(t, err)

		info, ok := result.(*ConsensusNamespaceInfo)
		require.True(t, ok)
		assert.Equal(t, "lookup-ns", info.Name)
		assert.Equal(t, uint32(128), info.Dimension)
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("string query for nonexistent namespace returns nil", func(t *testing.T) {
		log := createTestVectorLogger(t)
		fsm := NewVectorConsensusFSM(VectorShardID, 1, log)

		result, err := fsm.Lookup(context.Background(), "nonexistent")
		require.NoError(t, err)
		assert.Nil(t, result)
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("unsupported query type returns error", func(t *testing.T) {
		log := createTestVectorLogger(t)
		fsm := NewVectorConsensusFSM(VectorShardID, 1, log)

		_, err := fsm.Lookup(context.Background(), 12345) // int is not supported
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid query type")
		require.NoError(t, fsm.Close(context.Background()))
	})
}

// =============================================================================
// Snapshot Tests
// =============================================================================

func TestVectorConsensusFSM_SaveAndRecoverSnapshot(t *testing.T) {
	t.Run("round-trips namespace metadata via snapshot", func(t *testing.T) {
		log := createTestVectorLogger(t)
		fsm1 := NewVectorConsensusFSM(VectorShardID, 1, log)

		// Create namespaces
		for _, ns := range []string{"snap-ns-1", "snap-ns-2"} {
			cmd := encodeVectorCmd(t, &VectorCommand{
				Op:        OpCreateNamespace,
				Namespace: ns,
				Dimension: 64,
			})
			entries := []sm.Entry{{Index: 1, Cmd: cmd}}
			results := make([]sm.Result, 1)
			err := fsm1.Update(context.Background(), entries, results)
			require.NoError(t, err)
		}

		// Save snapshot
		var buf bytes.Buffer
		done := make(chan struct{})
		close(done)
		err := fsm1.SaveSnapshot(context.Background(), &buf, done)
		require.NoError(t, err)

		// Recover on a new FSM
		fsm2 := NewVectorConsensusFSM(VectorShardID, 2, log)
		err = fsm2.RecoverFromSnapshot(context.Background(), &buf, done)
		require.NoError(t, err)

		// Verify namespaces recovered
		vfsm2 := fsm2.(*VectorConsensusFSM)
		assert.Contains(t, vfsm2.namespaces, "snap-ns-1")
		assert.Contains(t, vfsm2.namespaces, "snap-ns-2")

		require.NoError(t, fsm1.Close(context.Background()))
		require.NoError(t, fsm2.Close(context.Background()))
	})

	t.Run("recover snapshot with storage reinitializes HNSW indexes", func(t *testing.T) {
		log := createTestVectorLogger(t)
		storage := newMockVectorStorage()

		fsm1 := NewVectorConsensusFSM(VectorShardID, 1, log)
		cmd := encodeVectorCmd(t, &VectorCommand{
			Op:        OpCreateNamespace,
			Namespace: "recover-ns",
			Dimension: 32,
		})
		entries := []sm.Entry{{Index: 1, Cmd: cmd}}
		results := make([]sm.Result, 1)
		err := fsm1.Update(context.Background(), entries, results)
		require.NoError(t, err)

		var buf bytes.Buffer
		done := make(chan struct{})
		close(done)
		err = fsm1.SaveSnapshot(context.Background(), &buf, done)
		require.NoError(t, err)

		// Recover on a new FSM that has storage
		fsm2 := NewVectorConsensusFSMWithStorage(VectorShardID, 2, storage, log)
		err = fsm2.RecoverFromSnapshot(context.Background(), &buf, done)
		require.NoError(t, err)

		vfsm2 := fsm2.(*VectorConsensusFSM)
		assert.Contains(t, vfsm2.namespaces, "recover-ns")

		require.NoError(t, fsm1.Close(context.Background()))
		require.NoError(t, fsm2.Close(context.Background()))
	})

	t.Run("recover from empty snapshot data fails", func(t *testing.T) {
		log := createTestVectorLogger(t)
		fsm := NewVectorConsensusFSM(VectorShardID, 1, log)

		// Write short data that can't be read as length prefix
		var buf bytes.Buffer
		buf.Write([]byte{0x00}) // too short for 4-byte length

		done := make(chan struct{})
		close(done)
		err := fsm.RecoverFromSnapshot(context.Background(), &buf, done)
		require.Error(t, err)
		require.NoError(t, fsm.Close(context.Background()))
	})

	t.Run("recover from corrupted snapshot data fails", func(t *testing.T) {
		log := createTestVectorLogger(t)
		fsm := NewVectorConsensusFSM(VectorShardID, 1, log)

		// Write valid length prefix but invalid msgpack data
		var buf bytes.Buffer
		lenBuf := make([]byte, 4)
		binary.BigEndian.PutUint32(lenBuf, 10)
		buf.Write(lenBuf)
		buf.Write([]byte("invalidmsg")) // not valid msgpack

		done := make(chan struct{})
		close(done)
		err := fsm.RecoverFromSnapshot(context.Background(), &buf, done)
		require.Error(t, err)
		require.NoError(t, fsm.Close(context.Background()))
	})
}

// =============================================================================
// Close Tests
// =============================================================================

func TestVectorConsensusFSM_Close(t *testing.T) {
	t.Run("close returns nil", func(t *testing.T) {
		log := createTestVectorLogger(t)
		fsm := NewVectorConsensusFSM(VectorShardID, 1, log)
		assert.NoError(t, fsm.Close(context.Background()))
	})
}

// =============================================================================
// createHNSWIndex distance metric Tests
// =============================================================================

func TestVectorConsensusFSM_CreateHNSWIndex_DistanceMetrics(t *testing.T) {
	tests := []struct {
		name   string
		metric string
	}{
		{"cosine lowercase", "cosine"},
		{"cosine uppercase", "COSINE"},
		{"dot_product lowercase", "dot_product"},
		{"dot_product uppercase", "DOT_PRODUCT"},
		{"L2 default", "L2"},
		{"unknown defaults to L2", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := createTestVectorLogger(t)
			storage := newMockVectorStorage()
			fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, storage, log)

			cmd := encodeVectorCmd(t, &VectorCommand{
				Op:             OpCreateNamespace,
				Namespace:      "metric-test",
				Dimension:      4,
				DistanceMetric: tt.metric,
			})
			entries := []sm.Entry{{Index: 1, Cmd: cmd}}
			results := make([]sm.Result, 1)
			err := fsm.Update(context.Background(), entries, results)
			result := results[0]
			require.NoError(t, err)
			assert.Equal(t, uint64(1), result.Value)

			// Verify namespace was created in the FSM metadata regardless of
			// whether the HNSW index was successfully built. The HNSW index
			// requires the "vector" build tag; without it, createHNSWIndex
			// returns ErrNotCompiled but that is logged and non-fatal.
			vfsm := fsm.(*VectorConsensusFSM)
			vfsm.mu.RLock()
			nsInfo, nsExists := vfsm.namespaces["metric-test"]
			vfsm.mu.RUnlock()
			assert.True(t, nsExists, "namespace should exist in FSM metadata")
			assert.Equal(t, "metric-test", nsInfo.Name)
			assert.Equal(t, uint32(4), nsInfo.Dimension)

			// Verify storage also received the namespace
			assert.Contains(t, storage.namespaces, "metric-test")

			require.NoError(t, fsm.Close(context.Background()))
		})
	}
}

// suppress unused import warning for dberrors package
var _ = dberrors.ErrNotFound
