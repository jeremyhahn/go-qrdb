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

package secure

import (
	"crypto/subtle"
	"errors"
	"log/slog"
	"sync/atomic"

	"github.com/jeremyhahn/go-enclave"
)

// Buffer is a short-lived, mlocked, canary-guarded working copy of
// sensitive bytes. It wraps a go-enclave memory enclave (enclave.ByteAccess), the
// portfolio's protected-memory library (go-qrdb#114, go-qrdb#553).
//
// Buffers MUST be destroyed when no longer needed — callers are expected
// to defer Destroy() immediately after construction. The zero value is
// unusable; callers must use one of the provided constructors.
//
// Thread safety: Destroy is safe to call concurrently with itself (it is
// idempotent via atomic CAS). However, WithBytes() / Size() / Wipe() while
// another goroutine is calling Destroy() is undefined and must be
// avoided — Buffer is intended to be single-owner for its working life.
//
// # Why this is not raw memguard (go-qrdb#114)
//
// memguard aborted the whole process when it could not lock pages —
// typically at an RLIMIT_MEMLOCK ceiling — by calling core.Panic, which
// first purged every live enclave in the process. This package used to call
// memguard directly with no recover, so a locked-memory ceiling killed the
// daemon, and the purge forced a process-wide lock (purgeMu) that every
// borrow and every allocation had to serialize on to survive it.
//
// go-enclave maps its own pages and has no process-wide purge: a failed
// mlock returns enclave.ErrLockedMemoryUnavailable, admission against
// enclave.ProcessAllocator refuses with enclave.ErrBudgetExhausted before any
// syscall is made, and one enclave's Destroy affects only its own mapping.
// Nothing in this package needs to lock across allocations any more; each
// Buffer's read/write borrow is its memory enclave's own, per-instance
// lock.
type Buffer struct {
	enc       enclave.ByteAccess
	destroyed atomic.Bool

	// empty marks the explicit live zero-length value NewEmptyBuffer
	// returns. go-enclave refuses a zero-length mapping, so an empty Buffer
	// holds no enclave at all: enc is nil and the value lives until Destroy.
	// It is set at construction and never changes.
	empty bool
}

// NewEmptyBuffer returns a live Buffer that holds zero bytes.
//
// A zero length is a legitimate value — an empty passphrase, an absent
// optional credential — and it must stay distinguishable from a destroyed
// Buffer, which Size alone cannot do: both report 0. IsAlive reports true for
// this value until Destroy, WithBytes calls its callback with an empty slice,
// and Equal and EqualBytes compare it like any other value.
//
// NewBuffer and NewBufferFromBytes still refuse a zero length with
// *InvalidSizeError: an empty value is asked for by name, never produced by a
// size computation that came out zero. It maps no pages and admits nothing
// against the process locked-memory budget, so it cannot fail, and a shutdown
// enclave.WipeAll has nothing of it to wipe. CopyOut refuses it with
// *EmptyBorrowError, as it refuses every empty borrow.
func NewEmptyBuffer() *Buffer {
	return &Buffer{empty: true}
}

// hollow reports whether b is the zero Buffer: no enclave and not the empty
// value. It holds nothing and reads as destroyed. go-enclave hands out its
// memory enclaves as the enclave.ByteAccess interface, whose nil value has no
// methods to call, so every use of enc checks this first.
func (b *Buffer) hollow() bool {
	return b.enc == nil && !b.empty
}

// wrapAllocError translates go-enclave's construction errors into this
// package's vocabulary.
//
// enclave.ErrInvalidSize becomes *InvalidSizeError so existing callers that
// type-switch on it are unaffected. Everything else — chiefly
// enclave.ErrBudgetExhausted (the process-wide locked-memory budget refusing
// an admission) and enclave.ErrLockedMemoryUnavailable (the kernel refusing
// mlock despite admission) — is returned unchanged, so callers can match it
// with errors.Is against the go-enclave sentinel. Both paths previously did
// not return at all: they purged the process and aborted it.
func wrapAllocError(size int, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, enclave.ErrInvalidSize) {
		return &InvalidSizeError{Size: size}
	}
	return err
}

