// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package secure_test exercises the SDK re-exports the way a CONSUMER reaches
// them — importing sdk/go/secure and nothing under pkg/ — because that is the
// only import path a consumer is allowed, and an alias nothing calls from
// outside its own module is how a re-export silently goes missing.
//
// It exists because one did. The protected-memory borrow migration added
// GuardBorrows to pkg/secure, documented it in that package's rules as THE
// form to arm the detector with, and did not re-export it; nothing failed,
// because nothing in this module referenced the SDK surface at all.
package secure_test

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	securesdk "github.com/jeremyhahn/go-qrdb/sdk/go/secure"
)

// deadlockBound is how long a borrow that should have been REPORTED is given
// before the test calls it a deadlock. Generous, because the operation under
// it is microseconds and the only thing a larger number buys is tolerance for
// a loaded runner.
const deadlockBound = 20 * time.Second

// The one-line form pkg/secure's rules direct callers to must work HERE, from
// the SDK, with no pkg/* import anywhere in this file.
//
// That is the whole assertion: a consumer following rule 8 gets the arm,
// capture, Goexit and restore sequence from one deferred call, instead of
// hand-rolling the thing the guard exists to remove.
func TestGuardBorrows_ArmsAndDisarmsThroughTheSDK(t *testing.T) {
	buf, err := securesdk.NewBufferFromBytes([]byte("material"))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	defer buf.Destroy()

	// Armed, a clean path reports nothing.
	reported := false
	func() {
		defer securesdk.GuardBorrows(func(string, ...any) { reported = true })()
		if _, err := securesdk.CopyOut(buf); err != nil {
			t.Errorf("CopyOut: %v", err)
		}
	}()
	if reported {
		t.Error("GuardBorrows reported a violation on a clean SDK path")
	}

	// Disarmed again after the stop: a borrow-class call that WOULD be a
	// violation inside a borrow completes normally outside one, which is what
	// proves the stop actually restored rather than merely returned.
	if got := buf.Size(); got != len("material") {
		t.Errorf("Size after the guard was disarmed = %d, want %d", got, len("material"))
	}
}

// The guard must actually CATCH through the SDK, not merely arm. A re-export
// that returned a no-op stop would pass the test above and fail this one.
//
// # Why the wait is bounded
//
// Mutation-verified, and the result dictated the shape: replacing the body of
// the SDK's GuardBorrows with `return func() {}` does not make this test fail,
// it makes it HANG — the allocation inside the borrow becomes the real
// self-deadlock, on a goroutine this test is waiting for. Which is the
// migration's whole subject arriving in the test that asserts it is detected.
//
// So the wait is bounded and the timeout names the cause. A test that proves
// the deadlock is caught must not itself depend on the catch to terminate.
//
// The bound cannot RECOVER from a genuine hang — the wedged goroutine holds
// the purge lock for the life of the binary, so anything after this in the
// package blocks behind it. What it buys is that the failure arrives WITH its
// reason, ahead of the wedge, instead of the run dying on its wall-clock limit
// naming whichever test happened to be running.
//
// Which is why the buffer's Destroy is SKIPPED once the wait has timed out,
// and that placement is load-bearing rather than tidy. Destroy is purge-class:
// it takes the purge lock exclusively, and the wedged goroutine is holding it
// shared and never will not be. An unconditional `defer buf.Destroy()` blocks
// during the Goexit that t.Fatalf starts, so the diagnostic never prints and
// the run dies on its own timeout with the stack of the cleanup instead of the
// cause. Measured here with the mutant applied, before the skip existed.
//
// pkg/dek's TestClusterDEKManager_ToleratesABarrierThatAllocatesProtectedMemory
// learned the same thing on the same class of failure. Two tests reaching it
// independently is the argument for stating it rather than re-deriving it.
func TestGuardBorrows_ReportsAViolationThroughTheSDK(t *testing.T) {
	buf, err := securesdk.NewBuffer(32)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	var hung atomic.Bool
	defer func() {
		if hung.Load() {
			// Nothing to release: see the note above. Destroy would block on
			// the borrow that is already wedged and swallow the diagnostic.
			return
		}
		buf.Destroy()
	}()

	var got string
	func() {
		defer securesdk.GuardBorrows(func(format string, _ ...any) { got = format })()
		// On its own goroutine: the violation ends the goroutine that raised
		// it, and that must not be the one running the test.
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = buf.WithBytes(func([]byte) error {
				// Purge-class inside a borrow — the self-deadlock, which the
				// armed guard turns into a report.
				if s, secErr := securesdk.NewSecret(context.Background(), []byte("inside the borrow")); secErr == nil {
					s.Destroy()
				}
				return nil
			})
		}()
		select {
		case <-done:
		case <-time.After(deadlockBound):
			hung.Store(true)
			t.Fatalf("the allocation inside the borrow did not return within %s: "+
				"the SDK's GuardBorrows armed nothing, so this is the real "+
				"self-deadlock rather than the report it should have produced",
				deadlockBound)
		}
	}()

	if got == "" {
		t.Fatal("the SDK's GuardBorrows did not report an allocation made inside a borrow")
	}
	if !bytes.Contains([]byte(got), []byte("NESTED BORROW")) {
		t.Errorf("report = %q, want it to name the nested borrow", got)
	}
}

