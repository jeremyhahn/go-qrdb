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
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockEncryptionBarrier implements seal.EncryptionBarrier for testing.
type mockEncryptionBarrier struct {
	sealed bool
	mu     sync.RWMutex
}

func newMockEncryptionBarrier() *mockEncryptionBarrier {
	return &mockEncryptionBarrier{sealed: false}
}

func (m *mockEncryptionBarrier) Encrypt(plaintext []byte) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.sealed {
		return nil, &SealedError{Op: "Encrypt"}
	}
	out := make([]byte, len(plaintext)+4)
	copy(out, []byte("ENC:"))
	copy(out[4:], plaintext)
	return out, nil
}

func (m *mockEncryptionBarrier) Decrypt(ciphertext []byte) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.sealed {
		return nil, &SealedError{Op: "Decrypt"}
	}
	if len(ciphertext) < 4 {
		return nil, &CiphertextTooShortError{Op: "Decrypt", Detail: "ciphertext too short"}
	}
	return ciphertext[4:], nil
}

func (m *mockEncryptionBarrier) IsSealed() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sealed
}

func (m *mockEncryptionBarrier) seal() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sealed = true
}

// discardLogger returns a *slog.Logger that discards all output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestNewClusterDEKManager tests manager creation.
func TestNewClusterDEKManager(t *testing.T) {
	t.Run("valid creation", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		require.NotNil(t, mgr)
		assert.NoError(t, mgr.Close())
	})

	t.Run("nil barrier", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(nil, discardLogger())
		require.Error(t, err)
		assert.Nil(t, mgr)
		var nilErr *NilInputError
		assert.ErrorAs(t, err, &nilErr)
		assert.Equal(t, "barrier", nilErr.Field)
	})

	t.Run("nil logger", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), nil)
		require.Error(t, err)
		assert.Nil(t, mgr)
		var nilErr *NilInputError
		assert.ErrorAs(t, err, &nilErr)
		assert.Equal(t, "logger", nilErr.Field)
	})
}

// TestInitialize tests DEK initialization.
func TestInitialize(t *testing.T) {
	t.Run("valid initialization", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		err = mgr.Initialize(context.Background(), AlgorithmAES256GCM, 32)
		require.NoError(t, err)
		assert.True(t, mgr.initialized.Load())
		assert.Equal(t, uint64(1), mgr.GetDEKVersion())
	})

	t.Run("default algorithm", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		err = mgr.Initialize(context.Background(), "", 32)
		require.NoError(t, err)
		assert.True(t, mgr.initialized.Load())
	})

	t.Run("default key size", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		err = mgr.Initialize(context.Background(), AlgorithmAES256GCM, 0)
		require.NoError(t, err)
		assert.True(t, mgr.initialized.Load())
	})

	t.Run("invalid algorithm", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		err = mgr.Initialize(context.Background(), "INVALID", 32)
		require.Error(t, err)
		var uaErr *UnsupportedAlgorithmError
		assert.ErrorAs(t, err, &uaErr)
		assert.Equal(t, "INVALID", uaErr.Algorithm)
	})

	t.Run("sealed barrier", func(t *testing.T) {
		barrier := newMockEncryptionBarrier()
		barrier.seal()
		mgr, err := NewClusterDEKManager(barrier, discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		err = mgr.Initialize(context.Background(), AlgorithmAES256GCM, 32)
		require.Error(t, err)
		var sealErr *SealedError
		assert.ErrorAs(t, err, &sealErr)
	})
}

// TestInitializeIdempotent tests that Initialize is idempotent.
func TestInitializeIdempotent(t *testing.T) {
	mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
	require.NoError(t, err)
	defer func() { _ = mgr.Close() }()

	ctx := context.Background()
	require.NoError(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))
	firstVersion := mgr.GetDEKVersion()

	require.NoError(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))
	assert.Equal(t, firstVersion, mgr.GetDEKVersion())
}

