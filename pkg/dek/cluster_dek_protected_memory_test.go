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
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

	// The subtest below asserts that UnwrapKey's output does not ALIAS the
	// manager's protected buffer — it is about the cluster DEK not escaping,
	// not a requirement that the output be heap-backed. The tenant key
	// material it returns is heap-resident for reasons documented on
	// UnwrapKey and tracked in #22.
	t.Run("unwrapped output does not alias the live key", func(t *testing.T) {
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
		assert.False(t, aliased, "UnwrapKey must not return a slice over the manager's protected buffer")

		for i := range unwrapped {
			unwrapped[i] ^= 0xFF
		}
		assert.Equal(t, before, copyLiveDEK(t, mgr))
	})
}

// withCurrentDEK holds the DEK buffer's own borrow for the WHOLE callback, not
// just the slice-header read.
//
// That is the entire point of go-qrdb#74. Before it, Bytes() returned a slice
// the caller used after the call returned with no lock held, so a memguard
// failure anywhere in the process (today: a concurrent Destroy of this SAME
// buffer, since go-enclave gives every buffer its own independent lock rather
// than a process-wide one) could unmap those pages while WrapKey was still
// reading them. See TestClusterDEKManager_WithCurrentDEK_HoldsTheBorrowAcrossTheCallback,
// below, for how that is asserted and exactly what changed with the go-enclave
// adoption (go-qrdb#114/#553).
func TestClusterDEKManager_WithCurrentDEK_HoldsTheBorrowAcrossTheCallback(t *testing.T) {
	ctx := context.Background()
	mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
	require.NoError(t, err)
	defer func() { _ = mgr.Close() }()
	require.NoError(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))

	// WrapKey runs its AES-GCM inside withCurrentDEK's borrow.
	wrapped, err := mgr.WrapKey([]byte("tenant-key-material"))
	require.NoError(t, err)
	require.NotEmpty(t, wrapped)

	unwrapped, err := mgr.UnwrapKey(wrapped)
	require.NoError(t, err)
	assert.Equal(t, []byte("tenant-key-material"), unwrapped)

	// And the borrow really is held: a Destroy of the SAME live buffer started
	// while the callback runs must not complete until the callback has
	// returned.
	//
	// THE PROBE CHANGED WITH go-qrdb#114/#553, because the guarantee did.
	// Until the go-enclave adoption this asserted that an UNRELATED protected
	// allocation could not complete during the borrow — true then, because
	// memguard's failure path purged the whole process and a package-wide
	// lock serialized every allocation against every borrow to survive it.
	//
	// go-enclave gives every Buffer and Secret its own independent lock, so
	// that lock is deliberately gone: an allocation of an UNRELATED object is
	// now supposed to proceed while this borrow is held, and asserting the old
	// exclusion would be asserting something false. What survives is the
	// guarantee that actually matters here: only THIS enclave's own Destroy
	// can release its pages, and Destroy takes the write lock, so it waits out
	// every in-flight reader — the shape any sync.RWMutex gives for free.
	//
	// Asserted by ORDER, not by a timeout. The first version of this test
	// waited 150ms and concluded "still blocked, therefore coordinated", which
	// reports a false pass on any machine slow enough that the operation had
	// not finished yet regardless (found in review of go-qrdb#74).
	var order atomic.Int32
	var borrowExit, destroyExit int32
	destroyDone := make(chan struct{})
	started := make(chan struct{})

	live := livePlaintext(mgr)
	require.NotNil(t, live)

	require.NoError(t, mgr.withCurrentDEK("test", func(dek []byte) error {
		require.Len(t, dek, 32)
		go func() {
			defer close(destroyDone)
			close(started)
			// The one operation that can still unmap these exact pages.
			live.Destroy()
			destroyExit = order.Add(1)
		}()
		<-started
		// Give the destroyer every chance to run to completion if nothing is
		// stopping it. Yielding rather than sleeping keeps this a scheduling
		// concession, not a duration the assertion depends on.
		for i := 0; i < 1000; i++ {
			runtime.Gosched()
		}
		borrowExit = order.Add(1)
		return nil
	}))

	<-destroyDone
	assert.Equal(t, int32(1), borrowExit, "the borrow must finish first")
	assert.Equal(t, int32(2), destroyExit, "Destroy must complete only after the borrow released")
}

