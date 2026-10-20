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

// StorageBarrier's own guarded writes.
//
// atomic_verbs_composition_test.go covers what the BACKENDS promise. These
// cover what the BARRIER does on top of them, which is a different and
// sharper contract: the caller's expectation is a PLAINTEXT, the bytes handed
// to the backend as a guard are the CIPHERTEXT that was just read, and the two
// comparisons happen at different layers on purpose.
//
// Getting that wrong does not fail loudly. Encrypting `expected` afresh and
// guarding on that would refuse EVERY guarded write, because the AEAD is
// nonce-randomised and the same plaintext encrypts to different bytes each
// time -- a caller would see its compare-and-swap decline forever with no
// error to explain it.

package seal

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// nonGuardedBackend is a StorageBackend with no guarded SWAP of its own, so
// the barrier's SWAP capability probe takes its unsupported branch. It is
// NOT a backend with no guarded verbs at all: embedding StorageBackend means
// it inherits CompareAndDelete by promotion, since StorageBackend declares
// that verb unconditionally (guardedDeleteBackend in barrier.go). Only the
// swap probe can ever be defeated through this type; the delete probe cannot
// (storageBackendImpliesGuardedDelete below).
type nonGuardedBackend struct{ StorageBackend }

// failingGetBackend returns a non-ErrSecretNotFound error from Get, which the
// barrier must propagate rather than translate into "absent".
type failingGetBackend struct {
	StorageBackend
	err error
}

func (b *failingGetBackend) Get(context.Context, string) ([]byte, error) { return nil, b.err }

