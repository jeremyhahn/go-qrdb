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

package pin

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

const (
	lockoutSOPIN    = "so-pin-1234"
	lockoutWrongPIN = "wrong-pin-0000"
)

// newLockoutFixture returns a provisioned software backend decorated with the
// lockout, on a fake clock the test advances.
func newLockoutFixture(t *testing.T) (*lockoutBackend, *SoftwareBackend, *time.Time) {
	t.Helper()
	inner := newProvisionBackend(t)
	if err := inner.SetSOPIN("", lockoutSOPIN); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_000_000, 0)
	clock := &now
	b := newLockoutBackend(inner, func() time.Time { return *clock })
	return b, inner, clock
}

// spendFreeFailures spends every free failure on VerifySOPIN.
func spendFreeFailures(t *testing.T, b PINBackend) {
	t.Helper()
	for i := 0; i < auth.SOPINFreeFailures; i++ {
		if err := b.VerifySOPIN(lockoutWrongPIN); !errors.Is(err, ErrPINInvalid) {
			t.Fatalf("wrong SO PIN %d = %v, want ErrPINInvalid", i+1, err)
		}
	}
}

// requireLocked asserts err is the lockout refusal with retry.
func requireLocked(t *testing.T, err error, retry time.Duration) {
	t.Helper()
	var locked *SOPINLockedError
	if !errors.As(err, &locked) {
		t.Fatalf("got %v, want *SOPINLockedError", err)
	}
	if locked.RetryAfter != retry {
		t.Fatalf("RetryAfter = %v, want %v", locked.RetryAfter, retry)
	}
	if !errors.Is(err, ErrPINLocked) {
		t.Fatal("SOPINLockedError must unwrap to ErrPINLocked")
	}
}

// TestLockoutBackend_BackoffDoublesAndResets walks the lockout on a fake
// clock: free failures cost nothing; each failure after them locks the SO PIN
// for a backoff that doubles; a locked check is refused without comparing,
// even for the right PIN; and the right PIN, once admitted, resets the count.
func TestLockoutBackend_BackoffDoublesAndResets(t *testing.T) {
	b, _, now := newLockoutFixture(t)
	spendFreeFailures(t, b)
	requireLocked(t, b.VerifySOPIN(lockoutSOPIN), auth.SOPINBaseBackoff)

	*now = now.Add(auth.SOPINBaseBackoff)
	if err := b.VerifySOPIN(lockoutWrongPIN); !errors.Is(err, ErrPINInvalid) {
		t.Fatalf("wrong PIN after the backoff = %v", err)
	}
	requireLocked(t, b.VerifySOPIN(lockoutSOPIN), 2*auth.SOPINBaseBackoff)

	*now = now.Add(2 * auth.SOPINBaseBackoff)
	if err := b.VerifySOPIN(lockoutSOPIN); err != nil {
		t.Fatalf("right PIN after the backoff = %v", err)
	}
	for i := 0; i < auth.SOPINFreeFailures-1; i++ {
		if err := b.VerifySOPIN(lockoutWrongPIN); !errors.Is(err, ErrPINInvalid) {
			t.Fatalf("free failure %d after a reset = %v", i+1, err)
		}
	}
	if err := b.VerifySOPIN(lockoutSOPIN); err != nil {
		t.Fatalf("right PIN within the free failures = %v", err)
	}
}

// TestLockoutBackend_EverySOPINComparisonShares: every method that compares a
// presented SO PIN counts against, and is refused by, the one counter.
func TestLockoutBackend_EverySOPINComparisonShares(t *testing.T) {
	guesses := map[string]func(PINBackend, string) error{
		"VerifySOPIN":  func(b PINBackend, p string) error { return b.VerifySOPIN(p) },
		"SetSOPIN":     func(b PINBackend, p string) error { return b.SetSOPIN(p, "new-so-pin-9999") },
		"ChangeSOPIN":  func(b PINBackend, p string) error { return b.ChangeSOPIN(p, "new-so-pin-9999") },
		"SetUserPIN":   func(b PINBackend, p string) error { return b.SetUserPIN(p, "user-pin-1234") },
		"ResetLockout": func(b PINBackend, p string) error { return b.ResetLockout(p) },
	}
	for name, guess := range guesses {
		t.Run(name, func(t *testing.T) {
			b, _, _ := newLockoutFixture(t)
			for i := 0; i < auth.SOPINFreeFailures; i++ {
				if err := guess(b, lockoutWrongPIN); !isSOPINMismatch(err) {
					t.Fatalf("wrong SO PIN %d via %s = %v, want a mismatch", i+1, name, err)
				}
			}
			requireLocked(t, b.VerifySOPIN(lockoutSOPIN), auth.SOPINBaseBackoff)
			requireLocked(t, guess(b, lockoutSOPIN), auth.SOPINBaseBackoff)
		})
	}
}