// A destroyed DEK still reports NotInitializedError, not the borrow's own
// NilBufferError.
//
// The translation matters because callers switch on this error: leaking
// *secure.NilBufferError through a public path would change the contract for
// every consumer that distinguishes "never initialized" from "internal fault".
func TestClusterDEKManager_WithCurrentDEK_TranslatesADestroyedBuffer(t *testing.T) {
	ctx := context.Background()
	mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
	require.NoError(t, err)
	require.NoError(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))

	// Destroy the DEK behind the manager's back, leaving it "initialized" with
	// an unusable buffer — the state Close would otherwise reach.
	livePlaintext(mgr).Destroy()

	err = mgr.withCurrentDEK("test", func([]byte) error {
		t.Error("the callback must not run against a destroyed buffer")
		return nil
	})
	var notInit *NotInitializedError
	require.ErrorAs(t, err, &notInit,
		"a destroyed DEK must report NotInitializedError, not the borrow's NilBufferError")
}

// aliasCapturingBarrier records the slice header it was handed, so a test can
// prove the plaintext the manager encrypted WAS its mlocked buffer rather than
// a heap copy of it.
//
// Retaining the slice past the call breaks the borrow contract, which is why
// only a test may do it: the comparison is of element addresses, made while
// the buffer is still live, and the contents are never read.
type aliasCapturingBarrier struct {
	mu   sync.Mutex
	seen [][]byte
}

func (b *aliasCapturingBarrier) Encrypt(plaintext []byte) ([]byte, error) {
	b.mu.Lock()
	b.seen = append(b.seen, plaintext)
	b.mu.Unlock()

	out := make([]byte, len(plaintext)+4)
	copy(out, []byte("ENC:"))
	copy(out[4:], plaintext)
	return out, nil
}

func (b *aliasCapturingBarrier) Decrypt(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < 4 {
		return nil, &CiphertextTooShortError{Op: "aliasCapturingBarrier.Decrypt"}
	}
	return append([]byte(nil), ciphertext[4:]...), nil
}

func (b *aliasCapturingBarrier) IsSealed() bool { return false }

// assertEncryptedFromProtectedMemory checks that call number idx to the
// barrier was handed the manager's live protected buffer itself.
func assertEncryptedFromProtectedMemory(t *testing.T, barrier *aliasCapturingBarrier, mgr *ClusterDEKManager, idx int, op string) {
	t.Helper()

	barrier.mu.Lock()
	require.Greater(t, len(barrier.seen), idx, "%s did not reach the barrier", op)
	got := barrier.seen[idx]
	barrier.mu.Unlock()

	mgr.mu.RLock()
	require.NotNil(t, mgr.dekPlaintext)
	live := mgr.dekPlaintext.Bytes()
	mgr.mu.RUnlock()

	require.NotEmpty(t, got, "%s handed the barrier an empty plaintext", op)
	require.NotEmpty(t, live, "%s left no live DEK to compare against", op)
	require.Equal(t, len(live), len(got))

	assert.True(t, &got[0] == &live[0],
		"%s handed the barrier a COPY of the cluster DEK. The plaintext must be "+
			"the mlocked buffer itself: a copy lives on the swappable, "+
			"core-dumpable Go heap, where secure.Wipe cannot reach compiler or "+
			"runtime copies (go-qrdb#74, #145)", op)
}

// The cluster DEK reaches the barrier as the mlocked buffer, never as a heap
// copy of it.
//
// This pins behaviour Initialize and Rotate already have; it is here because
// that behaviour was nearly traded away and nothing would have noticed. A
// revision of PR #82 answered a deadlock finding by materialising `plain :=
// make([]byte, len(dek))` and encrypting that — swapping a liveness fault for
// a live cluster KEK on the swappable, core-dumpable Go heap, which is the
// wrong direction for a key-encryption key. Every existing test passed.
//
// Mutation-verified 2/2: reinstating the heap copy in either Initialize or
// Rotate fails this with "handed the barrier a COPY of the cluster DEK",
// naming the site.
func TestClusterDEKManager_EncryptsFromProtectedMemory(t *testing.T) {
	ctx := context.Background()
	barrier := &aliasCapturingBarrier{}
	mgr, err := NewClusterDEKManager(barrier, discardLogger())
	require.NoError(t, err)
	defer func() { _ = mgr.Close() }()

	require.NoError(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))
	assertEncryptedFromProtectedMemory(t, barrier, mgr, 0, "Initialize")

	// Rotate installs a fresh buffer, so its call must alias the NEW one —
	// checked straight after, while it is still the live DEK.
	require.NoError(t, mgr.Rotate(ctx))
	assertEncryptedFromProtectedMemory(t, barrier, mgr, 1, "Rotate")
}

