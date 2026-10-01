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
	"errors"
	"sync"
	"time"
)

// The SO PIN lockout. Several SO-PIN-gated operations are reachable without
// authentication on the boot-time sealed-window listener -- barrier
// initialization, seal, CSR signing -- and the SO PIN is their only
// authorization, so every call is an online guess at the PIN. The lockout
// lives here, in a PINBackend decorator, so that every SO PIN check on the
// node counts against ONE counter whichever operation and transport made it:
// a guess spent on a seal costs the same as one spent on a rekey.
//
// Only failed checks count. The first SOPINFreeFailures failures in a row
// cost nothing, so an operator can mistype; every failure from then on locks
// the SO PIN for a backoff that doubles each time, from SOPINBaseBackoff up
// to SOPINMaxBackoff. A correct PIN resets the count. A legitimate officer is
// therefore never refused for volume: requests that never reach the check,
// and successful checks, cost nothing.
//
// Once the backoff reaches its cap an attacker gets about 96 guesses a day,
// so the SO PIN needs at least 40 bits of entropy to hold for decades -- for
// example 8 random characters from a 62-character alphabet (about 47 bits)
// or a 12-digit random number (about 40 bits).
const (
	// SOPINFreeFailures is the number of consecutive wrong SO PINs admitted
	// before the lockout engages.
	SOPINFreeFailures = 5
	// SOPINBaseBackoff is the lockout after the first failure past the free
	// ones.
	SOPINBaseBackoff = time.Second
	// SOPINMaxBackoff caps the doubling backoff.
	SOPINMaxBackoff = 15 * time.Minute
)

// SOPINLockedError is returned by every SO PIN check of a lockout backend
// (see NewLockoutBackend) while it is locked out after repeated failures. The
// presented PIN was not compared. It unwraps to ErrPINLocked, the PIN-lockout
// condition every transport answers as 429 / RESOURCE_EXHAUSTED.
type SOPINLockedError struct {
	// RetryAfter is how long until the next check is admitted.
	RetryAfter time.Duration
}

// Error implements the error interface.
func (e *SOPINLockedError) Error() string {
	return "pin: SO PIN locked after repeated failures; retry after " + e.RetryAfter.String()
}

// Unwrap returns ErrPINLocked.
func (e *SOPINLockedError) Unwrap() error { return ErrPINLocked }

// lockoutBackend decorates a PINBackend with the SO PIN lockout. Every method
// that compares a presented SO PIN -- VerifySOPIN, SetSOPIN with a current
// PIN, ChangeSOPIN, SetUserPIN, ResetLockout -- runs through check; the rest
// pass straight through.
type lockoutBackend struct {
	PINBackend

	mu          sync.Mutex
	failures    int
	lockedUntil time.Time
	// now is the clock; nil means time.Now. Tests replace it.
	now func() time.Time
}

var (
	_ PINBackend        = (*lockoutBackend)(nil)
	_ FIDO2HashVerifier = (*lockoutBackend)(nil)
)

// NewLockoutBackend returns backend decorated with the SO PIN lockout. It is
// idempotent: a backend that is already decorated is returned as it is, so a
// backend that crosses more than one composition seam keeps ONE counter. A
// nil backend is returned as nil, so the PIN-gated operations still fail
// closed with ErrBackendNotConfigured.
//
// Install it where the node's backend is chosen, before the backend is handed
// to more than one consumer: two decorators around the same backend are two
// counters, and each would admit its own free failures.
func NewLockoutBackend(backend PINBackend) PINBackend {
	if backend == nil {
		return nil
	}
	if _, ok := backend.(*lockoutBackend); ok {
		return backend
	}
	return &lockoutBackend{PINBackend: backend}
}

// VerifySOPIN verifies the SO PIN under the lockout.
func (b *lockoutBackend) VerifySOPIN(pin string) error {
	return b.check(func() error { return b.PINBackend.VerifySOPIN(pin) })
}

// SetSOPIN sets the SO PIN. A first-time set (empty currentSOPIN) compares
// nothing and passes through; a replacement verifies currentSOPIN under the
// lockout.
func (b *lockoutBackend) SetSOPIN(currentSOPIN, newSOPIN string) error {
	if currentSOPIN == "" {
		return b.PINBackend.SetSOPIN(currentSOPIN, newSOPIN)
	}
	return b.check(func() error { return b.PINBackend.SetSOPIN(currentSOPIN, newSOPIN) })
}

