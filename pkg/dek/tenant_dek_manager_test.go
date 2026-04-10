// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package dek

import (
	"context"
	"crypto/rand"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockStorageBackend implements StorageBackend for testing.
type mockStorageBackend struct {
	mu         sync.RWMutex
	data       map[string][]byte
	failGet    bool
	failPut    bool
	failDelete bool
	failList   bool
}

func newMockStorageBackend() *mockStorageBackend {
	return &mockStorageBackend{
		data: make(map[string][]byte),
	}
}

func (s *mockStorageBackend) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.failGet {
		return nil, &StorageOperationError{Op: "mockStorageBackend.Get", Detail: "forced failure"}
	}
	value, ok := s.data[key]
	if !ok {
		return nil, &NotFoundError{Resource: "key", ID: key}
	}
	result := make([]byte, len(value))
	copy(result, value)
	return result, nil
}

func (s *mockStorageBackend) Put(_ context.Context, key string, data []byte) error {
	if s.failPut {
		return &StorageOperationError{Op: "mockStorageBackend.Put", Detail: "forced failure"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = data
	return nil
}

func (s *mockStorageBackend) Delete(_ context.Context, key string) error {
	if s.failDelete {
		return &StorageOperationError{Op: "mockStorageBackend.Delete", Detail: "forced failure"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

func (s *mockStorageBackend) List(_ context.Context, prefix string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.failList {
		return nil, &StorageOperationError{Op: "mockStorageBackend.List", Detail: "forced failure"}
	}
	var keys []string
	for k := range s.data {
		if len(prefix) == 0 || strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (s *mockStorageBackend) Close() error {
	return nil
}

// newInitializedClusterDEK creates a ClusterDEKManager that is initialized
// and ready for use in tenant DEK tests.
func newInitializedClusterDEK(t *testing.T) *ClusterDEKManager {
	t.Helper()
	mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
	require.NoError(t, err)
	require.NoError(t, mgr.Initialize(context.Background(), AlgorithmAES256GCM, 32))
	return mgr
}

// TestNewTenantDEKManager tests manager creation.
func TestNewTenantDEKManager(t *testing.T) {
	t.Run("valid creation with defaults", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t),
			newMockStorageBackend(),
			discardLogger(),
			nil,
		)
		require.NoError(t, err)
		require.NotNil(t, mgr)
		assert.NoError(t, mgr.Close())
	})

	t.Run("valid creation with custom config", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t),
			newMockStorageBackend(),
			discardLogger(),
			&TenantDEKManagerConfig{
				DefaultAlgorithm: "AES-128-GCM",
				DefaultKeyBytes:  16,
			},
		)
		require.NoError(t, err)
		require.NotNil(t, mgr)
		assert.NoError(t, mgr.Close())
	})

	t.Run("nil cluster DEK manager", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(nil, newMockStorageBackend(), discardLogger(), nil)
		require.Error(t, err)
		assert.Nil(t, mgr)
		var nilErr *NilInputError
		assert.ErrorAs(t, err, &nilErr)
	})

	t.Run("nil storage", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(newInitializedClusterDEK(t), nil, discardLogger(), nil)
		require.Error(t, err)
		assert.Nil(t, mgr)
		var nilErr *NilInputError
		assert.ErrorAs(t, err, &nilErr)
	})

	t.Run("nil logger", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(newInitializedClusterDEK(t), newMockStorageBackend(), nil, nil)
		require.Error(t, err)
		assert.Nil(t, mgr)
		var nilErr *NilInputError
		assert.ErrorAs(t, err, &nilErr)
	})
}