// LogRelease records a problem go-enclave reported while releasing what, and
// is the one way go-qrdb handles the error from a go-enclave Destroy whose
// caller has nowhere to return it (a deferred release, or a Destroy with no
// result of its own). A nil err logs nothing. A corrupted canary or guard is
// logged at ERROR: memory the enclave did not own was written. Anything else
// is logged at WARN: the material was wiped, or the release is still on
// go-enclave's ledger for a retry or the shutdown sweep to finish, so the
// caller has nothing further to do about it.
func LogRelease(what string, err error) {
	if err == nil {
		return
	}
	attrs := []any{slog.String("what", what), slog.String("error", err.Error())}
	if errors.Is(err, enclave.ErrEnclaveCorrupted) {
		slog.Error("secure: protected memory was corrupted before its release", attrs...)
		return
	}
	slog.Warn("secure: releasing protected memory reported a problem", attrs...)
}

// processAllocator returns the shared, process-wide locked-memory budget
// every Buffer admits against.
//
// It is enclave.ProcessAllocator: the binary sizes and installs the budget
// once at startup (sdk/go/secure.SetAllocator, wired from
// enclave.NewAllocatorWithinRLimit), and this package — a library, never the
// process owner — only ever calls ProcessAllocator to reach it. With nothing
// installed, the first call lazily installs a default sized to the
// discovered RLIMIT_MEMLOCK ceiling, so an unconfigured process is still
// bounded rather than unlimited.
func processAllocator() (enclave.Allocator, error) {
	return enclave.ProcessAllocator()
}

// NewBuffer allocates a zero-filled Buffer of size bytes, admitted against
// the process-wide locked-memory budget (see processAllocator). The memory
// is locked (mlock), guard-paged on both sides, and zero-wiped on destroy.
// Returns an *InvalidSizeError when size <= 0.
//
// The pages stay WRITABLE for the buffer's lifetime, which is what makes the
// fill-in-place pattern possible — generating a key straight into locked
// memory rather than via the heap. A locked-memory ceiling or an exhausted
// budget now returns a typed error instead of killing the process.
func NewBuffer(size int) (*Buffer, error) {
	if size <= 0 {
		return nil, &InvalidSizeError{Size: size}
	}
	a, err := processAllocator()
	if err != nil {
		return nil, err
	}
	enc, err := a.NewSized(size)
	if err != nil {
		return nil, wrapAllocError(size, err)
	}
	return &Buffer{enc: enc}, nil
}

// NewBufferFromBytes creates a Buffer by moving src into mlocked memory,
// admitted against the process-wide locked-memory budget. On return, src is
// wiped — the caller must not reuse it, on every return path including
// failure. Nil or empty src returns (nil, *InvalidSizeError).
//
// # Why this does not use enclave.Allocator.New directly
//
// go-enclave's New (and the unadmitted NewMemory it shares its move
// contract with) FREEZES the resulting pages read-only — deliberate, and a
// real hardening: a write through a retained slice faults instead of
// silently corrupting the enclave. But this package's Buffer has always been
// uniformly writable regardless of which constructor built it — the fill-in
// -place pattern NewBuffer exists for depends on it, and WithBytes documents
// mutation as sometimes intended — so freezing only the NewBufferFromBytes
// half would make a write through WithBytes() SIGSEGV the process depending on which constructor happened to build the
// Buffer being written to, which is exactly the kind of landmine a caller has
// no way to see coming.
//
// So this goes through the same admitted, WRITABLE sized allocation NewBuffer
// itself uses, and copies src in rather than moving it by construction. src
// is still wiped on every return path, including failure — the ownership
// contract this function documents — it is simply this function wiping it
// with enclave.Wipe instead of the allocator doing so as part of the move.
func NewBufferFromBytes(src []byte) (*Buffer, error) {
	if len(src) == 0 {
		return nil, &InvalidSizeError{Size: len(src)}
	}
	a, err := processAllocator()
	if err != nil {
		enclave.Wipe(src)
		return nil, err
	}
	enc, err := a.NewSized(len(src))
	if err != nil {
		enclave.Wipe(src)
		return nil, wrapAllocError(len(src), err)
	}
	if werr := enc.WithBytes(func(dst []byte) error {
		copy(dst, src)
		return nil
	}); werr != nil {
		enclave.Wipe(src)
		LogRelease("failed NewBufferFromBytes allocation", enc.Destroy())
		return nil, werr
	}
	enclave.Wipe(src)
	return &Buffer{enc: enc}, nil
}

