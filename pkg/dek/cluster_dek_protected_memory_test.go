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
	"errors"
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

// copyCurrentDEK copies the live DEK out of protected memory so a test can
// compare successive values. It exists for tests ONLY: production callers use
// the scoped slice WithCurrentDEK hands them and never take a copy.
func copyCurrentDEK(t *testing.T, mgr *ClusterDEKManager) []byte {
	t.Helper()
	var out []byte
	require.NoError(t, mgr.WithCurrentDEK(func(dek []byte) error {
		out = bytes.Clone(dek)
		return nil
	}))
	return out
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

		// Scoped access sees exactly those protected bytes.
		assert.Equal(t, buf.Bytes(), copyCurrentDEK(t, mgr))
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

		called := false
		err = mgr.WithCurrentDEK(func([]byte) error {
			called = true
			return nil
		})
		require.Error(t, err)
		assert.False(t, called, "no DEK may be handed out after Close")
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

	t.Run("CurrentVersion reads the DEK state under the manager lock", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)

		require.NoError(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))

		// Stand in for a Close that is mid-flight: hold the state lock, flip
		// the manager to closed, then release. A CurrentVersion that consults
		// the initialised flag outside the lock can slip in before the flip
		// and report a version for a DEK that is being destroyed; one that
		// reads under the lock can only observe the state after the flip.
		mgr.mu.Lock()

		started := make(chan struct{})
		done := make(chan struct{})
		var (
			version    uint64
			versionErr error
		)
		go func() {
			close(started)
			version, versionErr = mgr.CurrentVersion()
			close(done)
		}()
		<-started

		mgr.dekPlaintext.Destroy()
		mgr.dekPlaintext = nil
		mgr.initialized.Store(false)
		mgr.mu.Unlock()

		<-done
		require.Error(t, versionErr, "CurrentVersion must not report a version for a destroyed DEK")
		assert.Equal(t, uint64(0), version)
		var niErr *NotInitializedError
		assert.ErrorAs(t, versionErr, &niErr)

		require.NoError(t, mgr.Close())
	})

	t.Run("no version is reported once Close has returned", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)

		require.NoError(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))

		const readers = 8
		var wg sync.WaitGroup
		closed := make(chan struct{})
		failures := make(chan error, readers)

		for i := 0; i < readers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-closed:
						if _, err := mgr.CurrentVersion(); err == nil {
							failures <- errors.New("CurrentVersion succeeded after Close returned")
						}
						return
					default:
						_, _ = mgr.CurrentVersion()
					}
				}
			}()
		}

		require.NoError(t, mgr.Close())
		close(closed)
		wg.Wait()
		close(failures)

		for err := range failures {
			t.Error(err)
		}
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
		assert.Equal(t, before, copyCurrentDEK(t, mgr), "failed rotate must leave the DEK intact")
		assert.Equal(t, uint64(1), mgr.GetDEKVersion())
	})
}