// EnableBorrowGuard's lower-level form must work through the SDK too, with the
// re-exported handler and error types, since a consumer installing its own
// handler cannot name *secure.NestedBorrowError any other way.
func TestEnableBorrowGuard_HandlerAndErrorTypesAreUsableFromTheSDK(t *testing.T) {
	buf, err := securesdk.NewBuffer(16)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	defer buf.Destroy()

	caught := make(chan *securesdk.NestedBorrowError, 1)
	var handler securesdk.BorrowViolationFunc = func(e *securesdk.NestedBorrowError) {
		caught <- e
		panic(e) // must not return; panicking is one of the two correct exits
	}
	restore := securesdk.EnableBorrowGuard(handler)
	defer restore()

	func() {
		defer func() { _ = recover() }()
		_ = buf.WithBytes(func([]byte) error {
			_ = buf.Size() // borrow-class, nested
			return nil
		})
	}()

	select {
	case e := <-caught:
		if e.Op != "Buffer.Size" {
			t.Errorf("violation Op = %q, want %q", e.Op, "Buffer.Size")
		}
	default:
		t.Fatal("the SDK's EnableBorrowGuard caught nothing")
	}
}

// CopyOut must copy through the SDK and refuse an empty source there too — the
// fail-closed half is the reason it exists, so a re-export that dropped it
// would be worse than no re-export.
func TestCopyOut_CopiesAndRefusesThroughTheSDK(t *testing.T) {
	const material = "0123456789abcdef"
	buf, err := securesdk.NewBufferFromBytes([]byte(material))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	defer buf.Destroy()

	out, err := securesdk.CopyOut(buf)
	if err != nil {
		t.Fatalf("CopyOut: %v", err)
	}
	if !bytes.Equal(out, []byte(material)) {
		t.Error("CopyOut returned different bytes than the buffer holds")
	}

	// A destroyed buffer reports the re-exported typed error rather than an
	// empty copy.
	spent, err := securesdk.NewBuffer(16)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	spent.Destroy()
	var nilBuf *securesdk.NilBufferError
	if _, err := securesdk.CopyOut(spent); !errors.As(err, &nilBuf) {
		t.Fatalf("CopyOut on a destroyed buffer: err = %v, want *NilBufferError", err)
	}
}

// A *Buffer obtained through the SDK must satisfy the re-exported Borrower,
// which is what lets a consumer write its own helper against the interface
// rather than the concrete type.
func TestBorrower_IsSatisfiedByTheSDKBuffer(t *testing.T) {
	buf, err := securesdk.NewBufferFromBytes([]byte("borrowable"))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	defer buf.Destroy()

	var b securesdk.Borrower = buf
	seen := 0
	if err := b.WithBytes(func(src []byte) error {
		seen = len(src)
		return nil
	}); err != nil {
		t.Fatalf("WithBytes through Borrower: %v", err)
	}
	if seen != len("borrowable") {
		t.Errorf("borrowed %d bytes, want %d", seen, len("borrowable"))
	}

	// And CopyOut must accept it as the interface, not only as the concrete
	// type — the signature consumers actually write against.
	out, err := securesdk.CopyOut(b)
	if err != nil {
		t.Fatalf("CopyOut(Borrower): %v", err)
	}
	if string(out) != "borrowable" {
		t.Errorf("CopyOut(Borrower) = %q, want %q", out, "borrowable")
	}
}