// TestGetOrCreateTenantDEK tests DEK creation and retrieval.
func TestGetOrCreateTenantDEK(t *testing.T) {
	t.Run("creates new DEK for new tenant", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), newMockStorageBackend(), discardLogger(), nil,
		)
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		ctx := context.Background()
		dek, err := mgr.GetOrCreateTenantDEK(ctx, "tenant-abc")
		require.NoError(t, err)
		assert.Len(t, dek, 32)

		// Second call returns same DEK (cached).
		dek2, err := mgr.GetOrCreateTenantDEK(ctx, "tenant-abc")
		require.NoError(t, err)
		assert.Equal(t, dek, dek2)
	})

	t.Run("empty tenant ID", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), newMockStorageBackend(), discardLogger(), nil,
		)
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		dek, err := mgr.GetOrCreateTenantDEK(context.Background(), "")
		require.Error(t, err)
		assert.Nil(t, dek)
		var invErr *InvalidInputError
		assert.ErrorAs(t, err, &invErr)
	})

	t.Run("closed manager", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), newMockStorageBackend(), discardLogger(), nil,
		)
		require.NoError(t, err)
		_ = mgr.Close()

		dek, err := mgr.GetOrCreateTenantDEK(context.Background(), "tenant-xyz")
		require.Error(t, err)
		assert.Nil(t, dek)
		var mcErr *ManagerClosedError
		assert.ErrorAs(t, err, &mcErr)
	})

	t.Run("storage put failure", func(t *testing.T) {
		storage := newMockStorageBackend()
		storage.failPut = true
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), storage, discardLogger(), nil,
		)
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		dek, err := mgr.GetOrCreateTenantDEK(context.Background(), "tenant-fail")
		require.Error(t, err)
		assert.Nil(t, dek)
	})
}

// TestGetTenantDEK tests DEK retrieval.
func TestGetTenantDEK(t *testing.T) {
	t.Run("retrieves existing DEK", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), newMockStorageBackend(), discardLogger(), nil,
		)
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		ctx := context.Background()
		createdDEK, err := mgr.GetOrCreateTenantDEK(ctx, "tenant-123")
		require.NoError(t, err)

		retrievedDEK, err := mgr.GetTenantDEK(ctx, "tenant-123")
		require.NoError(t, err)
		assert.Equal(t, createdDEK, retrievedDEK)
	})

	t.Run("returns not found for non-existent tenant", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), newMockStorageBackend(), discardLogger(), nil,
		)
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		dek, err := mgr.GetTenantDEK(context.Background(), "non-existent-tenant")
		require.Error(t, err)
		assert.Nil(t, dek)
		assert.True(t, IsNotFound(err))
	})

	t.Run("empty tenant ID", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), newMockStorageBackend(), discardLogger(), nil,
		)
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		dek, err := mgr.GetTenantDEK(context.Background(), "")
		require.Error(t, err)
		assert.Nil(t, dek)
	})

	t.Run("closed manager", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), newMockStorageBackend(), discardLogger(), nil,
		)
		require.NoError(t, err)
		_ = mgr.Close()

		dek, err := mgr.GetTenantDEK(context.Background(), "tenant-xyz")
		require.Error(t, err)
		assert.Nil(t, dek)
	})

	t.Run("cache hit", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), newMockStorageBackend(), discardLogger(), nil,
		)
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		ctx := context.Background()
		_, err = mgr.GetOrCreateTenantDEK(ctx, "cached-tenant")
		require.NoError(t, err)

		// Second retrieval should be from cache.
		dek, err := mgr.GetTenantDEK(ctx, "cached-tenant")
		require.NoError(t, err)
		assert.Len(t, dek, 32)
	})

	t.Run("loads from storage when not cached", func(t *testing.T) {
		storage := newMockStorageBackend()
		clusterDEK := newInitializedClusterDEK(t)

		mgr1, err := NewTenantDEKManager(clusterDEK, storage, discardLogger(), nil)
		require.NoError(t, err)

		ctx := context.Background()
		createdDEK, err := mgr1.GetOrCreateTenantDEK(ctx, "tenant-persist")
		require.NoError(t, err)
		if err := mgr1.Close(); err != nil {
			t.Fatal(err)
		}

		// New manager with same storage, no cache.
		mgr2, err := NewTenantDEKManager(clusterDEK, storage, discardLogger(), nil)
		require.NoError(t, err)
		defer func() { _ = mgr2.Close() }()

		retrievedDEK, err := mgr2.GetTenantDEK(ctx, "tenant-persist")
		require.NoError(t, err)
		assert.Equal(t, createdDEK, retrievedDEK)
	})

	t.Run("cluster DEK get failure on unwrap", func(t *testing.T) {
		storage := newMockStorageBackend()
		clusterDEK := newInitializedClusterDEK(t)

		mgr1, err := NewTenantDEKManager(clusterDEK, storage, discardLogger(), nil)
		require.NoError(t, err)

		ctx := context.Background()
		_, err = mgr1.GetOrCreateTenantDEK(ctx, "unwrap-fail")
		require.NoError(t, err)
		if err := mgr1.Close(); err != nil {
			t.Fatal(err)
		}

		// Close the cluster DEK to break unwrap.
		if err := clusterDEK.Close(); err != nil {
			t.Fatal(err)
		}

		mgr2, err := NewTenantDEKManager(clusterDEK, storage, discardLogger(), nil)
		require.NoError(t, err)
		defer func() { _ = mgr2.Close() }()

		_, err = mgr2.GetTenantDEK(ctx, "unwrap-fail")
		require.Error(t, err)
	})
}

