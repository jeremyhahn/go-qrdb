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

// Package secure re-exports go-qrdb's secure-memory types so SDK
// consumers never need to import the internal pkg/secure path or raw
// go-enclave. All public types and constructors are aliases — there is no
// logic in this file, EXCEPT the process-lifecycle re-exports
// (SetAllocator, ProcessAllocator, WipeAll), which are pure aliases over
// go-enclave itself: they belong to the binary that owns the process, not to
// pkg/secure, so this is their only home in the SDK (go-qrdb#114,
// go-qrdb#553).
//
// See github.com/jeremyhahn/go-qrdb/pkg/secure for the canonical
// documentation.
package secure

import (
	"context"
	"log/slog"

	"github.com/jeremyhahn/go-enclave"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// Secret is the SDK re-export of secure.Secret.
type Secret = secure.Secret

// Buffer is the SDK re-export of secure.Buffer.
type Buffer = secure.Buffer

// PassphraseProvider is the SDK re-export of secure.PassphraseProvider.
type PassphraseProvider = secure.PassphraseProvider

// TerminalProvider is the SDK re-export of secure.TerminalProvider.
type TerminalProvider = secure.TerminalProvider

// StdinProvider is the SDK re-export of secure.StdinProvider.
type StdinProvider = secure.StdinProvider

// CallbackProvider is the SDK re-export of secure.CallbackProvider.
type CallbackProvider = secure.CallbackProvider

// Borrower is the SDK re-export of secure.Borrower — the borrow half of
// *Buffer, and the narrowest thing a reader of protected material needs.
type Borrower = secure.Borrower

// BorrowViolationFunc is the SDK re-export of secure.BorrowViolationFunc.
type BorrowViolationFunc = secure.BorrowViolationFunc

// Error type re-exports.
type (
	NilSecretError    = secure.NilSecretError
	NilBufferError    = secure.NilBufferError
	OpenError         = secure.OpenError
	InvalidSizeError  = secure.InvalidSizeError
	ProviderError     = secure.ProviderError
	NestedBorrowError = secure.NestedBorrowError
	EmptyBorrowError  = secure.EmptyBorrowError

	ProcessHardeningError               = secure.ProcessHardeningError
	ProcessInspectionOptOutError        = secure.ProcessInspectionOptOutError
	ProcessInspectionOptOutRefusedError = secure.ProcessInspectionOptOutRefusedError
	ProcessTracedError                  = secure.ProcessTracedError
)

// ProcessHardening is the SDK re-export of secure.ProcessHardening: what
// HardenProcess did to the process.
type ProcessHardening = secure.ProcessHardening

// The HardenProcess outcomes, re-exported from secure.
const (
	ProcessHardened             = secure.ProcessHardened
	ProcessInspectionAllowed    = secure.ProcessInspectionAllowed
	ProcessHardeningUnavailable = secure.ProcessHardeningUnavailable
)

// AllowProcessInspectionEnv is the SDK re-export of
// secure.AllowProcessInspectionEnv: the environment variable that opts a
// process out of HardenProcess, with a logged warning, so a debugger can
// attach.
const AllowProcessInspectionEnv = secure.AllowProcessInspectionEnv

// ErrAllocatorInstalled is the SDK re-export of the same sentinel
// enclave.SetAllocator returns: the process-wide locked-memory budget was
// already installed, by an earlier SetAllocator call or by a library's first
// ProcessAllocator call installing the default. It is the same underlying
// error value, so errors.Is against it works through either import path.
var ErrAllocatorInstalled = enclave.ErrAllocatorInstalled

// Operational sentinels a caller can see, unwrapped, from this package's own
// public API — re-exported so an SDK-only consumer can errors.Is against them
// without importing raw go-enclave, which is exactly the boundary this
// package exists to hold (go-qrdb#114 review, rounds 3 and 4: round 3's pass
// covered NewBuffer/NewBufferFromBytes/DestroyErr/sealing but missed
// NewAllocatorWithinRLimit's and SetAllocator's own error surface, closed
// here). Every one of these actually reaches a caller RAW: every OTHER
// go-enclave error this package's internals touch (ErrInvalidSize,
// ErrEnclaveDestroyed, ErrEmptyEnclave, …) is translated into one of this
// package's own typed errors (InvalidSizeError, NilBufferError) before it is
// ever returned, so re-exporting those would name a sentinel a caller could
// never actually match against here.
var (
	// ErrBudgetExhausted is returned (wrapped) by NewBuffer and
	// NewBufferFromBytes when the process-wide locked-memory budget has no
	// room left for the requested allocation.
	ErrBudgetExhausted = enclave.ErrBudgetExhausted

	// ErrLockedMemoryUnavailable is returned (wrapped) by NewBuffer and
	// NewBufferFromBytes when the kernel itself refuses to lock the requested
	// pages, independent of the configured budget.
	ErrLockedMemoryUnavailable = enclave.ErrLockedMemoryUnavailable

	// ErrReleasedWithoutWipe is returned by Buffer.DestroyErr when the
	// environment denied the mprotect needed to make a frozen region writable
	// again before wiping it: the pages were unmapped without this package's
	// in-process zeroing, though nothing leaked to another process either way.
	ErrReleasedWithoutWipe = enclave.ErrReleasedWithoutWipe

	// ErrSealerWiped is returned (wrapped) by NewSecret, NewSecretFromBuffer
	// and Secret.Open once a shutdown WipeAll has destroyed the process
	// Sealer's keys: sealed values become permanently unopenable from that
	// point on.
	ErrSealerWiped = enclave.ErrSealerWiped

	// ErrSealFailed is returned (wrapped) by NewSecret and
	// NewSecretFromBuffer when the underlying AEAD sealing step itself fails
	// — a cause other than the process Sealer being wiped or the material
	// exceeding the per-message ceiling, both of which have their own,
	// narrower sentinels below.
	ErrSealFailed = enclave.ErrSealFailed

	// ErrMaterialTooLarge is returned (wrapped) by NewSecret and
	// NewSecretFromBuffer when the value being sealed exceeds go-enclave's
	// per-message AEAD ceiling.
	ErrMaterialTooLarge = enclave.ErrMaterialTooLarge

	// ErrSealedAuthentication is returned (wrapped in an *OpenError) by
	// Secret.Open when the sealed material fails AEAD authentication. It is
	// deliberately silent about which part failed.
	ErrSealedAuthentication = enclave.ErrSealedAuthentication

	// ErrPlaintextEscaped is returned (wrapped in an *OpenError) by
	// Secret.Open if the AEAD wrote recovered plaintext anywhere other than
	// the locked destination. go-enclave fails rather than copying the stray
	// plaintext in.
	ErrPlaintextEscaped = enclave.ErrPlaintextEscaped

	// ErrInvalidBudget is returned by NewAllocatorWithinRLimit when the
	// requested budget is not positive — a caller mistake checked before any
	// syscall, on every platform.
	ErrInvalidBudget = enclave.ErrInvalidBudget

	// ErrNilAllocator is returned by SetAllocator when called with a nil
	// Allocator.
	ErrNilAllocator = enclave.ErrNilAllocator

	// ErrBudgetExceedsRLimit is returned (wrapped in a *RLimitError) by
	// NewAllocatorWithinRLimit when the requested budget exceeds the
	// process's discovered RLIMIT_MEMLOCK ceiling.
	ErrBudgetExceedsRLimit = enclave.ErrBudgetExceedsRLimit

	// ErrLimitDiscoveryFailed is returned (wrapped in a *LimitDiscoveryError)
	// by NewAllocatorWithinRLimit when the process's locked-memory ceiling
	// itself could not be read.
	ErrLimitDiscoveryFailed = enclave.ErrLimitDiscoveryFailed
)

// Error type re-exports for the errors above that carry structured detail
// beyond their sentinel — errors.As, not just errors.Is, needs the concrete
// type. Both are returned exclusively by NewAllocatorWithinRLimit.
type (
	RLimitError         = enclave.RLimitError
	LimitDiscoveryError = enclave.LimitDiscoveryError
)

// LockedMemoryLimit is the SDK re-export of enclave.LockedMemoryLimit: a
// snapshot of the process's locked-memory ceiling, as DiscoverLockedMemoryLimit
// read it. Bytes is a ceiling to rely on only when Bounded is true.
type LockedMemoryLimit = enclave.LockedMemoryLimit

// LimitSource is the SDK re-export of enclave.LimitSource: where a discovered
// locked-memory ceiling came from, or why there is none.
type LimitSource = enclave.LimitSource

// The LimitSource values DiscoverLockedMemoryLimit reports, re-exported so a
// binary deriving its budget from the host can record the derivation without
// importing raw go-enclave.
const (
	LimitUnknown     = enclave.LimitUnknown
	LimitRLimit      = enclave.LimitRLimit
	LimitUnlimited   = enclave.LimitUnlimited
	LimitNotEnforced = enclave.LimitNotEnforced
	LimitUnsupported = enclave.LimitUnsupported
)

// DiscoverLockedMemoryLimit is the SDK re-export of
// enclave.DiscoverLockedMemoryLimit. It reads the process's current
// locked-memory ceiling so a binary can DERIVE its budget from the host
// rather than guess one; see cmd/qrdbd's resolveSecureMemoryBudget for the
// reference derivation. A failure to read the ceiling is
// ErrLimitDiscoveryFailed (as a *LimitDiscoveryError); an unbounded ceiling
// is not an error and reports Bounded false.
func DiscoverLockedMemoryLimit() (LockedMemoryLimit, error) {
	return enclave.DiscoverLockedMemoryLimit()
}

// Allocator is the SDK re-export of enclave.Allocator: the process-wide
// locked-memory budget every Buffer allocation and every Secret.Open admits
// against. Sealing (NewSecret, NewSecretFromBuffer's internal seal) is NOT
// admitted against it — go-enclave has no budgeted seal path as of
// v0.6.0-alpha, so that allocation is unbounded. It is a one-time cost per
// value rather than a per-request one, unlike Open, which is why go-enclave
// scoped the gap that way; see pkg/secure.Secret's documentation.
type Allocator = enclave.Allocator

// CopyOut is the SDK re-export of secure.CopyOut. It copies protected bytes
// into a fresh caller-owned slice with the borrow held across the whole copy,
// and refuses an empty source rather than returning zeros.
func CopyOut(b Borrower) ([]byte, error) { return secure.CopyOut(b) }

// EnableBorrowGuard is the SDK re-export of secure.EnableBorrowGuard. It arms
// nested-borrow detection so a callback that reaches an allocation of
// protected memory REPORTS instead of deadlocking, and returns a function that
// restores the previous state.
//
// It is a test facility: arming it costs a runtime.Stack call per borrow. It
// is re-exported because a consumer wiring its own protected-memory code
// against this SDK needs the same check the owning repository uses, and cannot
// get it from pkg/secure (SDK consumers never import pkg/*).
func EnableBorrowGuard(onViolation BorrowViolationFunc) (restore func()) {
	return secure.EnableBorrowGuard(onViolation)
}

// GuardBorrows is the SDK re-export of secure.GuardBorrows. It arms the
// nested-borrow detector for the duration of a test and returns a stop
// function that reports the first violation caught, if any:
//
//	defer securesdk.GuardBorrows(t.Fatalf)()
//
// report is t.Fatalf or t.Errorf; nothing in this package imports testing.
//
// DEFERRED, not called at the end of the test. A violation ends the goroutine
// that raised it — including the test's own — so a stop written as the last
// statement is never reached, and the run reports an unexplained Goexit
// instead of the deadlock.
//
// This is the form pkg/secure's package documentation directs callers to, and
// it is re-exported for the reason EnableBorrowGuard is: a consumer following
// that rule against this SDK cannot import pkg/*, and without this would have
// to hand-roll the arm / capture / Goexit / restore sequence — the exact
// footgun the guard exists to remove.
func GuardBorrows(report func(format string, args ...any)) (stop func()) {
	return secure.GuardBorrows(report)
}

// NewSecret is the SDK re-export of secure.NewSecret.
func NewSecret(ctx context.Context, src []byte) (*Secret, error) {
	return secure.NewSecret(ctx, src)
}

// NewSecretFromBuffer is the SDK re-export of secure.NewSecretFromBuffer.
func NewSecretFromBuffer(ctx context.Context, buf *Buffer) (*Secret, error) {
	return secure.NewSecretFromBuffer(ctx, buf)
}

// NewBuffer is the SDK re-export of secure.NewBuffer.
func NewBuffer(size int) (*Buffer, error) { return secure.NewBuffer(size) }

// NewBufferFromBytes is the SDK re-export of secure.NewBufferFromBytes.
func NewBufferFromBytes(src []byte) (*Buffer, error) {
	return secure.NewBufferFromBytes(src)
}

// Wipe is the SDK re-export of secure.Wipe.
func Wipe(b []byte) { secure.Wipe(b) }

// NewTerminalProvider is the SDK re-export of secure.NewTerminalProvider.
func NewTerminalProvider() *TerminalProvider { return secure.NewTerminalProvider() }

// NewStdinProvider is the SDK re-export of secure.NewStdinProvider.
func NewStdinProvider() *StdinProvider { return secure.NewStdinProvider() }

// NewCallbackProvider is the SDK re-export of secure.NewCallbackProvider.
func NewCallbackProvider(fn func(ctx context.Context, prompt string) ([]byte, error)) *CallbackProvider {
	return secure.NewCallbackProvider(fn)
}

// HardenProcess is the SDK re-export of secure.HardenProcess. Like
// SetAllocator it belongs to the BINARY that owns the process: call it once
// at startup, before anything constructs a Buffer or Secret, so that a
// debugger or a same-user process cannot read protected memory through ptrace
// or /proc/<pid>/mem. A binary with an audit trail records an outcome of
// ProcessInspectionAllowed there. cmd/qrdbd's start command is the reference
// wiring.
func HardenProcess(logger *slog.Logger) (ProcessHardening, error) {
	return secure.HardenProcess(logger)
}

// NewAllocatorWithinRLimit is the SDK re-export of
// enclave.NewAllocatorWithinRLimit. It builds a locked-memory budget of
// maxBytes, refusing one that exceeds the process's discovered
// RLIMIT_MEMLOCK ceiling at construction rather than at the first allocation
// under load. This is the usual way to build the budget SetAllocator
// installs.
func NewAllocatorWithinRLimit(maxBytes int64) (*Allocator, error) {
	return enclave.NewAllocatorWithinRLimit(maxBytes)
}

// SetAllocator installs a as the process-wide locked-memory budget every
// Buffer allocation and every Secret.Open in the process admits against.
// Sealing a Secret is not admitted against it — see Allocator's documentation
// for the gap and why it is scoped that way.
//
// It belongs to the BINARY, called once at startup — before anything in the
// process constructs a Buffer or Secret — as cmd/qrdbd/start.go's runStart
// does. It is one-shot and fails closed with ErrAllocatorInstalled if a process
// allocator already exists, including the default that a library's first
// ProcessAllocator call installs lazily. Library code in this repository
// never calls it; see pkg/secure's package documentation.
func SetAllocator(a *Allocator) error { return enclave.SetAllocator(a) }

// ProcessAllocator returns the process-wide locked-memory budget SetAllocator
// installed, or a default sized to the discovered RLIMIT_MEMLOCK ceiling if
// nothing was installed. This is what pkg/secure's own constructors call;
// it is re-exported so a caller that wants to allocate go-enclave memory
// directly, outside a Buffer or Secret, admits against the same budget.
func ProcessAllocator() (*Allocator, error) { return enclave.ProcessAllocator() }

// WipeAll destroys every live locked-memory enclave the process has
// allocated through go-enclave — every outstanding Buffer, every plaintext a
// Secret.Open call has transiently produced, and any SDK consumer's own
// enclave — wiping and unmapping their pages.
//
// A Secret's SEALED form is not one of those pages: a SealedEnclave's
// ciphertext and wrapped-key blob live on the ordinary (unlocked) heap by
// design, so this call does not zero them directly. What it does instead is
// destroy the process Sealer's own key material — itself a locked-memory
// enclave on the same registry — so every live Secret becomes permanently
// unopenable (Secret.Open subsequently fails with enclave.ErrSealerWiped)
// from the moment this returns, without its ciphertext bytes being wiped.
// Zeroing those is Secret.Destroy's job; call it explicitly rather than
// relying on this as a substitute.
//
// It belongs to the BINARY, called on orderly shutdown (deferred at startup,
// as cmd/qrdbd/start.go's runStart does) so the process's mlocked memory is
// swept on every exit path. Library code in this repository never calls it.
//
// ctx bounds the sweep: it is waited for while a borrow is held, while a
// contended lock is retried, and while admission for a concurrent sweep is
// queued. Give a shutdown sweep a deadline taken at shutdown rather than
// context.Background(), which waits as long as the borrows it owes take.
func WipeAll(ctx context.Context) (int, error) { return enclave.WipeAll(ctx) }