// TestErrorReExports_AreDistinctAndPopulated pins the SDK's go-enclave error
// re-exports as a consumer sees them: every sentinel is populated, and no two
// names alias the same value — a copy-paste slip that re-exported one
// go-enclave sentinel under two SDK names would leave a caller's errors.Is
// matching a condition it did not ask about.
func TestErrorReExports_AreDistinctAndPopulated(t *testing.T) {
	sentinels := map[string]error{
		"ErrAllocatorInstalled":      securesdk.ErrAllocatorInstalled,
		"ErrBudgetExhausted":         securesdk.ErrBudgetExhausted,
		"ErrLockedMemoryUnavailable": securesdk.ErrLockedMemoryUnavailable,
		"ErrReleasedWithoutWipe":     securesdk.ErrReleasedWithoutWipe,
		"ErrSealerWiped":             securesdk.ErrSealerWiped,
		"ErrSealFailed":              securesdk.ErrSealFailed,
		"ErrMaterialTooLarge":        securesdk.ErrMaterialTooLarge,
		"ErrSealedAuthentication":    securesdk.ErrSealedAuthentication,
		"ErrPlaintextEscaped":        securesdk.ErrPlaintextEscaped,
		"ErrInvalidBudget":           securesdk.ErrInvalidBudget,
		"ErrNilAllocator":            securesdk.ErrNilAllocator,
		"ErrBudgetExceedsRLimit":     securesdk.ErrBudgetExceedsRLimit,
		"ErrLimitDiscoveryFailed":    securesdk.ErrLimitDiscoveryFailed,
	}
	seen := make(map[error]string, len(sentinels))
	for name, err := range sentinels {
		if err == nil {
			t.Errorf("%s is nil", name)
			continue
		}
		if other, dup := seen[err]; dup {
			t.Errorf("%s and %s re-export the same sentinel %q", name, other, err)
		}
		seen[err] = name
	}
}

// TestErrorReExports_MatchWhatTheSDKReturns drives the SDK calls that fail
// deterministically without touching process state and matches their errors
// against the SDK's own re-exports, the only names an SDK-only consumer has.
func TestErrorReExports_MatchWhatTheSDKReturns(t *testing.T) {
	tests := []struct {
		name string
		call func() error
		want error
	}{
		{
			name: "NewAllocatorWithinRLimit refuses a non-positive budget",
			call: func() error {
				_, err := securesdk.NewAllocatorWithinRLimit(0)
				return err
			},
			want: securesdk.ErrInvalidBudget,
		},
		{
			// A nil allocator is refused before the one-shot install check, so
			// this leaves the process allocator exactly as it found it.
			name: "SetAllocator refuses a nil allocator",
			call: func() error { return securesdk.SetAllocator(nil) },
			want: securesdk.ErrNilAllocator,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want errors.Is(err, %v)", err, tc.want)
			}
		})
	}
}

// TestDiscoverLockedMemoryLimit_ReportsASource pins the re-export a binary
// derives its budget from: discovery succeeds on this host and always names
// where its figure came from, and a Bounded limit carries a positive figure.
func TestDiscoverLockedMemoryLimit_ReportsASource(t *testing.T) {
	limit, err := securesdk.DiscoverLockedMemoryLimit()
	if err != nil {
		t.Fatalf("DiscoverLockedMemoryLimit: %v", err)
	}
	if limit.Source == securesdk.LimitUnknown {
		t.Errorf("successful discovery reported source %v", limit.Source)
	}
	if limit.Bounded && (limit.Bytes <= 0 || limit.Source != securesdk.LimitRLimit) {
		t.Errorf("bounded limit = %+v, want a positive RLIMIT_MEMLOCK figure", limit)
	}
}
