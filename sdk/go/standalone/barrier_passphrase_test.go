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

package standalone

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/secure"
)

// barrierPassphrase builds the *secure.Buffer BarrierConfig.Passphrase takes.
//
// UNCONSTRAINED ON PURPOSE. Its callers are spread across test files with no
// build constraint of their own, and a helper's constraint must be a superset
// of every caller's (AGENTS.md, "Build constraints on test files"). An
// unconstrained file is the widest superset available and needs no revisiting
// when a caller in a tagged file appears.
//
// The buffer is registered for cleanup rather than destroyed at the end of the
// call, because a single passphrase is typically shared by two NewServer calls
// in a restart test and both must be able to borrow it.
func barrierPassphrase(t *testing.T, pp string) *secure.Buffer {
	t.Helper()
	buf, err := secure.NewBufferFromBytes([]byte(pp))
	if err != nil {
		t.Fatalf("secure.NewBufferFromBytes: %v", err)
	}
	t.Cleanup(buf.Destroy)
	return buf
}

// ---------------------------------------------------------------------------
// BarrierConfig.Passphrase: nil, destroyed, zero value, live (go-qrdb#429)
//
// FOUR SHAPES, BOTH DIRECTIONS. Converting this field from []byte to
// *secure.Buffer gave it a LIFECYCLE the old type did not have, and the first
// version of that conversion read every unreadable shape as "not configured" —
// which on a fresh data dir meant an embedder who asked for a passphrase
// barrier silently got seal.StrategyNone and plaintext at rest. Nothing in
// this package covered any of the four, which is why that was invisible.
//
// The tests below pin the distinction itself, not just the error type:
//
//   nil          not configured  -> comes up, none barrier      (allowed)
//   destroyed    configured, unusable -> *BarrierPassphraseError (refused)
//   zero value   configured, unusable -> *BarrierPassphraseError (refused)
//   live         configured, usable   -> comes up, unsealed      (allowed)
// ---------------------------------------------------------------------------

// TestBarrierPassphrase_NilComesUpWithNoBarrier pins the ALLOWED half. A nil
// Passphrase is the long-standing meaning of leaving the field unset, it must
// keep working, and the barrier it produces must be the one the field doc now
// promises: none, unsealed, no encryption.
//
// Without this test the fix for the destroyed case could be "refuse anything
// that is not a live buffer", which would break every embedder that never set
// the field.
func TestBarrierPassphrase_NilComesUpWithNoBarrier(t *testing.T) {
	ctx := context.Background()

	srv, err := NewServer(ctx, &ServerConfig{
		Backend: database.BackendMemory,
		DataDir: t.TempDir(),
		Barrier: &BarrierConfig{
			Strategy:   seal.StrategyPassphrase,
			AutoUnseal: true,
			Passphrase: nil,
		},
	})
	if err != nil {
		t.Fatalf("NewServer with a nil passphrase must succeed: %v", err)
	}
	defer func() {
		if shutdownErr := srv.Shutdown(ctx); shutdownErr != nil {
			t.Fatalf("Shutdown: %v", shutdownErr)
		}
	}()

	status, err := srv.Client().Barrier().BarrierStatus(ctx)
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if status.Sealed {
		t.Fatal("a none barrier must come up unsealed")
	}
	if status.Strategy != string(seal.StrategyNone) {
		t.Errorf("Strategy = %q, want %q — a nil passphrase must yield the NONE barrier "+
			"the field doc promises, not a passphrase barrier sealed by something unnamed",
			status.Strategy, seal.StrategyNone)
	}
}

// TestBarrierPassphrase_DestroyedIsRefused is the regression test for the
// finding. A DESTROYED buffer is a configured credential that has become
// unusable; it is not an absent one, and it must not be allowed to downgrade
// the barrier.
//
// The data dir is FRESH on purpose. That is the dangerous case: on an existing
// dir the barrier is already initialized under a passphrase and a none barrier
// would fail to unseal it loudly, but on a fresh dir the none branch
// INITIALIZES SUCCESSFULLY, IsSealed() is false, encfs wraps the data plane
// with a root key sealed by nothing, and the server comes up looking healthy.
func TestBarrierPassphrase_DestroyedIsRefused(t *testing.T) {
	ctx := context.Background()

	buf, err := secure.NewBufferFromBytes([]byte("destroyed-before-use"))
	if err != nil {
		t.Fatalf("secure.NewBufferFromBytes: %v", err)
	}
	buf.Destroy()

	srv, err := NewServer(ctx, &ServerConfig{
		Backend: database.BackendPebble,
		DataDir: t.TempDir(),
		Barrier: &BarrierConfig{
			Strategy:   seal.StrategyPassphrase,
			AutoUnseal: true,
			Passphrase: buf,
		},
	})
	if err == nil {
		_ = srv.Shutdown(ctx)
		t.Fatal("NewServer came up with a DESTROYED passphrase. On a fresh data dir " +
			"that is a none barrier and plaintext at rest, for an embedder that " +
			"asked for a passphrase barrier.")
	}
	var ppErr *BarrierPassphraseError
	if !errors.As(err, &ppErr) {
		t.Fatalf("err = %T (%v), want *BarrierPassphraseError", err, err)
	}
	if !errors.As(err, new(*secure.NilBufferError)) {
		t.Errorf("the borrow failure must be reachable through Unwrap; got %v", err)
	}
	if srv != nil {
		t.Error("NewServer returned a Server alongside its error")
	}
}