// WithBytes calls fn with the buffer's mlocked bytes, holding the borrow for
// the whole call.
//
// It is the only way to read a Buffer: go-enclave scopes every borrow to a
// callback, and a borrow held across the READ — not merely across an
// accessor — keeps THIS buffer's pages mapped for the whole time fn touches
// them, so a concurrent Destroy on the same Buffer waits rather than
// unmapping underneath the reader. A caller whose signature forces a []byte
// that outlives the callback uses CopyOut and wipes the copy when done.
//
// fn MUST NOT retain the slice beyond the call, and MUST NOT call this
// Buffer's own methods reentrantly: the memory enclave's lock is not
// reentrant, so a nested WithBytes/Size/Destroy on the SAME buffer from inside
// its own callback deadlocks. Anything on a DIFFERENT Buffer or Secret —
// allocating, opening, borrowing, destroying — is safe from inside the
// callback, because each holds its own independent lock; that is what lets a
// borrow be held across a barrier, KEK or verifier that uses protected memory
// itself. The borrow guard in borrowguard.go catches the same-buffer reentry
// for a test.
//
// Returns *NilBufferError when the buffer is nil, destroyed, or a zero value.
// Any error fn returns is passed through unchanged. The empty Buffer
// NewEmptyBuffer returns calls fn with a zero-length slice.
func (b *Buffer) WithBytes(fn func([]byte) error) error {
	if b == nil || fn == nil || b.destroyed.Load() || b.hollow() {
		return &NilBufferError{}
	}
	enterBorrow("Buffer.WithBytes", b)
	defer exitBorrow(b)
	if b.empty {
		return fn([]byte{})
	}
	return nilBufferIfGone(b.enc.WithBytes(fn))
}

// IsAlive reports whether b still holds its value: true from construction
// until Destroy, including for the empty Buffer NewEmptyBuffer returns, and
// false for a nil, destroyed or zero-value Buffer, or one whose pages a
// shutdown enclave.WipeAll has already taken.
//
// It is the check Size cannot be: a live empty Buffer and a destroyed one
// both report a Size of 0.
func (b *Buffer) IsAlive() bool {
	if b == nil || b.destroyed.Load() || b.hollow() {
		return false
	}
	if b.empty {
		return true
	}
	enterBorrow("Buffer.IsAlive", b)
	defer exitBorrow(b)
	return b.enc.IsAlive()
}

// EqualBytes reports, in constant time, whether b holds exactly p. The
// comparison is crypto/subtle.ConstantTimeCompare, with b's borrow held
// across it: the timing reveals whether the lengths differ, never where the
// contents do. An empty p equals the empty Buffer.
//
// A nil, destroyed or zero-value b returns (false, *NilBufferError), so a
// caller can tell "not equal" from "nothing left to compare".
func (b *Buffer) EqualBytes(p []byte) (bool, error) {
	equal := false
	if err := b.WithBytes(func(v []byte) error {
		equal = subtle.ConstantTimeCompare(v, p) == 1
		return nil
	}); err != nil {
		return false, err
	}
	return equal, nil
}

// Equal reports, in constant time, whether b and o hold the same bytes. Both
// borrows are held across one crypto/subtle comparison, so neither value is
// copied out of locked memory; the timing reveals whether the lengths differ,
// never where the contents do. A Buffer equals itself while it is alive, and
// two empty Buffers are equal.
//
// A nil, destroyed or zero-value b or o returns (false, *NilBufferError).
//
// The two borrows are read borrows of two independent enclaves, so two
// goroutines comparing the same pair in opposite orders do not contend; the
// one way to wedge them is a Destroy racing a borrow on the same Buffer,
// which Buffer's single-owner contract already forbids.
func (b *Buffer) Equal(o *Buffer) (bool, error) {
	if b == o {
		if !b.IsAlive() {
			return false, &NilBufferError{}
		}
		return true, nil
	}
	equal := false
	if err := b.WithBytes(func(v []byte) error {
		return o.WithBytes(func(w []byte) error {
			equal = subtle.ConstantTimeCompare(v, w) == 1
			return nil
		})
	}); err != nil {
		return false, err
	}
	return equal, nil
}

// nilBufferIfGone maps go-enclave's "nothing to borrow" failures — the
// enclave was destroyed, or holds nothing — to this package's
// *NilBufferError, and passes every other error (fn's own included) through.
func nilBufferIfGone(err error) error {
	if errors.Is(err, enclave.ErrEnclaveDestroyed) || errors.Is(err, enclave.ErrEmptyEnclave) {
		return &NilBufferError{}
	}
	return err
}