// TestRotateTenantDEK tests DEK rotation.
func TestRotateTenantDEK(t *testing.T) {
	t.Run("rotates existing DEK", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), newMockStorageBackend(), discardLogger(), nil,
		)
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		ctx := context.Background()
		originalDEK, err := mgr.GetOrCreateTenantDEK(ctx, "tenant-rotate")
		require.NoError(t, err)

		originalStatus, err := mgr.GetTenantDEKStatus(ctx, "tenant-rotate")
		require.NoError(t, err)
		assert.Equal(t, uint64(1), originalStatus.Version)

		require.NoError(t, mgr.RotateTenantDEK(ctx, "tenant-rotate"))

		newDEK, err := mgr.GetTenantDEK(ctx, "tenant-rotate")
		require.NoError(t, err)
		assert.NotEqual(t, originalDEK, newDEK)

		newStatus, err := mgr.GetTenantDEKStatus(ctx, "tenant-rotate")
		require.NoError(t, err)
		assert.Equal(t, uint64(2), newStatus.Version)
		assert.False(t, newStatus.RotatedAt.IsZero())
	})

	t.Run("empty tenant ID", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), newMockStorageBackend(), discardLogger(), nil,
		)
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		err = mgr.RotateTenantDEK(context.Background(), "")
		require.Error(t, err)
		var invErr *InvalidInputError
		assert.ErrorAs(t, err, &invErr)
	})

	t.Run("non-existent tenant", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), newMockStorageBackend(), discardLogger(), nil,
		)
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		err = mgr.RotateTenantDEK(context.Background(), "non-existent")
		require.Error(t, err)
		assert.True(t, IsNotFound(err))
	})

	t.Run("closed manager", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), newMockStorageBackend(), discardLogger(), nil,
		)
		require.NoError(t, err)
		_, err = mgr.GetOrCreateTenantDEK(context.Background(), "tenant-close")
		require.NoError(t, err)
		_ = mgr.Close()

		err = mgr.RotateTenantDEK(context.Background(), "tenant-close")
		require.Error(t, err)
		var mcErr *ManagerClosedError
		assert.ErrorAs(t, err, &mcErr)
	})

	t.Run("storage put failure during rotation", func(t *testing.T) {
		storage := newMockStorageBackend()
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), storage, discardLogger(), nil,
		)
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		ctx := context.Background()
		_, err = mgr.GetOrCreateTenantDEK(ctx, "tenant-fail-rotate")
		require.NoError(t, err)

		storage.failPut = true
		err = mgr.RotateTenantDEK(ctx, "tenant-fail-rotate")
		require.Error(t, err)
	})

	t.Run("rotates from storage when not cached", func(t *testing.T) {
		storage := newMockStorageBackend()
		clusterDEK := newInitializedClusterDEK(t)

		mgr1, err := NewTenantDEKManager(clusterDEK, storage, discardLogger(), nil)
		require.NoError(t, err)

		ctx := context.Background()
		_, err = mgr1.GetOrCreateTenantDEK(ctx, "tenant-rotate-storage")
		require.NoError(t, err)
		if err := mgr1.Close(); err != nil {
			t.Fatal(err)
		}

		mgr2, err := NewTenantDEKManager(clusterDEK, storage, discardLogger(), nil)
		require.NoError(t, err)
		defer func() { _ = mgr2.Close() }()

		require.NoError(t, mgr2.RotateTenantDEK(ctx, "tenant-rotate-storage"))

		status, err := mgr2.GetTenantDEKStatus(ctx, "tenant-rotate-storage")
		require.NoError(t, err)
		assert.Equal(t, uint64(2), status.Version)
	})
}