// SetUserPIN sets the user PIN under SO PIN authorization, verified under the
// lockout.
func (b *lockoutBackend) SetUserPIN(soPIN, newUserPIN string) error {
	return b.check(func() error { return b.PINBackend.SetUserPIN(soPIN, newUserPIN) })
}

// ChangeSOPIN changes the SO PIN, verifying currentSOPIN under the lockout.
func (b *lockoutBackend) ChangeSOPIN(currentSOPIN, newSOPIN string) error {
	return b.check(func() error { return b.PINBackend.ChangeSOPIN(currentSOPIN, newSOPIN) })
}

// ResetLockout clears the decorated backend's native lockout under SO PIN
// authorization. The SO PIN is verified here, under this lockout, before the
// decorated backend is asked: a backend with no native lockout answers
// ResetLockout with nil whatever the PIN (the software backend does), and
// that nil must neither authorize anything nor reset the failure count. It
// does not clear this lockout beyond what any correct SO PIN does.
func (b *lockoutBackend) ResetLockout(soPIN string) error {
	if err := b.VerifySOPIN(soPIN); err != nil {
		return err
	}
	return b.PINBackend.ResetLockout(soPIN)
}

// GetLockoutStatus reports the decorated backend's native lockout when it has
// one, else this decorator's SO PIN lockout.
func (b *lockoutBackend) GetLockoutStatus() *LockoutStatus {
	if status := b.PINBackend.GetLockoutStatus(); status != nil {
		return status
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock()
	status := &LockoutStatus{FailedAttempts: b.failures, MaxAttempts: SOPINFreeFailures}
	if now.Before(b.lockedUntil) {
		status.IsLocked = true
		status.LockoutUntil = b.lockedUntil
		status.RecoverySeconds = int(b.lockedUntil.Sub(now).Seconds())
	}
	return status
}

// VerifyFIDO2Hash delegates to the decorated backend's FIDO2HashVerifier, and
// reports false when it has none -- what Service reports for such a backend.
func (b *lockoutBackend) VerifyFIDO2Hash(hash []byte) bool {
	if v, ok := b.PINBackend.(FIDO2HashVerifier); ok {
		return v.VerifyFIDO2Hash(hash)
	}
	return false
}

// clock returns the current time.
func (b *lockoutBackend) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

// check runs verify, the decorated backend's SO PIN comparison, unless the SO
// PIN is locked out. Checks run one at a time and a failure is counted before
// the next check starts, so guesses fired concurrently are no cheaper than
// guesses fired in turn. It returns verify's result unchanged, or
// *SOPINLockedError without running verify while locked out. Only a wrong PIN
// counts as a failure (see isSOPINMismatch): a backend that cannot answer is
// not a guess.
func (b *lockoutBackend) check(verify func() error) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.clock()
	if now.Before(b.lockedUntil) {
		return &SOPINLockedError{RetryAfter: b.lockedUntil.Sub(now)}
	}
	err := verify()
	if err == nil {
		b.failures = 0
		b.lockedUntil = time.Time{}
		return nil
	}
	if !isSOPINMismatch(err) {
		return err
	}
	b.failures++
	if b.failures >= SOPINFreeFailures {
		b.lockedUntil = now.Add(soPINBackoff(b.failures - SOPINFreeFailures))
	}
	return err
}

// isSOPINMismatch reports whether err says the presented SO PIN was wrong, in
// any of the shapes a backend answers it: ErrPINInvalid from a verification,
// ErrInvalidCurrentPIN from a change, *ErrPINInvalidWithStatus from a backend
// with native attempt counters.
func isSOPINMismatch(err error) bool {
	var withStatus *ErrPINInvalidWithStatus
	return errors.Is(err, ErrPINInvalid) || errors.Is(err, ErrInvalidCurrentPIN) || errors.As(err, &withStatus)
}

// soPINBackoff is the lockout after the n-th failure past the free ones:
// SOPINBaseBackoff doubled n times, capped at SOPINMaxBackoff.
func soPINBackoff(n int) time.Duration {
	backoff := SOPINBaseBackoff
	for i := 0; i < n && backoff < SOPINMaxBackoff; i++ {
		backoff *= 2
	}
	return min(backoff, SOPINMaxBackoff)
}