// Size returns the length of the underlying mlocked buffer. Returns 0
// after Destroy, on a zero-value Buffer, and for the live empty Buffer
// NewEmptyBuffer returns; IsAlive tells those apart.
func (b *Buffer) Size() int {
	if b == nil || b.destroyed.Load() || b.enc == nil {
		return 0
	}
	enterBorrow("Buffer.Size", b)
	defer exitBorrow(b)
	return b.enc.Size()
}

// Destroy wipes and releases the mlocked memory, returning its reservation to
// the process allocator. Safe to call multiple times — subsequent calls are
// no-ops. Safe to call on a nil receiver.
//
// This is the deferable form: a deferred call has nowhere to hand an error to,
// so any release problem go-enclave reports is logged (see LogRelease) rather
// than returned. Use DestroyErr to observe it.
func (b *Buffer) Destroy() {
	LogRelease("buffer", b.DestroyErr())
}

// DestroyErr is Destroy with go-enclave's release report returned instead of
// logged. A failed release never panics; it returns nil for an ordinary
// release and for a
// nil or already-destroyed Buffer. Otherwise it returns what
// the memory enclave's Destroy reported:
//
//   - enclave.ErrReleasedWithoutWipe: the environment denied the mprotect that
//     makes a frozen region writable again before wiping it, so the pages were
//     unmapped WITHOUT the in-process zeroing. Nothing leaked to another
//     process (the kernel zeroes an anonymous page before reuse), but the
//     defense-in-depth wipe did not run.
//   - enclave.ErrLockedMemoryUnavailable: the kernel refused munlock or
//     munmap. The material is wiped and reads as destroyed, but the pages are
//     still held; b stays retryable, so a later DestroyErr resumes the release.
//   - enclave.ErrEnclaveCorrupted: the canary or guards were overwritten, so
//     memory this package did not own was written. The pages are still wiped
//     and released.
//
// go-qrdb's own test suite cannot force any of these: they are exercised only
// from INSIDE go-enclave's package-internal tests (unexported syscall seams),
// and b.enc, though an enclave.ByteAccess interface since go-enclave
// v0.9.0-alpha, is always built by go-enclave: a fake injected only to reach
// these paths would test the fake (go-qrdb#114 review, round 2: tracked as a
// known gap rather than papered over with a seam that exists for one test).
//
// An attempt that does not finish does NOT leave b permanently marked
// destroyed. The CAS below flips b.destroyed to true before the release runs,
// which is right for the ordinary concurrent-Destroy case (only one caller
// proceeds; the rest see nil immediately) but would be wrong for any attempt
// that does not complete the release: a refused munlock or munmap, or an armed
// borrow guard's violation handler panicking or calling runtime.Goexit from
// checkNotBorrowed before the release even starts. Without the reset, a caller
// that retries would find b.destroyed already true and get a false "nil,
// already destroyed" for a release that never completed, while the mapping
// stays live and registered (go-qrdb#114 review, round 4, non-blocking). The
// reset is keyed on completion rather than on recover(), so it covers the
// checkNotBorrowed window as well as the release itself, runs on runtime.Goexit
// (where recover returns nil), and lets an original panic propagate with its
// own stack instead of a re-panic.
func (b *Buffer) DestroyErr() error {
	if b == nil {
		return nil
	}
	if !b.destroyed.CompareAndSwap(false, true) {
		return nil
	}
	completed := false
	defer func() {
		if !completed {
			b.destroyed.Store(false)
		}
	}()
	checkNotBorrowed("Buffer.Destroy", b)
	if b.enc == nil {
		completed = true
		return nil
	}
	err := b.enc.Destroy()
	completed = !errors.Is(err, enclave.ErrLockedMemoryUnavailable)
	return err
}

// Wipe zeroes a caller-owned []byte using go-enclave's
// compiler-optimization-resistant wipe. Useful at the boundary where a
// caller holds a raw slice that must be cleared before returning (e.g.,
// after copying its contents into a Buffer). Nil or empty slices are
// no-ops.
func Wipe(b []byte) {
	if len(b) == 0 {
		return
	}
	enclave.Wipe(b)
}