// protectedAllocBarrier is a seal.EncryptionBarrier whose Encrypt allocates
// PROTECTED memory — a purge-class operation.
//
// Nothing in the seal.EncryptionBarrier contract forbids this, so it is a
// legal implementation a caller may inject, and NewClusterDEKManager accepts
// any implementation. That makes it the contract the manager must actually
// hold up against, rather than the concrete StorageBarrier that happens to be
// wired in production (go-qrdb#74).
type protectedAllocBarrier struct{ calls atomic.Int32 }

func (b *protectedAllocBarrier) Encrypt(plaintext []byte) ([]byte, error) {
	b.calls.Add(1)
	// The offending act: protected allocation inside the callee. Under
	// memguard this deadlocked a caller holding a borrow across the call —
	// its process-wide purgeMu would be taken exclusively on a goroutine
	// already holding it shared. go-enclave gives every enclave its own
	// independent lock, so this specific allocation can no longer deadlock
	// against an unrelated borrow the caller might hold; what this call
	// still tests is that NewClusterDEKManager tolerates an injected
	// implementation doing this at all, per the interface-boundary
	// discussion below.
	scratch, err := secure.NewBuffer(16)
	if err != nil {
		return nil, err
	}
	defer scratch.Destroy()

	out := make([]byte, len(plaintext)+4)
	copy(out, []byte("ENC:"))
	copy(out[4:], plaintext)
	return out, nil
}

func (b *protectedAllocBarrier) Decrypt(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < 4 {
		return nil, &CiphertextTooShortError{Op: "protectedAllocBarrier.Decrypt"}
	}
	return append([]byte(nil), ciphertext[4:]...), nil
}

func (b *protectedAllocBarrier) IsSealed() bool { return false }

// A barrier that allocates protected memory must not deadlock Initialize or
// Rotate.
//
// The other half of the vice, and the reason go-qrdb#145 exists. A revision of
// PR #82 wrapped m.barrier.Encrypt in a secure.Buffer borrow to close the
// purge race. Traced against the concrete StorageBarrier that was safe; traced
// against the INTERFACE the constructor actually accepts it was not, and any
// conforming implementation like the one above hangs the daemon at boot.
//
// Between them these two tests fence both escapes: neither a heap copy nor a
// borrow held across the barrier can be reintroduced without turning something
// red. What remains unfenced — the purge race on the unborrowed read — cannot
// be closed from this side of the interface, which is what #145 asks the owner
// to rule on.
//
// The work runs on a goroutine with a bounded wait because the failure mode is
// a permanent hang, not an error: without the bound this test would take the
// whole package down with it and report nothing useful.
func TestClusterDEKManager_ToleratesABarrierThatAllocatesProtectedMemory(t *testing.T) {
	barrier := &protectedAllocBarrier{}
	mgr, err := NewClusterDEKManager(barrier, discardLogger())
	require.NoError(t, err)

	// A step that deadlocks leaves a goroutine holding the manager's state
	// lock forever, so Close would block on it too — and the whole package
	// would then die on ITS timeout with no named cause, throwing away the
	// diagnostic below. t.Fatalf ends only its own goroutine; it cannot
	// release a lock some other goroutine is wedged on. So the cleanup is
	// skipped once a step has hung: there is nothing left to release, and the
	// point of the bounded wait is that the failure arrives WITH its reason.
	//
	// Measured: with the borrow reinstated across m.barrier.Encrypt, the
	// unconditional Close turned this into "panic: test timed out after 1m0s"
	// and the "did not return" line never appeared.
	var hung atomic.Bool
	defer func() {
		if hung.Load() {
			return
		}
		_ = mgr.Close()
	}()

	for _, step := range []struct {
		name string
		run  func() error
	}{
		{"Initialize", func() error { return mgr.Initialize(context.Background(), AlgorithmAES256GCM, 32) }},
		{"Rotate", func() error { return mgr.Rotate(context.Background()) }},
	} {
		t.Run(step.name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() { done <- step.run() }()

			select {
			case runErr := <-done:
				require.NoError(t, runErr)
			case <-time.After(20 * time.Second):
				hung.Store(true)
				t.Fatalf("%s did not return: the barrier's protected-memory "+
					"allocation deadlocked against a borrow held across it", step.name)
			}
		})
	}

	if hung.Load() {
		return
	}

	if got := barrier.calls.Load(); got < 2 {
		t.Errorf("barrier.Encrypt called %d times, want at least 2 "+
			"(Initialize and Rotate) — the test did not exercise the path", got)
	}
}
