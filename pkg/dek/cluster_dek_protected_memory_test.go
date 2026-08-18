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
	"reflect"
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

// copyLiveDEK copies the live DEK out of protected memory so a test can
// compare successive values. It reaches into the manager's unexported state
// deliberately: no exported API hands out the key, which is the invariant
// TestClusterDEKEncapsulation pins down.
func copyLiveDEK(t *testing.T, mgr *ClusterDEKManager) []byte {
	t.Helper()
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	require.NotNil(t, mgr.dekPlaintext)
	return bytes.Clone(mgr.dekPlaintext.Bytes())
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

		// WrapKey applies exactly those protected bytes: its output decrypts
		// under the key held in the buffer and under no other.
		wrappedKey, err := mgr.WrapKey([]byte("a tenant data encryption key"))
		require.NoError(t, err)
		unwrapped, err := AESGCMDecrypt(buf.Bytes(), wrappedKey)
		require.NoError(t, err)
		assert.Equal(t, []byte("a tenant data encryption key"), unwrapped)
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

		_, err = mgr.WrapKey([]byte("tenant DEK"))
		require.Error(t, err, "no DEK may be applied after Close")
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

	t.Run("rotate loses the race with a concurrent close", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)

		require.NoError(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))

		// Stand in for a Close that is mid-flight, as the CurrentVersion case
		// above does: hold the state lock, tear the DEK down, then release.
		// A Rotate that only consults the initialised flag before taking the
		// lock installs a fresh protected buffer into a closed manager — a
		// live mlocked allocation no later Close would ever destroy, and one
		// no checked accessor would report.
		mgr.mu.Lock()

		started := make(chan struct{})
		done := make(chan struct{})
		var rotateErr error
		go func() {
			close(started)
			rotateErr = mgr.Rotate(ctx)
			close(done)
		}()
		<-started

		mgr.dekPlaintext.Destroy()
		mgr.dekPlaintext = nil
		mgr.initialized.Store(false)
		mgr.mu.Unlock()

		<-done
		require.Error(t, rotateErr, "rotate must not proceed against a closed manager")
		var niErr *NotInitializedError
		assert.ErrorAs(t, rotateErr, &niErr)
		assert.Nil(t, livePlaintext(mgr), "a losing rotate must leave no protected buffer behind")
		assert.False(t, mgr.initialized.Load())

		require.NoError(t, mgr.Close())
	})

	t.Run("no rotation is accepted once Close has returned", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)

		require.NoError(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))
		require.NoError(t, mgr.Close())

		require.Error(t, mgr.Rotate(ctx))
		assert.Nil(t, livePlaintext(mgr))
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
		assert.Equal(t, before, copyLiveDEK(t, mgr), "failed rotate must leave the DEK intact")
		assert.Equal(t, uint64(1), mgr.GetDEKVersion())
	})
}

// TestClusterDEKEncapsulation pins down the manager's key-integrity boundary:
// no exported method lends out the live key bytes, so an external caller can
// neither mutate the active key nor retain it beyond a call. Callers reach the
// DEK only through WrapKey/UnwrapKey, which apply it inside the manager.
func TestClusterDEKEncapsulation(t *testing.T) {
	t.Run("no exported method lends the key to a callback", func(t *testing.T) {
		mgrType := reflect.TypeOf(&ClusterDEKManager{})
		for i := 0; i < mgrType.NumMethod(); i++ {
			method := mgrType.Method(i)
			for arg := 0; arg < method.Type.NumIn(); arg++ {
				assert.NotEqual(t, reflect.Func, method.Type.In(arg).Kind(),
					"exported method %s takes a callback: a callback is how live key "+
						"bytes escape the manager, so the DEK stays behind WrapKey/UnwrapKey",
					method.Name)
			}
		}
	})

	t.Run("wrapping does not expose or retain the live key", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		require.NoError(t, mgr.Initialize(context.Background(), AlgorithmAES256GCM, 32))
		before := copyLiveDEK(t, mgr)

		plaintext := []byte("a tenant data encryption key")
		wrapped, err := mgr.WrapKey(plaintext)
		require.NoError(t, err)

		// Nothing the caller holds reveals the DEK.
		assert.False(t, bytes.Contains(wrapped, before), "the wrapped output must not carry the DEK")

		// A caller mutating everything it was given and everything it passed
		// in cannot reach the manager's key material.
		for i := range wrapped {
			wrapped[i] ^= 0xFF
		}
		for i := range plaintext {
			plaintext[i] ^= 0xFF
		}

		assert.Equal(t, before, copyLiveDEK(t, mgr), "the live DEK must be unreachable from caller-held memory")
		assert.Equal(t, uint64(1), mgr.GetDEKVersion())

		// The key still works: the manager's own round trip is unaffected.
		fresh, err := mgr.WrapKey([]byte("another tenant DEK"))
		require.NoError(t, err)
		unwrapped, err := mgr.UnwrapKey(fresh)
		require.NoError(t, err)
		assert.Equal(t, []byte("another tenant DEK"), unwrapped)
	})

	t.Run("unwrapped output is a copy, not the live key", func(t *testing.T) {
		mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
		require.NoError(t, err)
		defer func() { _ = mgr.Close() }()

		require.NoError(t, mgr.Initialize(context.Background(), AlgorithmAES256GCM, 32))
		before := copyLiveDEK(t, mgr)

		wrapped, err := mgr.WrapKey(bytes.Repeat([]byte{0xA5}, 32))
		require.NoError(t, err)
		unwrapped, err := mgr.UnwrapKey(wrapped)
		require.NoError(t, err)

		mgr.mu.RLock()
		live := mgr.dekPlaintext.Bytes()
		aliased := &unwrapped[0] == &live[0]
		mgr.mu.RUnlock()
		assert.False(t, aliased, "UnwrapKey must not return a slice over protected memory")

		for i := range unwrapped {
			unwrapped[i] ^= 0xFF
		}
		assert.Equal(t, before, copyLiveDEK(t, mgr))
	})
}