// newGuardedTestBarrier returns an INITIALIZED (therefore unsealed)
// StorageBarrier over base.
func newGuardedTestBarrier(t *testing.T, base StorageBackend) *StorageBarrier {
	t.Helper()
	b, err := NewStorageBarrier(testLogger(), base,
		BarrierConfig{RootKeyPath: "sys/root"}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := b.Initialize(context.Background(), MustCredentials(context.Background(), []byte("test-password"))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return b
}

// TestStorageBarrier_CompareAndSwap_GuardsOnCiphertextComparesOnPlaintext is
// the property the whole design rests on.
//
// A successful swap proves both halves at once: the caller's PLAINTEXT
// expectation matched, and the ciphertext guard handed down matched what the
// backend actually held. An implementation that encrypted `expected` to build
// the guard would fail here every time.
func TestStorageBarrier_CompareAndSwap_GuardsOnCiphertextComparesOnPlaintext(t *testing.T) {
	ctx := context.Background()
	b := newGuardedTestBarrier(t, NewMemoryBackend())

	if err := b.Put(ctx, "k", []byte("old")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	swapped, err := b.CompareAndSwap(ctx, "k", []byte("old"), []byte("new"))
	if err != nil {
		t.Fatalf("CompareAndSwap: %v", err)
	}
	if !swapped {
		t.Fatal("CompareAndSwap declined a matching plaintext: the guard is " +
			"probably built by re-encrypting `expected`, which can never match " +
			"a nonce-randomised AEAD")
	}
	got, err := b.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "new" {
		t.Errorf("value = %q, want %q", got, "new")
	}
}

// TestStorageBarrier_CompareAndSwap_IsNotDeterministicallyEncrypted is the
// same claim from the other side: two encryptions of one plaintext differ, so
// the ciphertext genuinely cannot be reconstructed for comparison.
//
// Without this, the test above could pass against an implementation that
// happened to be deterministic, and the design note would be unverified.
func TestStorageBarrier_CompareAndSwap_IsNotDeterministicallyEncrypted(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryBackend()
	b := newGuardedTestBarrier(t, base)

	if err := b.Put(ctx, "a", []byte("same")); err != nil {
		t.Fatalf("Put a: %v", err)
	}
	if err := b.Put(ctx, "b", []byte("same")); err != nil {
		t.Fatalf("Put b: %v", err)
	}
	ca, err := base.Get(ctx, "a")
	if err != nil {
		t.Fatalf("base Get a: %v", err)
	}
	cb, err := base.Get(ctx, "b")
	if err != nil {
		t.Fatalf("base Get b: %v", err)
	}
	if string(ca) == string(cb) {
		t.Fatal("one plaintext encrypted to identical ciphertext twice: the " +
			"barrier is deterministic, and the guarded-write design note is wrong")
	}
}

// TestStorageBarrier_GuardedWrites_DeclineOnAStalePlaintext: the comparison is
// against the DECRYPTED value, so a caller holding a stale plaintext is
// refused and nothing changes.
func TestStorageBarrier_GuardedWrites_DeclineOnAStalePlaintext(t *testing.T) {
	ctx := context.Background()

	t.Run("CompareAndSwap", func(t *testing.T) {
		b := newGuardedTestBarrier(t, NewMemoryBackend())
		if err := b.Put(ctx, "k", []byte("actual")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		swapped, err := b.CompareAndSwap(ctx, "k", []byte("stale"), []byte("new"))
		if err != nil || swapped {
			t.Fatalf("CompareAndSwap = (%v, %v), want (false, nil)", swapped, err)
		}
		got, _ := b.Get(ctx, "k")
		if string(got) != "actual" {
			t.Errorf("a declined swap wrote anyway: %q", got)
		}
	})

	t.Run("CompareAndDelete", func(t *testing.T) {
		b := newGuardedTestBarrier(t, NewMemoryBackend())
		if err := b.Put(ctx, "k", []byte("actual")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		deleted, err := b.CompareAndDelete(ctx, "k", []byte("stale"))
		if err != nil || deleted {
			t.Fatalf("CompareAndDelete = (%v, %v), want (false, nil)", deleted, err)
		}
		got, _ := b.Get(ctx, "k")
		if string(got) != "actual" {
			t.Errorf("a declined delete changed the value: %q", got)
		}
	})
}

// TestStorageBarrier_CompareAndDelete_RemovesOnAMatch covers the success arm.
func TestStorageBarrier_CompareAndDelete_RemovesOnAMatch(t *testing.T) {
	ctx := context.Background()
	b := newGuardedTestBarrier(t, NewMemoryBackend())

	if err := b.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	deleted, err := b.CompareAndDelete(ctx, "k", []byte("v"))
	if err != nil {
		t.Fatalf("CompareAndDelete: %v", err)
	}
	if !deleted {
		t.Fatal("CompareAndDelete declined a matching plaintext")
	}
	if _, err := b.Get(ctx, "k"); err == nil {
		t.Error("the key survived a successful guarded delete")
	}
}

// TestStorageBarrier_GuardedWrites_AbsentIsUnchangedNotAnError: a
// StorageBackend signals a missing key with ErrSecretNotFound, and both verbs
// must report that as an ordinary "nothing to do" rather than a failure --
// otherwise every caller has to special-case an error that is not one
// (go-qrdb#56). A swap also does not CREATE.
func TestStorageBarrier_GuardedWrites_AbsentIsUnchangedNotAnError(t *testing.T) {
	ctx := context.Background()

	t.Run("CompareAndSwap does not create", func(t *testing.T) {
		b := newGuardedTestBarrier(t, NewMemoryBackend())
		swapped, err := b.CompareAndSwap(ctx, "missing", []byte("v"), []byte("next"))
		if err != nil {
			t.Fatalf("CompareAndSwap on an absent key: %v", err)
		}
		if swapped {
			t.Fatal("CompareAndSwap reported a swap on an absent key")
		}
		if _, gerr := b.Get(ctx, "missing"); gerr == nil {
			t.Error("CompareAndSwap created an absent key")
		}
	})

	t.Run("CompareAndDelete on an absent key", func(t *testing.T) {
		b := newGuardedTestBarrier(t, NewMemoryBackend())
		deleted, err := b.CompareAndDelete(ctx, "missing", []byte("v"))
		if err != nil {
			t.Fatalf("CompareAndDelete on an absent key: %v", err)
		}
		if deleted {
			t.Error("CompareAndDelete reported a delete of an absent key")
		}
	})
}

// TestStorageBarrier_GuardedWrites_RefuseWhileSealed: both verbs check the
// barrier state FIRST, before touching storage. A sealed barrier cannot
// decrypt, so it cannot evaluate the plaintext comparison at all -- answering
// "false" would be indistinguishable from "your expected value was stale".
func TestStorageBarrier_GuardedWrites_RefuseWhileSealed(t *testing.T) {
	ctx := context.Background()
	b := newGuardedTestBarrier(t, NewMemoryBackend())
	if err := b.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	if swapped, err := b.CompareAndSwap(ctx, "k", []byte("v"), []byte("next")); swapped || !errors.Is(err, ErrSealed) {
		t.Errorf("CompareAndSwap while sealed = (%v, %v), want (false, ErrSealed)", swapped, err)
	}
	if deleted, err := b.CompareAndDelete(ctx, "k", []byte("v")); deleted || !errors.Is(err, ErrSealed) {
		t.Errorf("CompareAndDelete while sealed = (%v, %v), want (false, ErrSealed)", deleted, err)
	}
}

// storageBackendImpliesGuardedDelete is a COMPILE-TIME statement of why
// ErrGuardedDeleteUnsupported is unreachable from StorageBarrier.
//
// StorageBarrier.base is typed StorageBackend, and StorageBackend itself
// declares CompareAndDelete -- so the `b.base.(guardedDeleteBackend)` probe in
// CompareAndDelete can never fail, and the ErrGuardedDeleteUnsupported arm
// below it is dead. CompareAndSwap is genuinely optional by comparison:
// StorageBackend does NOT declare it, so that probe is a real discriminator.
//
// If StorageBackend ever drops CompareAndDelete, this assertion stops
// compiling -- which is the signal that the sentinel has become reachable and
// wants a test rather than this comment.
var storageBackendImpliesGuardedDelete = func(b StorageBackend) guardedDeleteBackend { return b }

// TestStorageBarrier_CompareAndSwap_RefusesOnABackendWithoutIt: the barrier
// cannot synthesise atomicity over a backend that lacks the verb. Falling back
// to read-then-write would still return true while silently ceasing to be a
// guarded write, so the only honest answer is the specific sentinel.
//
// Only the SWAP half is exercised. The delete half cannot be reached -- see
// storageBackendImpliesGuardedDelete above -- and a test that pretended to
// cover it would have to construct something the type system forbids.
func TestStorageBarrier_CompareAndSwap_RefusesOnABackendWithoutIt(t *testing.T) {
	ctx := context.Background()
	mem := NewMemoryBackend()
	b := newGuardedTestBarrier(t, mem)
	if err := b.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// A base that forwards everything StorageBackend requires but offers no
	// CompareAndSwap of its own.
	b.base = &nonGuardedBackend{StorageBackend: mem}
	_ = storageBackendImpliesGuardedDelete // the delete probe cannot be defeated

	swapped, err := b.CompareAndSwap(ctx, "k", []byte("v"), []byte("next"))
	if swapped || !errors.Is(err, ErrGuardedSwapUnsupported) {
		t.Errorf("CompareAndSwap = (%v, %v), want (false, ErrGuardedSwapUnsupported)", swapped, err)
	}

	// And the value is untouched by the refusal.
	got, gerr := b.Get(ctx, "k")
	if gerr != nil || string(got) != "v" {
		t.Errorf("stored = (%q, %v), want %q unchanged", got, gerr, "v")
	}
}

// TestStorageBarrier_GuardedWrites_PropagateARealGetFailure: only
// ErrSecretNotFound means "absent". Any other read failure is a real failure
// and must reach the caller -- reporting it as (false, nil) would tell a
// caller its guarded write was cleanly declined when the store is in fact
// broken, and a retry loop would spin forever on a healthy-looking answer.
func TestStorageBarrier_GuardedWrites_PropagateARealGetFailure(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("backend unavailable")
	b := newGuardedTestBarrier(t, NewMemoryBackend())
	b.base = &failingGetBackend{StorageBackend: NewMemoryBackend(), err: boom}

	if swapped, err := b.CompareAndSwap(ctx, "k", []byte("v"), []byte("next")); swapped || !errors.Is(err, boom) {
		t.Errorf("CompareAndSwap = (%v, %v), want (false, the backend's error)", swapped, err)
	}
	if deleted, err := b.CompareAndDelete(ctx, "k", []byte("v")); deleted || !errors.Is(err, boom) {
		t.Errorf("CompareAndDelete = (%v, %v), want (false, the backend's error)", deleted, err)
	}
}

// TestStorageBarrier_GuardedWrites_ReportUndecryptableStoredBytes: a value
// that will not decrypt is a GetError, not a mismatch. Reporting it as a
// mismatch would tell a caller its expectation was stale when the stored
// ciphertext is actually corrupt or encrypted under a different epoch -- and
// a caller that then wrote a "corrected" value would destroy whatever was
// really there.
func TestStorageBarrier_GuardedWrites_ReportUndecryptableStoredBytes(t *testing.T) {
	ctx := context.Background()
	mem := NewMemoryBackend()
	b := newGuardedTestBarrier(t, mem)

	// Bytes that are not a valid envelope, written under the barrier.
	if err := mem.Put(ctx, "k", []byte("not ciphertext")); err != nil {
		t.Fatalf("base Put: %v", err)
	}

	swapped, err := b.CompareAndSwap(ctx, "k", []byte("v"), []byte("next"))
	if swapped {
		t.Error("CompareAndSwap swapped over an undecryptable value")
	}
	var swapGetErr *GetError
	if !errors.As(err, &swapGetErr) {
		t.Errorf("CompareAndSwap error = %T (%v), want *GetError", err, err)
	}

	deleted, err := b.CompareAndDelete(ctx, "k", []byte("v"))
	if deleted {
		t.Error("CompareAndDelete removed an undecryptable value")
	}
	var delGetErr *GetError
	if !errors.As(err, &delGetErr) {
		t.Errorf("CompareAndDelete error = %T (%v), want *GetError", err, err)
	}
}

// rewritingBackend re-stores guardKey's plaintext through the barrier right
// after the barrier first reads it, so the ciphertext the barrier compared is
// replaced before its batch reaches the backend. The plaintext is unchanged;
// only the bytes are new.
type rewritingBackend struct {
	*MemoryBackend
	guardKey string
	rewrite  func()
	done     bool
}

func (r *rewritingBackend) Get(ctx context.Context, key string) ([]byte, error) {
	v, err := r.MemoryBackend.Get(ctx, key)
	if key == r.guardKey && !r.done {
		r.done = true
		r.rewrite()
	}
	return v, err
}

// TestStorageBarrier_PutAllIfAllUnchanged_GuardsOnCiphertextComparesOnPlaintext
// is the batch counterpart of the CompareAndSwap property: a plaintext guard
// that matches is applied, and the writes land encrypted.
func TestStorageBarrier_PutAllIfAllUnchanged_GuardsOnCiphertextComparesOnPlaintext(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryBackend()
	b := newGuardedTestBarrier(t, base)
	if err := b.Put(ctx, "held", []byte("old")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := b.Put(ctx, "gone", []byte("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	applied, err := b.PutAllIfAllUnchanged(ctx,
		[]kvstore.Guard{{Key: "held", Value: []byte("old")}, {Key: "absent"}},
		[]kvstore.KV{{Key: "held", Value: []byte("new")}, {Key: "secret", Value: []byte("plaintext")}, {Key: "gone", Delete: true}})
	if err != nil || !applied {
		t.Fatalf("PutAllIfAllUnchanged = (%v, %v), want (true, nil)", applied, err)
	}
	if got, _ := b.Get(ctx, "held"); string(got) != "new" {
		t.Errorf("held = %q, want %q", got, "new")
	}
	if got, _ := b.Get(ctx, "secret"); string(got) != "plaintext" {
		t.Errorf("secret = %q, want %q", got, "plaintext")
	}
	if raw, _ := base.Get(ctx, "secret"); bytes.Contains(raw, []byte("plaintext")) {
		t.Error("the backend holds the plaintext: the batch wrote it unencrypted")
	}
	if _, err := base.Get(ctx, "gone"); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("gone: err = %v, want deleted", err)
	}
}

func TestStorageBarrier_PutAllIfAllUnchanged_DeclinesAndWritesNothing(t *testing.T) {
	ctx := context.Background()
	cases := map[string]kvstore.Guard{
		"stale plaintext":            {Key: "held", Value: []byte("other")},
		"absent guard on a present":  {Key: "held"},
		"present guard on an absent": {Key: "absent", Value: []byte("old")},
	}
	for name, guard := range cases {
		t.Run(name, func(t *testing.T) {
			b := newGuardedTestBarrier(t, NewMemoryBackend())
			if err := b.Put(ctx, "held", []byte("old")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			applied, err := b.PutAllIfAllUnchanged(ctx, []kvstore.Guard{guard},
				[]kvstore.KV{{Key: "new", Value: []byte("v")}})
			if err != nil || applied {
				t.Fatalf("PutAllIfAllUnchanged = (%v, %v), want (false, nil)", applied, err)
			}
			if _, err := b.Get(ctx, "new"); !errors.Is(err, ErrSecretNotFound) {
				t.Errorf("new: err = %v, want unwritten", err)
			}
		})
	}
}

// TestStorageBarrier_PutAllIfAllUnchanged_RefusesWhenTheCiphertextMoved proves
// the guard handed down is the ciphertext that was read: a rewrite of the same
// plaintext in between makes the backend refuse.
func TestStorageBarrier_PutAllIfAllUnchanged_RefusesWhenTheCiphertextMoved(t *testing.T) {
	ctx := context.Background()
	base := &rewritingBackend{MemoryBackend: NewMemoryBackend(), guardKey: "held"}
	b := newGuardedTestBarrier(t, base)
	if err := b.Put(ctx, "held", []byte("old")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	base.rewrite = func() {
		if err := b.Put(ctx, "held", []byte("old")); err != nil {
			t.Errorf("rewrite: %v", err)
		}
	}

	applied, err := b.PutAllIfAllUnchanged(ctx,
		[]kvstore.Guard{{Key: "held", Value: []byte("old")}},
		[]kvstore.KV{{Key: "new", Value: []byte("v")}})
	if err != nil || applied {
		t.Fatalf("PutAllIfAllUnchanged = (%v, %v), want (false, nil): the "+
			"guard was not the ciphertext that was read", applied, err)
	}
}

func TestStorageBarrier_PutAllIfAllUnchanged_RefusesWhileSealed(t *testing.T) {
	b := newGuardedTestBarrier(t, NewMemoryBackend())
	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := b.PutAllIfAllUnchanged(context.Background(), nil, nil); !errors.Is(err, ErrSealed) {
		t.Fatalf("err = %v, want ErrSealed", err)
	}
}

func TestStorageBarrier_PutAllIfAllUnchanged_RefusesOnABackendWithoutIt(t *testing.T) {
	b := newGuardedTestBarrier(t, nonGuardedBackend{NewMemoryBackend()})
	_, err := b.PutAllIfAllUnchanged(context.Background(), []kvstore.Guard{{Key: "g"}}, nil)
	if !errors.Is(err, ErrGuardedBatchUnsupported) || !errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
		t.Fatalf("err = %v, want ErrGuardedBatchUnsupported matching kvstore.ErrAtomicWriteUnsupported", err)
	}
}

// failingGetBatchBackend is a MemoryBackend, guarded batch included, whose
// reads fail once err is set.
type failingGetBatchBackend struct {
	*MemoryBackend
	err error
}

func (f *failingGetBatchBackend) Get(ctx context.Context, key string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.MemoryBackend.Get(ctx, key)
}

func TestStorageBarrier_PutAllIfAllUnchanged_ReportsReadAndDecryptFailures(t *testing.T) {
	ctx := context.Background()
	readErr := errors.New("disk on fire")
	failing := &failingGetBatchBackend{MemoryBackend: NewMemoryBackend()}
	b := newGuardedTestBarrier(t, failing)
	failing.err = readErr
	if _, err := b.PutAllIfAllUnchanged(ctx, []kvstore.Guard{{Key: "g"}}, nil); !errors.Is(err, readErr) {
		t.Errorf("read failure: err = %v, want %v", err, readErr)
	}

	base := NewMemoryBackend()
	b = newGuardedTestBarrier(t, base)
	if err := base.Put(ctx, "garbage", []byte("not a ciphertext")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	_, err := b.PutAllIfAllUnchanged(ctx, []kvstore.Guard{{Key: "garbage", Value: []byte("x")}}, nil)
	var getErr *GetError
	if !errors.As(err, &getErr) {
		t.Errorf("undecryptable guard: err = %v, want *GetError", err)
	}
}

func TestMemoryBackend_PutAllIfAllUnchanged(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryBackend()
	if err := m.Put(ctx, "held", []byte("1")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	for name, guard := range map[string]kvstore.Guard{
		"stale value":                {Key: "held", Value: []byte("2")},
		"absent guard on a present":  {Key: "held"},
		"present guard on an absent": {Key: "absent", Value: []byte{}},
	} {
		applied, err := m.PutAllIfAllUnchanged(ctx, []kvstore.Guard{guard}, []kvstore.KV{{Key: "new", Value: []byte("v")}})
		if err != nil || applied {
			t.Errorf("%s: PutAllIfAllUnchanged = (%v, %v), want (false, nil)", name, applied, err)
		}
	}
	if ok, _ := m.Exists(ctx, "new"); ok {
		t.Fatal("a declined batch wrote")
	}

	applied, err := m.PutAllIfAllUnchanged(ctx,
		[]kvstore.Guard{{Key: "held", Value: []byte("1")}, {Key: "absent"}},
		[]kvstore.KV{{Key: "new", Value: []byte("v")}, {Key: "held", Delete: true}})
	if err != nil || !applied {
		t.Fatalf("PutAllIfAllUnchanged = (%v, %v), want (true, nil)", applied, err)
	}
	if got, _ := m.Get(ctx, "new"); string(got) != "v" {
		t.Errorf("new = %q, want %q", got, "v")
	}
	if ok, _ := m.Exists(ctx, "held"); ok {
		t.Error("held survived a batched delete")
	}
}