// TestBarrierPassphrase_ZeroValueIsRefused covers the other shape a non-nil
// but unreadable buffer takes. `&secure.Buffer{}` is constructible from any
// package — every field is unexported, so the composite literal is legal and
// yields a buffer with no enclave behind it — and it is the shape a struct
// that forgot its constructor produces.
func TestBarrierPassphrase_ZeroValueIsRefused(t *testing.T) {
	ctx := context.Background()

	srv, err := NewServer(ctx, &ServerConfig{
		Backend: database.BackendMemory,
		DataDir: t.TempDir(),
		Barrier: &BarrierConfig{
			Strategy:   seal.StrategyPassphrase,
			AutoUnseal: true,
			Passphrase: &secure.Buffer{},
		},
	})
	if err == nil {
		_ = srv.Shutdown(ctx)
		t.Fatal("NewServer came up with a zero-value passphrase buffer")
	}
	var ppErr *BarrierPassphraseError
	if !errors.As(err, &ppErr) {
		t.Fatalf("err = %T (%v), want *BarrierPassphraseError", err, err)
	}
}

// TestBarrierPassphrase_DestroyThenReconstruct drives the sequence the field
// doc's own ownership rule invites: the buffer stays the CALLER'S to destroy,
// so an embedder destroys it after the first server and builds another.
//
// The second data dir is fresh, which is what makes this the silent case
// rather than a loud one.
func TestBarrierPassphrase_DestroyThenReconstruct(t *testing.T) {
	ctx := context.Background()

	buf, err := secure.NewBufferFromBytes([]byte("reused-across-two-servers"))
	if err != nil {
		t.Fatalf("secure.NewBufferFromBytes: %v", err)
	}

	cfg := func(dir string) *ServerConfig {
		return &ServerConfig{
			Backend: database.BackendMemory,
			DataDir: dir,
			Barrier: &BarrierConfig{
				Strategy:   seal.StrategyPassphrase,
				AutoUnseal: true,
				Passphrase: buf,
			},
		}
	}

	first, err := NewServer(ctx, cfg(t.TempDir()))
	if err != nil {
		buf.Destroy()
		t.Fatalf("NewServer (first): %v", err)
	}
	if shutdownErr := first.Shutdown(ctx); shutdownErr != nil {
		buf.Destroy()
		t.Fatalf("Shutdown (first): %v", shutdownErr)
	}

	// The caller owns the buffer and disposes of it, exactly as documented.
	buf.Destroy()

	second, err := NewServer(ctx, cfg(t.TempDir()))
	if err == nil {
		_ = second.Shutdown(ctx)
		t.Fatal("the second server came up on a destroyed passphrase — a none " +
			"barrier, silently, from a config that names StrategyPassphrase")
	}
	var ppErr *BarrierPassphraseError
	if !errors.As(err, &ppErr) {
		t.Fatalf("err = %T (%v), want *BarrierPassphraseError", err, err)
	}
}

// TestBarrierPassphraseError_Message covers both spellings of the diagnostic
// and the Unwrap contract. The nil-Err form is the defensive "borrow succeeded
// and returned nothing" branch, which no pkg/secure constructor can currently
// produce.
func TestBarrierPassphraseError_Message(t *testing.T) {
	withCause := &BarrierPassphraseError{Err: &secure.NilBufferError{}}
	if !strings.Contains(withCause.Error(), "refusing to fall back to an unsealed barrier") {
		t.Errorf("message must say what it refuses to do: %q", withCause.Error())
	}
	if withCause.Unwrap() == nil {
		t.Error("Unwrap must expose the borrow failure")
	}

	noCause := &BarrierPassphraseError{}
	if !strings.Contains(noCause.Error(), "holds no bytes") {
		t.Errorf("the no-cause form must say the buffer was empty: %q", noCause.Error())
	}
	if noCause.Unwrap() != nil {
		t.Error("Unwrap on the no-cause form must be nil")
	}
}

// ---------------------------------------------------------------------------
// The copy-out is wiped (go-qrdb#429)
//
// THE REGRESSION THESE PIN. Before the *secure.Buffer conversion the line was
// `softwareSecret = config.Barrier.Passphrase` — an ALIAS of the embedder's
// slice. One copy of the plaintext existed and it was the caller's, so the
// caller's own wipe removed it. Copying out of the enclave silently broke
// that: the embedder's wipe now reaches a slice nobody else holds, while
// setupBarrier's copy sits in ordinary pageable memory that NO caller can
// reach, for the life of the process, created on every NewServer.
//
// That is a NET LOSS on the exact measure this whole change is about, and it
// is invisible — nothing observable differs. Hence a test.
// ---------------------------------------------------------------------------