// TestWithCurrentDEK tests scoped access to the current DEK.
func TestWithCurrentDEK(t *testing.T) {
	t.Run("uninitialized manager", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		called := false
		err = mgr.WithCurrentDEK(func([]byte) error {
			called = true
			return nil
		})
		require.Error(t, err)
		assert.False(t, called, "callback must not run without a DEK")
		var niErr *NotInitializedError
		assert.ErrorAs(t, err, &niErr)
	})

	t.Run("nil callback", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		require.NoError(t, mgr.Initialize(context.Background(), AlgorithmAES256GCM, 32))

		err = mgr.WithCurrentDEK(nil)
		require.Error(t, err)
		var nilErr *NilInputError
		assert.ErrorAs(t, err, &nilErr)
	})

	t.Run("initialized manager", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		require.NoError(t, mgr.Initialize(context.Background(), AlgorithmAES256GCM, 32))

		require.NoError(t, mgr.WithCurrentDEK(func(dek []byte) error {
			assert.Len(t, dek, 32)
			return nil
		}))
	})

	t.Run("aliases protected memory rather than copying", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		require.NoError(t, mgr.Initialize(context.Background(), AlgorithmAES256GCM, 32))

		require.NoError(t, mgr.WithCurrentDEK(func(dek []byte) error {
			assert.Same(t, &mgr.dekPlaintext.Bytes()[0], &dek[0],
				"callback must see the mlocked buffer, not a heap copy")
			return nil
		}))
	})

	t.Run("callback error propagates", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		require.NoError(t, mgr.Initialize(context.Background(), AlgorithmAES256GCM, 32))

		sentinel := &NotImplementedError{Message: "from callback"}
		err = mgr.WithCurrentDEK(func([]byte) error { return sentinel })
		assert.ErrorIs(t, err, error(sentinel))
	})

	t.Run("nil plaintext after manual reset", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		require.NoError(t, mgr.Initialize(context.Background(), AlgorithmAES256GCM, 32))

		mgr.mu.Lock()
		mgr.dekPlaintext = nil
		mgr.mu.Unlock()

		err = mgr.WithCurrentDEK(func([]byte) error { return nil })
		require.Error(t, err)
		assert.Contains(t, err.Error(), "DEK plaintext not initialized")
	})
}

// TestRotate tests DEK rotation.
func TestRotate(t *testing.T) {
	t.Run("uninitialized manager", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		err = mgr.Rotate(context.Background())
		require.Error(t, err)
		var niErr *NotInitializedError
		assert.ErrorAs(t, err, &niErr)
	})

	t.Run("successful rotation", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		ctx := context.Background()
		require.NoError(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))

		dek1 := copyCurrentDEK(t, mgr)
		v1 := mgr.GetDEKVersion()

		require.NoError(t, mgr.Rotate(ctx))

		dek2 := copyCurrentDEK(t, mgr)
		v2 := mgr.GetDEKVersion()

		assert.Equal(t, v1+1, v2)
		assert.NotEqual(t, dek1, dek2)
	})

	t.Run("sealed barrier", func(t *testing.T) {
		barrier := newMockEncryptionBarrier()
		mgr, err := NewClusterDEKManager(barrier, discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		ctx := context.Background()
		require.NoError(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))

		barrier.seal()
		err = mgr.Rotate(ctx)
		require.Error(t, err)
		var sealErr *SealedError
		assert.ErrorAs(t, err, &sealErr)
	})

	t.Run("multiple rotations", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		ctx := context.Background()
		require.NoError(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))

		for i := 0; i < 5; i++ {
			require.NoError(t, mgr.Rotate(ctx))
		}
		assert.Equal(t, uint64(6), mgr.GetDEKVersion())
	})
}

// TestSyncToNode tests node synchronization.
func TestSyncToNode(t *testing.T) {
	t.Run("uninitialized manager", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		err = mgr.SyncToNode(context.Background(), 123)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not initialized")
	})

	t.Run("sync and verify", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		ctx := context.Background()
		require.NoError(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))

		nodeID := uint64(123)
		assert.False(t, mgr.IsNodeSynced(nodeID))

		require.NoError(t, mgr.SyncToNode(ctx, nodeID))
		assert.True(t, mgr.IsNodeSynced(nodeID))

		// Rotate clears sync state.
		require.NoError(t, mgr.Rotate(ctx))
		assert.False(t, mgr.IsNodeSynced(nodeID))
	})
}