// TestDeleteTenantDEK tests DEK deletion.
func TestDeleteTenantDEK(t *testing.T) {
	t.Run("deletes existing DEK", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), newMockStorageBackend(), discardLogger(), nil,
		)
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		ctx := context.Background()
		_, err = mgr.GetOrCreateTenantDEK(ctx, "tenant-delete")
		require.NoError(t, err)

		require.NoError(t, mgr.DeleteTenantDEK(ctx, "tenant-delete"))

		_, err = mgr.GetTenantDEK(ctx, "tenant-delete")
		require.Error(t, err)
		assert.True(t, IsNotFound(err))
	})

	t.Run("empty tenant ID", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), newMockStorageBackend(), discardLogger(), nil,
		)
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		err = mgr.DeleteTenantDEK(context.Background(), "")
		require.Error(t, err)
	})

	t.Run("deleting non-existent tenant is idempotent", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), newMockStorageBackend(), discardLogger(), nil,
		)
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		err = mgr.DeleteTenantDEK(context.Background(), "non-existent-tenant")
		require.NoError(t, err)
	})

	t.Run("closed manager", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), newMockStorageBackend(), discardLogger(), nil,
		)
		require.NoError(t, err)
		_ = mgr.Close()

		err = mgr.DeleteTenantDEK(context.Background(), "tenant-xyz")
		require.Error(t, err)
		var mcErr *ManagerClosedError
		assert.ErrorAs(t, err, &mcErr)
	})

	t.Run("storage delete failure", func(t *testing.T) {
		storage := newMockStorageBackend()
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), storage, discardLogger(), nil,
		)
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		ctx := context.Background()
		_, err = mgr.GetOrCreateTenantDEK(ctx, "tenant-fail-delete")
		require.NoError(t, err)

		storage.failDelete = true
		err = mgr.DeleteTenantDEK(ctx, "tenant-fail-delete")
		require.Error(t, err)
	})
}

// TestGetTenantDEKStatus tests status retrieval.
func TestGetTenantDEKStatus(t *testing.T) {
	t.Run("returns status for existing tenant", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), newMockStorageBackend(), discardLogger(), nil,
		)
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		ctx := context.Background()
		_, err = mgr.GetOrCreateTenantDEK(ctx, "tenant-status")
		require.NoError(t, err)

		status, err := mgr.GetTenantDEKStatus(ctx, "tenant-status")
		require.NoError(t, err)
		assert.Equal(t, "tenant-status", status.TenantID)
		assert.Equal(t, uint64(1), status.Version)
		assert.Equal(t, AlgorithmAES256GCM, status.Algorithm)
		assert.Equal(t, 32, status.KeyBytes)
		assert.False(t, status.CreatedAt.IsZero())
	})

	t.Run("empty tenant ID", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), newMockStorageBackend(), discardLogger(), nil,
		)
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		status, err := mgr.GetTenantDEKStatus(context.Background(), "")
		require.Error(t, err)
		assert.Nil(t, status)
	})

	t.Run("non-existent tenant", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), newMockStorageBackend(), discardLogger(), nil,
		)
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		status, err := mgr.GetTenantDEKStatus(context.Background(), "non-existent")
		require.Error(t, err)
		assert.Nil(t, status)
		assert.True(t, IsNotFound(err))
	})

	t.Run("closed manager", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), newMockStorageBackend(), discardLogger(), nil,
		)
		require.NoError(t, err)
		_ = mgr.Close()

		status, err := mgr.GetTenantDEKStatus(context.Background(), "tenant-xyz")
		require.Error(t, err)
		assert.Nil(t, status)
	})

	t.Run("loads status from storage when not cached", func(t *testing.T) {
		storage := newMockStorageBackend()
		clusterDEK := newInitializedClusterDEK(t)

		mgr1, err := NewTenantDEKManager(clusterDEK, storage, discardLogger(), nil)
		require.NoError(t, err)
		ctx := context.Background()
		_, err = mgr1.GetOrCreateTenantDEK(ctx, "tenant-status-storage")
		require.NoError(t, err)
		_ = mgr1.Close()

		mgr2, err := NewTenantDEKManager(clusterDEK, storage, discardLogger(), nil)
		require.NoError(t, err)
		defer func() { _ = mgr2.Close() }()

		status, err := mgr2.GetTenantDEKStatus(ctx, "tenant-status-storage")
		require.NoError(t, err)
		assert.Equal(t, "tenant-status-storage", status.TenantID)
		assert.Equal(t, uint64(1), status.Version)
	})
}