// TestLockoutBackend_ConcurrentGuessesAreCountedBeforeTheNext: SO PIN guesses
// are compared one at a time and a failure is counted before the next, so
// firing many at once buys no more comparisons than firing them in turn.
func TestLockoutBackend_ConcurrentGuessesAreCountedBeforeTheNext(t *testing.T) {
	b, _, _ := newLockoutFixture(t)
	const guesses = 50
	var compared atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < guesses; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if errors.Is(b.VerifySOPIN(lockoutWrongPIN), ErrPINInvalid) {
				compared.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := compared.Load(); n != auth.SOPINFreeFailures {
		t.Fatalf("%d of %d concurrent SO PIN guesses were compared; want exactly %d", n, guesses, auth.SOPINFreeFailures)
	}
}

// TestLockoutBackend_OnlyAWrongPINCounts: a backend that cannot answer is not
// a guess, so it neither counts toward the lockout nor is masked.
func TestLockoutBackend_OnlyAWrongPINCounts(t *testing.T) {
	b := NewLockoutBackend(newProvisionBackend(t))
	for i := 0; i < 3*auth.SOPINFreeFailures; i++ {
		if err := b.VerifySOPIN(lockoutSOPIN); !errors.Is(err, ErrPINNotSet) {
			t.Fatalf("VerifySOPIN with no SO PIN = %v, want ErrPINNotSet", err)
		}
	}
	if err := b.SetSOPIN("", lockoutSOPIN); err != nil {
		t.Fatalf("first-time SetSOPIN = %v", err)
	}
	if err := b.VerifySOPIN(lockoutSOPIN); err != nil {
		t.Fatalf("right PIN = %v", err)
	}
}

// TestLockoutBackend_FirstTimeSetComparesNothing: provisioning the first SO
// PIN carries no current PIN, so it passes through even while locked out.
func TestLockoutBackend_FirstTimeSetComparesNothing(t *testing.T) {
	b, _, _ := newLockoutFixture(t)
	spendFreeFailures(t, b)
	if err := b.SetSOPIN("", "another-pin-1"); !errors.Is(err, ErrPINAlreadySet) {
		t.Fatalf("SetSOPIN with no current PIN = %v, want the backend's own ErrPINAlreadySet", err)
	}
}

// TestNewLockoutBackend_IdempotentAndNil: decorating twice keeps one counter;
// nil stays nil so PIN-gated operations fail closed.
func TestNewLockoutBackend_IdempotentAndNil(t *testing.T) {
	if NewLockoutBackend(nil) != nil {
		t.Fatal("NewLockoutBackend(nil) must be nil")
	}
	once := NewLockoutBackend(newProvisionBackend(t))
	if twice := NewLockoutBackend(once); twice != once {
		t.Fatal("decorating a decorated backend must return it unchanged")
	}
}

// TestLockoutBackend_LockoutStatus reports the decorator's state when the
// decorated backend has no native lockout, and the native one when it does.
func TestLockoutBackend_LockoutStatus(t *testing.T) {
	b, _, _ := newLockoutFixture(t)
	if s := b.GetLockoutStatus(); s == nil || s.IsLocked || s.FailedAttempts != 0 || s.MaxAttempts != auth.SOPINFreeFailures {
		t.Fatalf("fresh status = %+v", s)
	}
	spendFreeFailures(t, b)
	s := b.GetLockoutStatus()
	if s == nil || !s.IsLocked || s.FailedAttempts != auth.SOPINFreeFailures || s.RecoverySeconds != int(auth.SOPINBaseBackoff.Seconds()) {
		t.Fatalf("locked status = %+v", s)
	}

	native := &LockoutStatus{FailedAttempts: 2, MaxAttempts: 10}
	withNative := NewLockoutBackend(&nativeLockoutBackend{PINBackend: newProvisionBackend(t), status: native})
	if got := withNative.GetLockoutStatus(); got != native {
		t.Fatalf("status over a native lockout = %+v, want the native one", got)
	}
}

// TestLockoutBackend_FIDO2Delegates: the decorator keeps the decorated
// backend's FIDO2 verifier, and reports false when there is none.
func TestLockoutBackend_FIDO2Delegates(t *testing.T) {
	b, inner, _ := newLockoutFixture(t)
	if err := inner.SetUserPIN(lockoutSOPIN, "user-pin-1234"); err != nil {
		t.Fatal(err)
	}
	if !b.VerifyFIDO2Hash(ComputeFIDO2PINHash("user-pin-1234")) {
		t.Fatal("the decorated software backend's FIDO2 verifier must answer through the decorator")
	}
	plain := NewLockoutBackend(&nativeLockoutBackend{PINBackend: newProvisionBackend(t)}).(*lockoutBackend)
	if plain.VerifyFIDO2Hash(ComputeFIDO2PINHash("user-pin-1234")) {
		t.Fatal("a backend with no FIDO2 verifier must answer false")
	}
}

// TestForCaller_SeparateCounters: anonymous and authenticated SO PIN checks
// count against separate counters. Anonymous wrong PINs lock only the
// anonymous counter -- an authenticated SO is still admitted -- and an
// authenticated caller's own wrong PINs lock only the authenticated one.
func TestForCaller_SeparateCounters(t *testing.T) {
	b, _, _ := newLockoutFixture(t)
	anonymous := context.Background()
	so := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "officer", TenantID: "system"})

	spendFreeFailures(t, ForCaller(anonymous, b))
	requireLocked(t, ForCaller(anonymous, b).VerifySOPIN(lockoutSOPIN), auth.SOPINBaseBackoff)
	if err := ForCaller(so, b).VerifySOPIN(lockoutSOPIN); err != nil {
		t.Fatalf("an authenticated SO after anonymous guesses = %v, want admitted", err)
	}

	spendFreeFailures(t, ForCaller(so, b))
	requireLocked(t, ForCaller(so, b).VerifySOPIN(lockoutSOPIN), auth.SOPINBaseBackoff)
}