// TestGetStatus tests status retrieval.
func TestGetStatus(t *testing.T) {
	t.Run("uninitialized manager", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		status, err := mgr.GetStatus(context.Background())
		require.NoError(t, err)
		statusResp, ok := status.(*ClusterDEKStatus)
		require.True(t, ok)
		assert.False(t, statusResp.Initialized)
	})

	t.Run("initialized manager", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		ctx := context.Background()
		require.NoError(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))
		require.NoError(t, mgr.SyncToNode(ctx, 1))

		status, err := mgr.GetStatus(ctx)
		require.NoError(t, err)
		statusResp, ok := status.(*ClusterDEKStatus)
		require.True(t, ok)

		assert.True(t, statusResp.Initialized)
		assert.Equal(t, uint64(1), statusResp.Version)
		assert.Equal(t, AlgorithmAES256GCM, statusResp.Algorithm)
		assert.Equal(t, 32, statusResp.KeyBytes)
		assert.Len(t, statusResp.SyncedNodes, 1)
	})

	t.Run("with active reencryption", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		ctx := context.Background()
		require.NoError(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))
		require.NoError(t, mgr.StartReencryption(ctx))

		status, err := mgr.GetStatus(ctx)
		require.NoError(t, err)
		statusResp, ok := status.(*ClusterDEKStatus)
		require.True(t, ok)
		require.NotNil(t, statusResp.Reencryption)
		assert.True(t, statusResp.Reencryption.Active)
	})
}

// TestReencryption tests re-encryption operations.
func TestReencryption(t *testing.T) {
	t.Run("start re-encryption", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		ctx := context.Background()
		require.NoError(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))

		require.NoError(t, mgr.StartReencryption(ctx))

		status, err := mgr.GetReencryptionStatus(ctx)
		require.NoError(t, err)
		info, ok := status.(*ReencryptionInfo)
		require.True(t, ok)
		assert.True(t, info.Active)
	})

	t.Run("cancel re-encryption", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		ctx := context.Background()
		require.NoError(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))
		require.NoError(t, mgr.StartReencryption(ctx))

		require.NoError(t, mgr.CancelReencryption(ctx))

		status, err := mgr.GetReencryptionStatus(ctx)
		require.NoError(t, err)
		info, ok := status.(*ReencryptionInfo)
		require.True(t, ok)
		assert.False(t, info.Active)
	})

	t.Run("double start error", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		ctx := context.Background()
		require.NoError(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))
		require.NoError(t, mgr.StartReencryption(ctx))

		err = mgr.StartReencryption(ctx)
		require.Error(t, err)
		var aipErr *AlreadyInProgressError
		assert.ErrorAs(t, err, &aipErr)
	})

	t.Run("cancel when not active", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		err = mgr.CancelReencryption(context.Background())
		require.Error(t, err)
		var naoErr *NoActiveOperationError
		assert.ErrorAs(t, err, &naoErr)
	})

	t.Run("start before initialization", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		err = mgr.StartReencryption(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not initialized")
	})

	t.Run("status when inactive", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		status, err := mgr.GetReencryptionStatus(context.Background())
		require.NoError(t, err)
		info, ok := status.(*ReencryptionInfo)
		require.True(t, ok)
		assert.False(t, info.Active)
	})
}

// TestRecoverDEK tests DEK recovery placeholder.
func TestRecoverDEK(t *testing.T) {
	mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
	require.NoError(t, err)
	defer func() { _ = mgr.Close() }()

	err = mgr.RecoverDEK(context.Background(), nil)
	require.Error(t, err)
	var niErr *NotImplementedError
	assert.ErrorAs(t, err, &niErr)
	assert.Contains(t, err.Error(), "not implemented")
}