// TestListTenantDEKs tests listing tenant DEKs.
func TestListTenantDEKs(t *testing.T) {
	t.Run("lists all tenant DEKs", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), newMockStorageBackend(), discardLogger(), nil,
		)
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		ctx := context.Background()
		tenantIDs := []string{"tenant-1", "tenant-2", "tenant-3"}
		for _, id := range tenantIDs {
			_, err = mgr.GetOrCreateTenantDEK(ctx, id)
			require.NoError(t, err)
		}

		listed, err := mgr.ListTenantDEKs(ctx)
		require.NoError(t, err)
		assert.Len(t, listed, 3)

		for _, id := range tenantIDs {
			found := false
			for _, listedID := range listed {
				if listedID == id {
					found = true
					break
				}
			}
			assert.True(t, found, "tenant %s should be in list", id)
		}
	})

	t.Run("returns empty list when no tenants", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), newMockStorageBackend(), discardLogger(), nil,
		)
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		listed, err := mgr.ListTenantDEKs(context.Background())
		require.NoError(t, err)
		assert.Empty(t, listed)
	})

	t.Run("closed manager", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), newMockStorageBackend(), discardLogger(), nil,
		)
		require.NoError(t, err)
		_ = mgr.Close()

		listed, err := mgr.ListTenantDEKs(context.Background())
		require.Error(t, err)
		assert.Nil(t, listed)
	})

	t.Run("storage list failure", func(t *testing.T) {
		storage := newMockStorageBackend()
		storage.failList = true
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), storage, discardLogger(), nil,
		)
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		listed, err := mgr.ListTenantDEKs(context.Background())
		require.Error(t, err)
		assert.Nil(t, listed)
	})
}

// TestTenantDEKManagerClose tests proper cleanup on close.
func TestTenantDEKManagerClose(t *testing.T) {
	t.Run("clears cached DEKs", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), newMockStorageBackend(), discardLogger(), nil,
		)
		require.NoError(t, err)

		ctx := context.Background()
		_, err = mgr.GetOrCreateTenantDEK(ctx, "tenant-close-test")
		require.NoError(t, err)

		require.NoError(t, mgr.Close())

		impl := mgr.(*defaultTenantDEKManager)
		assert.Nil(t, impl.cache)
	})

	t.Run("double close is safe", func(t *testing.T) {
		mgr, err := NewTenantDEKManager(
			newInitializedClusterDEK(t), newMockStorageBackend(), discardLogger(), nil,
		)
		require.NoError(t, err)

		assert.NoError(t, mgr.Close())
		assert.NoError(t, mgr.Close())
	})
}

