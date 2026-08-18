// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package dek

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// flakyEncryptBarrier wraps the plaintext like mockEncryptionBarrier but
// starts failing Encrypt after failAfter successful calls. It exercises the
// rotate-failure path, where the manager must keep the DEK it already has.
type flakyEncryptBarrier struct {
	mu        sync.Mutex
	calls     int
	failAfter int
}

func (b *flakyEncryptBarrier) Encrypt(plaintext []byte) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.calls > b.failAfter {
		return nil, &CryptoError{Op: "flakyEncryptBarrier.Encrypt", Detail: "injected failure"}
	}
	out := make([]byte, len(plaintext)+4)
	copy(out, []byte("ENC:"))
	copy(out[4:], plaintext)
	return out, nil
}

func (b *flakyEncryptBarrier) Decrypt(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < 4 {
		return nil, &CiphertextTooShortError{Op: "flakyEncryptBarrier.Decrypt"}
	}
	return ciphertext[4:], nil
}

func (b *flakyEncryptBarrier) IsSealed() bool { return false }

// livePlaintext returns the manager's live DEK buffer under the state lock.
func livePlaintext(mgr *ClusterDEKManager) *secure.Buffer {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return mgr.dekPlaintext
}

// TestClusterDEKProtectedMemory covers the protected-memory lifecycle of the
// live cluster DEK. The DEK is held in an mlocked, guard-paged
// *secure.Buffer from Initialize until Close — never on the ordinary heap —
// and every transition that supersedes or drops it destroys the buffer it
// replaced.
func TestClusterDEKProtectedMemory(t *testing.T) {
	ctx := context.Background()

	t.Run("initialize holds the DEK in a protected buffer", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		require.NoError(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))

		buf := livePlaintext(mgr)
		require.NotNil(t, buf)
		assert.Equal(t, 32, buf.Size())
		assert.NotEqual(t, make([]byte, 32), buf.Bytes(), "DEK must not be all zeroes")

		// The wrapped copy is the ciphertext of exactly these bytes, so the
		// protected buffer is the key the barrier actually sealed.
		mgr.mu.RLock()
		wrapped := bytes.Clone(mgr.dekWrapped)
		mgr.mu.RUnlock()
		require.Len(t, wrapped, 36)
		assert.Equal(t, buf.Bytes(), wrapped[4:])

		// GetCurrentDEK hands back a detached copy of the protected bytes.
		dek, err := mgr.GetCurrentDEK()
		require.NoError(t, err)
		assert.Equal(t, buf.Bytes(), dek)
	})

	t.Run("rotate destroys the superseded buffer", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		require.NoError(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))
		old := livePlaintext(mgr)
		require.NotNil(t, old)
		oldBytes := bytes.Clone(old.Bytes())

		require.NoError(t, mgr.Rotate(ctx))

		current := livePlaintext(mgr)
		require.NotNil(t, current)
		assert.NotSame(t, old, current, "rotate must install a new buffer")
		assert.Nil(t, old.Bytes(), "superseded buffer must be destroyed")
		assert.Equal(t, 0, old.Size())
		assert.NotEqual(t, oldBytes, current.Bytes(), "rotate must generate fresh key material")
		assert.Equal(t, uint64(2), mgr.GetDEKVersion())
	})

	t.Run("close destroys the live buffer", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)

		require.NoError(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))
		buf := livePlaintext(mgr)
		require.NotNil(t, buf)

		require.NoError(t, mgr.Close())

		assert.Nil(t, buf.Bytes(), "live buffer must be destroyed on Close")
		assert.Nil(t, livePlaintext(mgr))
		assert.False(t, mgr.initialized.Load())

		dek, err := mgr.GetCurrentDEK()
		require.Error(t, err)
		assert.Nil(t, dek)
	})

	t.Run("close is idempotent", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)

		require.NoError(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))
		require.NoError(t, mgr.Close())
		require.NoError(t, mgr.Close())
		assert.Nil(t, livePlaintext(mgr))
	})

	t.Run("failed initialize leaves no protected buffer behind", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(&failEncryptBarrier{}, discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		require.Error(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))

		assert.Nil(t, livePlaintext(mgr))
		assert.False(t, mgr.initialized.Load())
	})

	t.Run("failed rotate keeps the current DEK usable", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(&flakyEncryptBarrier{failAfter: 1}, discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		require.NoError(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))
		buf := livePlaintext(mgr)
		require.NotNil(t, buf)
		before := bytes.Clone(buf.Bytes())

		require.Error(t, mgr.Rotate(ctx))

		assert.Same(t, buf, livePlaintext(mgr), "failed rotate must not swap the buffer")
		dek, err := mgr.GetCurrentDEK()
		require.NoError(t, err)
		assert.Equal(t, before, dek, "failed rotate must leave the DEK intact")
		assert.Equal(t, uint64(1), mgr.GetDEKVersion())
	})
}