// TestForCaller_UndecoratedPassesThrough: an undecorated backend is
// returned as it is.
func TestForCaller_UndecoratedPassesThrough(t *testing.T) {
	plain := newProvisionBackend(t)
	if ForCaller(context.Background(), plain) != PINBackend(plain) {
		t.Fatal("ForCaller must return an undecorated backend as it is")
	}
}

// TestIsSOPINMismatch_Shapes: every shape in which a backend answers "wrong
// SO PIN" counts; anything else does not.
func TestIsSOPINMismatch_Shapes(t *testing.T) {
	mismatches := []error{ErrPINInvalid, ErrInvalidCurrentPIN, &ErrPINInvalidWithStatus{Status: &LockoutStatus{MaxAttempts: 3}}}
	for _, err := range mismatches {
		if !isSOPINMismatch(err) {
			t.Errorf("%v must count as a mismatch", err)
		}
	}
	for _, err := range []error{ErrPINNotSet, ErrBackendNotConfigured, ErrPINAlreadySet, &ErrTPMLocked{Status: &LockoutStatus{}}} {
		if isSOPINMismatch(err) {
			t.Errorf("%v must not count as a mismatch", err)
		}
	}
}

// nativeLockoutBackend stands in for a hardware backend: it reports its own
// lockout status and has no FIDO2 verifier. Everything else is the real
// software backend.
type nativeLockoutBackend struct {
	PINBackend
	status *LockoutStatus
}

func (n *nativeLockoutBackend) GetLockoutStatus() *LockoutStatus { return n.status }

var _ PINBackend = (*nativeLockoutBackend)(nil)

// TestWithLockoutClock_TheLockoutRunsOnTheInjectedClock: a decoration made
// with WithLockoutClock measures its backoff on that clock -- frozen, the
// lockout holds whatever the wall clock does; advanced past the backoff, the
// right PIN is admitted again.
func TestWithLockoutClock_TheLockoutRunsOnTheInjectedClock(t *testing.T) {
	inner := newProvisionBackend(t)
	if err := inner.SetSOPIN("", lockoutSOPIN); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_000_000, 0)
	b := NewLockoutBackend(inner, WithLockoutClock(func() time.Time { return now }))
	spendFreeFailures(t, b)
	requireLocked(t, b.VerifySOPIN(lockoutSOPIN), auth.SOPINBaseBackoff)
	if status := b.GetLockoutStatus(); !status.IsLocked || !status.LockoutUntil.Equal(now.Add(auth.SOPINBaseBackoff)) {
		t.Fatalf("status = %+v, want locked until the injected now plus the base backoff", status)
	}
	now = now.Add(auth.SOPINBaseBackoff)
	if err := b.VerifySOPIN(lockoutSOPIN); err != nil {
		t.Fatalf("the right PIN once the injected clock passed the backoff = %v", err)
	}
}

// TestWithLockoutClock_OnlyTheFirstDecorationIsConfigured: options configure
// the decoration a call creates; decorating the same raw backend again with
// other options returns the first decoration, on the first clock, so every
// consumer still shares one lockout. A nil clock keeps time.Now.
func TestWithLockoutClock_OnlyTheFirstDecorationIsConfigured(t *testing.T) {
	inner := newProvisionBackend(t)
	if err := inner.SetSOPIN("", lockoutSOPIN); err != nil {
		t.Fatal(err)
	}
	frozen := time.Unix(1_000_000, 0)
	first := NewLockoutBackend(inner, WithLockoutClock(func() time.Time { return frozen }))
	second := NewLockoutBackend(inner, WithLockoutClock(func() time.Time { return frozen.Add(time.Hour) }))
	if first != second {
		t.Fatal("a second decoration with other options is a different lockout")
	}
	if got := first.(*lockoutBackend).shared.lockout.Now(); !got.Equal(frozen) {
		t.Fatalf("the shared lockout reads %v, want the first decoration's clock", got)
	}
	wall := NewLockoutBackend(newProvisionBackend(t), WithLockoutClock(nil))
	if got := wall.(*lockoutBackend).shared.lockout.Now(); time.Since(got) > time.Minute || got.Before(frozen.Add(time.Hour)) {
		t.Fatalf("a nil clock reads %v, want time.Now", got)
	}
}