// TestClose tests proper cleanup on close.
func TestClose(t *testing.T) {
	t.Run("zeroes DEK and marks uninitialized", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)

		ctx := context.Background()
		require.NoError(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))
		require.NoError(t, mgr.StartReencryption(ctx))

		require.NoError(t, mgr.Close())
		assert.False(t, mgr.initialized.Load())
		assert.Nil(t, mgr.dekPlaintext)
	})

	t.Run("close without initialization", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		require.NoError(t, mgr.Close())
	})
}

// TestGetMetrics tests metrics retrieval.
func TestGetMetrics(t *testing.T) {
	mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
	require.NoError(t, err)
	defer func() { _ = mgr.Close() }()

	ctx := context.Background()
	metrics1 := mgr.GetMetrics()
	assert.Equal(t, uint64(0), metrics1["init_operations"])

	require.NoError(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))
	metrics2 := mgr.GetMetrics()
	assert.Equal(t, uint64(1), metrics2["init_operations"])

	_ = mgr.WithCurrentDEK(func([]byte) error { return nil })
	_ = mgr.WithCurrentDEK(func([]byte) error { return nil })
	_ = mgr.Rotate(ctx)
	_ = mgr.SyncToNode(ctx, 1)

	metrics3 := mgr.GetMetrics()
	assert.Equal(t, uint64(2), metrics3["dek_accesses"])
	assert.Equal(t, uint64(1), metrics3["rotate_operations"])
	assert.Equal(t, uint64(1), metrics3["sync_operations"])
}

// TestIsValidAlgorithm tests algorithm validation.
func TestIsValidAlgorithm(t *testing.T) {
	tests := []struct {
		algorithm string
		valid     bool
	}{
		{AlgorithmAES256GCM, true},
		{"aes256-gcm", true},
		{"AES256-GCM", true},
		{"invalid", false},
		{"", false},
		{"CHACHA20-POLY1305", false},
	}

	for _, tt := range tests {
		t.Run(tt.algorithm, func(t *testing.T) {
			assert.Equal(t, tt.valid, IsValidAlgorithm(tt.algorithm))
		})
	}
}

// TestConcurrentAccess tests thread-safety of the manager.
func TestConcurrentAccess(t *testing.T) {
	mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
	require.NoError(t, err)
	defer func() { _ = mgr.Close() }()

	ctx := context.Background()
	require.NoError(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))

	const numGoroutines = 50
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = mgr.WithCurrentDEK(func([]byte) error { return nil })
				_ = mgr.GetDEKVersion()
				_, _ = mgr.GetStatus(ctx)
			}
		}()
	}

	wg.Wait()
}

// BenchmarkWithCurrentDEK benchmarks scoped DEK access.
func BenchmarkWithCurrentDEK(b *testing.B) {
	mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
	if err != nil {
		b.Fatalf("failed to create manager: %v", err)
	}
	defer func() { _ = mgr.Close() }()

	if err := mgr.Initialize(context.Background(), AlgorithmAES256GCM, 32); err != nil {
		b.Fatalf("initialization failed: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mgr.WithCurrentDEK(func([]byte) error { return nil })
	}
}

// BenchmarkGetDEKVersion benchmarks version retrieval.
func BenchmarkGetDEKVersion(b *testing.B) {
	mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
	if err != nil {
		b.Fatalf("failed to create manager: %v", err)
	}
	defer func() { _ = mgr.Close() }()

	if err := mgr.Initialize(context.Background(), AlgorithmAES256GCM, 32); err != nil {
		b.Fatalf("initialization failed: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mgr.GetDEKVersion()
	}
}

// BenchmarkConcurrentDEKAccess benchmarks concurrent DEK access.
func BenchmarkConcurrentDEKAccess(b *testing.B) {
	mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
	if err != nil {
		b.Fatalf("failed to create manager: %v", err)
	}
	defer func() { _ = mgr.Close() }()

	if err := mgr.Initialize(context.Background(), AlgorithmAES256GCM, 32); err != nil {
		b.Fatalf("initialization failed: %v", err)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = mgr.WithCurrentDEK(func([]byte) error { return nil })
		}
	})
}
