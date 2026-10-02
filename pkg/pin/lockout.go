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
	"reflect"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// The SO PIN lockout. Several SO-PIN-gated operations are reachable without
// authentication on the boot-time sealed-window listener -- barrier
// initialization, seal, CSR signing -- and the SO PIN is their only
// authorization, so every such call is an online guess at the PIN. The
// lockout sits in a PINBackend decorator, so every SO PIN check on the node
// is counted whichever operation and transport made it.
//
// The counting itself is auth.SOPINLockout, the one implementation shared
// with the per-tenant SO PIN (auth.SOAuthenticator): a counter for anonymous
// sealed-window checks and one per authenticated principal (an identity, or
// a verified client certificate), under an aggregate cap, so no caller --
// anonymous or holding another certificate -- can lock the Security Officer
// out. See auth.SOPINLockout for the policy, the backoff and
// the recovery path.

// SOPINLockedError is returned by every SO PIN check of a lockout backend
// (see NewLockoutBackend) while its counter is locked out after repeated
// failures (auth.SOPINLockout). The presented PIN was not compared. It unwraps to ErrPINLocked,
// the PIN-lockout condition every transport answers as 429 /
// RESOURCE_EXHAUSTED.
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

// lockoutShared is the state one decorated backend's views share: the
// counting, keyed by the node.
type lockoutShared struct {
	lockout *auth.SOPINLockout
}

// nodeSOPINKey keys the node SO PIN in the shared lockout.
const nodeSOPINKey = "node"

// lockoutBackend decorates a PINBackend with the SO PIN lockout. It is a view
// of the decoration bound to one caller: NewLockoutBackend returns the
// anonymous view, and ForCaller the view of the caller in a context. Every
// method that compares a presented SO PIN -- VerifySOPIN, SetSOPIN with a
// current PIN, ChangeSOPIN, SetUserPIN, ResetLockout -- runs through check on
// the view's counter; the rest pass straight through.
type lockoutBackend struct {
	PINBackend
	shared *lockoutShared
	// principal is the caller this view counts against (auth.CallerPrincipal);
	// "" is the anonymous caller.
	principal string
}

var (
	_ PINBackend        = (*lockoutBackend)(nil)
	_ FIDO2HashVerifier = (*lockoutBackend)(nil)
)

// NewLockoutBackend returns backend decorated with the SO PIN lockout, as its
// anonymous view: a check made through it without a caller in hand counts
// against the anonymous counter, which can never lock out an authenticated
// SO. Callers that know who is asking select the counter with ForCaller.
//
// It is idempotent twice over: a backend that is already decorated is
// returned as it is, and decorating the same raw backend again -- in two
// transport options, say -- returns the decoration made the first time
// (lockoutRegistry), so every consumer of one backend shares ONE set of
// counters and one budget of free failures. A nil backend is returned as nil,
// so the PIN-gated operations still fail closed with ErrBackendNotConfigured.
//
// A backend that implements auth.SOPINKnownGoodStore (SoftwareBackend does)
// persists the lockout's known-good set, so a Security Officer's exemption
// from the aggregate cap survives a restart.
func NewLockoutBackend(backend PINBackend) PINBackend {
	if backend == nil {
		return nil
	}
	if _, ok := backend.(*lockoutBackend); ok {
		return backend
	}
	if !reflect.TypeOf(backend).Comparable() {
		// A backend whose dynamic type cannot key a map cannot be
		// recognised again; it is decorated afresh each time.
		return newLockoutBackend(backend, nil)
	}
	if existing, ok := lockoutRegistry.Load(backend); ok {
		return existing.(*lockoutBackend) //nolint:errcheck // type assertion on a value this container only ever holds
	}
	decorated, _ := lockoutRegistry.LoadOrStore(backend, newLockoutBackend(backend, nil))
	return decorated.(*lockoutBackend) //nolint:errcheck // type assertion on a value this container only ever holds
}

// lockoutRegistry maps each raw backend NewLockoutBackend has decorated to its
// decoration, keyed by the backend's identity, so two decorations of one
// backend share one set of counters. A node's PIN backends live as long as
// the process, so entries are never removed.
var lockoutRegistry sync.Map

// newLockoutBackend decorates backend with a lockout on clock (nil means
// time.Now), as its anonymous view.
func newLockoutBackend(backend PINBackend, clock func() time.Time) *lockoutBackend {
	var opts []auth.SOPINLockoutOption
	if store, ok := backend.(auth.SOPINKnownGoodStore); ok {
		opts = append(opts, auth.WithKnownGoodStore(store))
	}
	return &lockoutBackend{PINBackend: backend, shared: &lockoutShared{lockout: auth.NewSOPINLockout(clock, opts...)}}
}

// ForCaller returns the view of backend whose SO PIN checks count against the
// caller in ctx (auth.CallerPrincipal): that principal's own counter for an
// authenticated identity (not admitted anonymously by the sealed window) or a
// TLS-verified certificate, bounded by the aggregate cap, and the anonymous
// counter otherwise. A backend that is not decorated is returned as it is.
func ForCaller(ctx context.Context, backend PINBackend) PINBackend {
	b, ok := backend.(*lockoutBackend)
	if !ok {
		return backend
	}
	return &lockoutBackend{PINBackend: b.PINBackend, shared: b.shared, principal: auth.CallerPrincipal(ctx)}
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
// one, else this view's counter.
func (b *lockoutBackend) GetLockoutStatus() *LockoutStatus {
	if status := b.PINBackend.GetLockoutStatus(); status != nil {
		return status
	}
	failures, lockedUntil := b.shared.lockout.Status(nodeSOPINKey, b.principal)
	status := &LockoutStatus{FailedAttempts: failures, MaxAttempts: auth.SOPINFreeFailures}
	if !lockedUntil.IsZero() {
		status.IsLocked = true
		status.LockoutUntil = lockedUntil
		status.RecoverySeconds = int(lockedUntil.Sub(b.shared.lockout.Now()).Seconds())
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

// check runs verify, the decorated backend's SO PIN comparison, through the
// shared lockout on this view's counter. A refusal while locked out is
// *SOPINLockedError, and verify did not run. Only a wrong PIN counts (see
// isSOPINMismatch): a backend that cannot answer is not a guess.
func (b *lockoutBackend) check(verify func() error) error {
	err := b.shared.lockout.Check(context.Background(), nodeSOPINKey, b.principal, verify, isSOPINMismatch)
	var locked *auth.SOPINLockedError
	if errors.As(err, &locked) {
		return &SOPINLockedError{RetryAfter: locked.RetryAfter}
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