// swapWipeFn replaces the package's wipe hook for the duration of a test and
// restores it afterwards. The replacement still performs the real wipe, so the
// production behaviour under test is unchanged; it only records the slice.
func swapWipeFn(t *testing.T, fn func([]byte)) {
	t.Helper()
	prev := wipeFn
	wipeFn = fn
	t.Cleanup(func() { wipeFn = prev })
}

// assertWiped fails unless every byte of b is zero.
//
// A ZERO-LENGTH SLICE IS ALSO A FAILURE, and that is the assertion that makes
// the test mutation-proof rather than decorative: if the wipe is deleted from
// setupBarrier the hook is never called, the capture stays nil, and this
// reports that the test proved nothing instead of passing quietly. Same shape
// as pkg/api/rest's handlers_barrier_wipe_test.go.
func assertWiped(t *testing.T, what string, b []byte) {
	t.Helper()
	if len(b) == 0 {
		t.Fatalf("%s: nothing was ever wiped — the copy-out is still live in "+
			"pageable memory and this test proves nothing", what)
	}
	for i, c := range b {
		if c != 0 {
			t.Fatalf("%s: byte %d survived at %q — the passphrase copy was not zeroized",
				what, i, b)
		}
	}
}

// TestBarrierPassphrase_CopyOutIsWiped asserts the copy setupBarrier makes is
// zeroized before setupBarrier returns, on the SUCCESS path.
//
// The hook captures the slice as an alias, performs the real wipe, and the
// assertion runs after NewServer has returned — so a wipe that never happened,
// and a wipe that happened on the wrong slice, both fail.
func TestBarrierPassphrase_CopyOutIsWiped(t *testing.T) {
	ctx := context.Background()
	const secret = "wipe-me-after-the-barrier-is-up"

	var captured []byte
	swapWipeFn(t, func(b []byte) {
		captured = b
		secure.Wipe(b)
	})

	srv, err := NewServer(ctx, &ServerConfig{
		Backend: database.BackendMemory,
		DataDir: t.TempDir(),
		Barrier: &BarrierConfig{
			Strategy:   seal.StrategyPassphrase,
			AutoUnseal: true,
			Passphrase: barrierPassphrase(t, secret),
		},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() {
		if shutdownErr := srv.Shutdown(ctx); shutdownErr != nil {
			t.Fatalf("Shutdown: %v", shutdownErr)
		}
	}()

	if len(captured) != len(secret) {
		t.Fatalf("the wiped slice is %d bytes, want the %d-byte passphrase copy — "+
			"the wipe fired on something other than the copy-out",
			len(captured), len(secret))
	}
	assertWiped(t, "setupBarrier copy-out", captured)
}

// TestBarrierPassphrase_CopyOutIsWipedOnLaterFailure covers the path a wipe
// placed after the barrier work, instead of deferred, would miss: the copy-out
// succeeds and setupBarrier then fails further down.
//
// A leaked plaintext on an error path is the one most likely to go unnoticed,
// because nothing comes up to be inspected afterwards.
//
// The failure is a WRONG PASSPHRASE on a second boot against an existing data
// dir — runAutoUnseal's Unseal refuses it and returns BarrierUnsealError,
// which happens strictly after the copy has been made. The shamir strategy
// would NOT work here: the embedded transport never reads Passphrase on that
// path, so no copy-out occurs and there is nothing to wipe.
func TestBarrierPassphrase_CopyOutIsWipedOnLaterFailure(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	const wrong = "the-wrong-passphrase-entirely"

	// First boot establishes the barrier under the RIGHT passphrase. The hook
	// is not installed yet, so this boot contributes nothing to the capture.
	first, err := NewServer(ctx, &ServerConfig{
		Backend: database.BackendPebble,
		DataDir: dataDir,
		Barrier: &BarrierConfig{
			Strategy:   seal.StrategyPassphrase,
			AutoUnseal: true,
			Passphrase: barrierPassphrase(t, "the-right-passphrase"),
		},
	})
	if err != nil {
		t.Fatalf("NewServer (first boot): %v", err)
	}
	if shutdownErr := first.Shutdown(ctx); shutdownErr != nil {
		t.Fatalf("Shutdown (first boot): %v", shutdownErr)
	}

	var captured []byte
	swapWipeFn(t, func(b []byte) {
		captured = b
		secure.Wipe(b)
	})

	second, err := NewServer(ctx, &ServerConfig{
		Backend: database.BackendPebble,
		DataDir: dataDir,
		Barrier: &BarrierConfig{
			Strategy:   seal.StrategyPassphrase,
			AutoUnseal: true,
			Passphrase: barrierPassphrase(t, wrong),
		},
	})
	if err == nil {
		_ = second.Shutdown(ctx)
		t.Fatal("the second boot accepted the wrong passphrase")
	}

	if len(captured) != len(wrong) {
		t.Fatalf("the wiped slice is %d bytes, want the %d-byte passphrase copy",
			len(captured), len(wrong))
	}
	assertWiped(t, "setupBarrier copy-out on the error path", captured)
}