// TestTenantDEKManagerConcurrentAccess tests thread-safety.
func TestTenantDEKManagerConcurrentAccess(t *testing.T) {
	mgr, err := NewTenantDEKManager(
		newInitializedClusterDEK(t), newMockStorageBackend(), discardLogger(), nil,
	)
	require.NoError(t, err)
	defer func() { _ = mgr.Close() }()

	ctx := context.Background()
	const numGoroutines = 50
	const numOperations = 20

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			tenantID := "concurrent-tenant"
			for j := 0; j < numOperations; j++ {
				switch j % 4 {
				case 0:
					_, _ = mgr.GetOrCreateTenantDEK(ctx, tenantID)
				case 1:
					_, _ = mgr.GetTenantDEK(ctx, tenantID)
				case 2:
					_, _ = mgr.GetTenantDEKStatus(ctx, tenantID)
				case 3:
					_, _ = mgr.ListTenantDEKs(ctx)
				}
			}
		}()
	}

	wg.Wait()
}

// TestTenantIsolation tests that tenants cannot access each other's DEKs.
func TestTenantIsolation(t *testing.T) {
	mgr, err := NewTenantDEKManager(
		newInitializedClusterDEK(t), newMockStorageBackend(), discardLogger(), nil,
	)
	require.NoError(t, err)
	defer func() { _ = mgr.Close() }()

	ctx := context.Background()

	dekA, err := mgr.GetOrCreateTenantDEK(ctx, "tenant-a")
	require.NoError(t, err)

	dekB, err := mgr.GetOrCreateTenantDEK(ctx, "tenant-b")
	require.NoError(t, err)

	assert.NotEqual(t, dekA, dekB)

	retrievedA, err := mgr.GetTenantDEK(ctx, "tenant-a")
	require.NoError(t, err)
	assert.Equal(t, dekA, retrievedA)

	retrievedB, err := mgr.GetTenantDEK(ctx, "tenant-b")
	require.NoError(t, err)
	assert.Equal(t, dekB, retrievedB)
}

// TestTenantDEKEntryEncodeDecode tests binary encoding/decoding.
func TestTenantDEKEntryEncodeDecode(t *testing.T) {
	t.Run("round-trip encoding", func(t *testing.T) {
		wrappedDEK := make([]byte, 64)
		_, err := io.ReadFull(rand.Reader, wrappedDEK)
		require.NoError(t, err)

		now := time.Now()
		entry := &tenantDEKEntry{
			TenantID:   "test-tenant",
			WrappedDEK: wrappedDEK,
			Version:    5,
			Algorithm:  AlgorithmAES256GCM,
			KeyBytes:   32,
			CreatedAt:  now,
			RotatedAt:  now.Add(time.Hour),
		}

		encoded, err := encodeTenantDEKEntry(entry)
		require.NoError(t, err)
		assert.NotEmpty(t, encoded)

		decoded, err := decodeTenantDEKEntry(encoded)
		require.NoError(t, err)
		assert.Equal(t, entry.TenantID, decoded.TenantID)
		assert.Equal(t, entry.WrappedDEK, decoded.WrappedDEK)
		assert.Equal(t, entry.Version, decoded.Version)
		assert.Equal(t, entry.Algorithm, decoded.Algorithm)
		assert.Equal(t, entry.KeyBytes, decoded.KeyBytes)
		assert.WithinDuration(t, entry.CreatedAt, decoded.CreatedAt, time.Microsecond)
		assert.WithinDuration(t, entry.RotatedAt, decoded.RotatedAt, time.Microsecond)
	})

	t.Run("decode too short data", func(t *testing.T) {
		_, err := decodeTenantDEKEntry([]byte{1, 2, 3})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "too short")
	})

	t.Run("decode invalid algorithm length", func(t *testing.T) {
		data := make([]byte, 40)
		// Algorithm length exceeds data.
		data[8] = 0x00
		data[9] = 0x20 // 32 bytes, but only ~28 remain
		_, err := decodeTenantDEKEntry(data)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "algorithm")
	})

	t.Run("decode invalid tenant ID length", func(t *testing.T) {
		data := make([]byte, 100)
		// Algorithm length = 5.
		data[8] = 0x00
		data[9] = 0x05
		copy(data[10:15], []byte("AES-1"))
		// Tenant length exceeds data.
		data[15] = 0xFF
		data[16] = 0xFF
		_, err := decodeTenantDEKEntry(data)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "tenant ID")
	})

	t.Run("decode invalid wrapped DEK length", func(t *testing.T) {
		data := make([]byte, 100)
		// Algorithm length = 3.
		data[8] = 0x00
		data[9] = 0x03
		copy(data[10:13], []byte("AES"))
		// Tenant length = 3.
		data[13] = 0x00
		data[14] = 0x03
		copy(data[15:18], []byte("abc"))
		// Key bytes.
		data[18] = 0x00
		data[19] = 0x00
		data[20] = 0x00
		data[21] = 0x20
		// Wrapped DEK length exceeds data.
		data[22] = 0xFF
		data[23] = 0xFF
		data[24] = 0xFF
		data[25] = 0xFF
		_, err := decodeTenantDEKEntry(data)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "wrapped DEK")
	})
}

// TestStorageKeyFormat tests the storage key format.
func TestStorageKeyFormat(t *testing.T) {
	key := tenantDEKStorageKey("my-tenant")
	assert.Equal(t, "__sys:dek/tenant/my-tenant", key)
}

// TestWrapUnwrapDEK tests wrap/unwrap via the tenant manager.
func TestWrapUnwrapDEK(t *testing.T) {
	t.Run("wrap failure when cluster DEK unavailable", func(t *testing.T) {
		clusterDEK := newInitializedClusterDEK(t)
		storage := newMockStorageBackend()
		mgr, err := NewTenantDEKManager(clusterDEK, storage, discardLogger(), nil)
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		// Close cluster DEK to force failure.
		if err := clusterDEK.Close(); err != nil {
			t.Fatal(err)
		}

		_, err = mgr.GetOrCreateTenantDEK(context.Background(), "wrap-fail")
		require.Error(t, err)
	})
}

// BenchmarkGetOrCreateTenantDEK benchmarks tenant DEK creation.
func BenchmarkGetOrCreateTenantDEK(b *testing.B) {
	barrier := newMockEncryptionBarrier()
	logger := discardLogger()
	clusterDEK, _ := NewClusterDEKManager(barrier, logger)
	_ = clusterDEK.Initialize(context.Background(), AlgorithmAES256GCM, 32)

	storage := newMockStorageBackend()
	mgr, _ := NewTenantDEKManager(clusterDEK, storage, logger, nil)
	defer func() { _ = mgr.Close() }()

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = mgr.GetOrCreateTenantDEK(ctx, "bench-tenant")
	}
}

// BenchmarkGetTenantDEK benchmarks cached tenant DEK retrieval.
func BenchmarkGetTenantDEK(b *testing.B) {
	barrier := newMockEncryptionBarrier()
	logger := discardLogger()
	clusterDEK, _ := NewClusterDEKManager(barrier, logger)
	_ = clusterDEK.Initialize(context.Background(), AlgorithmAES256GCM, 32)

	storage := newMockStorageBackend()
	mgr, _ := NewTenantDEKManager(clusterDEK, storage, logger, nil)
	defer func() { _ = mgr.Close() }()

	ctx := context.Background()
	_, _ = mgr.GetOrCreateTenantDEK(ctx, "bench-tenant")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = mgr.GetTenantDEK(ctx, "bench-tenant")
	}
}

// BenchmarkConcurrentTenantDEKAccess benchmarks concurrent tenant DEK access.
func BenchmarkConcurrentTenantDEKAccess(b *testing.B) {
	barrier := newMockEncryptionBarrier()
	logger := discardLogger()
	clusterDEK, _ := NewClusterDEKManager(barrier, logger)
	_ = clusterDEK.Initialize(context.Background(), AlgorithmAES256GCM, 32)

	storage := newMockStorageBackend()
	mgr, _ := NewTenantDEKManager(clusterDEK, storage, logger, nil)
	defer func() { _ = mgr.Close() }()

	ctx := context.Background()
	_, _ = mgr.GetOrCreateTenantDEK(ctx, "bench-tenant")

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = mgr.GetTenantDEK(ctx, "bench-tenant")
		}
	})
}
