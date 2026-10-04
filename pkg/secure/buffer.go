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
	"errors"
	"log/slog"
	"sync/atomic"

	"github.com/jeremyhahn/go-enclave"
)

// Buffer is a short-lived, mlocked, canary-guarded working copy of
// sensitive bytes. It wraps enclave.MemoryEnclave from go-enclave, the
// portfolio's protected-memory library (go-qrdb#114, go-qrdb#553).
//
// Buffers MUST be destroyed when no longer needed — callers are expected
// to defer Destroy() immediately after construction. The zero value is
// unusable; callers must use one of the provided constructors.
//
// Thread safety: Destroy is safe to call concurrently with itself (it is
// idempotent via atomic CAS). However, Bytes() / Size() / Wipe() while
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
// Buffer's read/write borrow is enclave.MemoryEnclave's own, per-instance
// lock.
type Buffer struct {
	enc       *enclave.MemoryEnclave
	destroyed atomic.Bool
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
func processAllocator() (*enclave.Allocator, error) {
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
	checkNoBorrow("NewBuffer")
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
// go-enclave's New (and the unadmitted NewMemoryEnclave it shares its move
// contract with) FREEZES the resulting pages read-only — deliberate, and a
// real hardening: a write through a retained slice faults instead of
// silently corrupting the enclave. But this package's Buffer has always been
// uniformly writable regardless of which constructor built it — the fill-in
// -place pattern NewBuffer exists for depends on it, and Bytes/WithBytes
// document mutation as sometimes intended — so freezing only the
// NewBufferFromBytes half would make a write through Bytes()/WithBytes()
// SIGSEGV the process depending on which constructor happened to build the
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
	checkNoBorrow("NewBufferFromBytes")
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
		if derr := enc.Destroy(); derr != nil {
			slog.Warn("secure: releasing a failed NewBufferFromBytes allocation reported a non-fatal problem",
				slog.String("error", derr.Error()))
		}
		return nil, werr
	}
	enclave.Wipe(src)
	return &Buffer{enc: enc}, nil
}

// Bytes returns the underlying mlocked byte slice. The caller MUST NOT
// retain this slice beyond the lifetime of the Buffer, MUST NOT copy
// its contents into unprotected memory, and MUST NOT mutate it unless
// explicitly intended. Returns nil after Destroy.
//
// # This method cannot protect the slice it returns
//
// The returned slice aliases the enclave's locked pages and is NOT covered
// by any lock once Bytes has returned: a concurrent Destroy on THIS Buffer
// unmaps those pages, and a read through the stale slice then faults the
// process rather than returning an error (go-qrdb#71).
//
// Use WithBytes for anything that must be safe against that — or CopyOut,
// when the calling signature forces a []byte. Either holds the borrow for
// the whole read, which is the only shape a lock can cover.
//
// # What Bytes is still FOR
//
// Not "everything not yet converted". The remaining callers are reads whose
// borrow cannot be held at all, and they fall into three kinds:
//
//   - injected-interface: the material crosses into an INJECTED INTERFACE —
//     seal.EncryptionBarrier, seal.SealingStrategy, ceremony's BarrierUnseal —
//     whose implementations are supplied by consumers. Any conforming one may
//     allocate protected memory of its own, which no longer deadlocks against
//     a held borrow (go-qrdb#114 removed the process-wide lock that made it),
//     but rule 6 still forbids it, and the migration for these sites is
//     tracked separately (go-qrdb#145);
//   - allocates-protected: the read spans a call in this repository that
//     allocates protected memory of its own, such as a strategy constructor
//     copying the key into its own enclave — rule 6 again, with a callee that
//     can be named;
//   - nested-enclave: the read happens inside a go-quicraft enclave borrow,
//     whose own WithBytes documentation forbids nesting a second
//     protected-memory borrow inside it.
//
// A call site using Bytes for any OTHER reason is a defect. Each surviving
// one carries a directive naming its kind and reason, trailing the read or
// standing alone on the line above it:
//
//	//secure:unborrowed injected-interface m.barrier is a seal.EncryptionBarrier
//
// pkg/secure/analyzer's rule 4 reports any reference to Bytes outside a test
// file that lacks one (go-qrdb#428).
func (b *Buffer) Bytes() []byte {
	if b == nil || b.destroyed.Load() {
		return nil
	}
	enterBorrow("Buffer.Bytes")
	defer exitBorrow()
	out, err := b.enc.Open()
	if err != nil {
		return nil
	}
	return out
}

// WithBytes calls fn with the buffer's mlocked bytes, holding the borrow for
// the whole call.
//
// This is the borrow with an actual guarantee, and the reason to prefer it
// to Bytes: only a borrow held across the READ — not merely across the
// accessor — keeps THIS buffer's pages mapped for the whole time fn touches
// them, so a concurrent Destroy on the same Buffer waits rather than
// unmapping underneath the reader.
//
// fn MUST NOT retain the slice beyond the call, and MUST NOT call this
// Buffer's own methods reentrantly: enclave.MemoryEnclave's lock is not
// reentrant, so a nested WithBytes/Destroy on the SAME buffer from inside its
// own callback deadlocks. Calling into a DIFFERENT Buffer or Secret from
// inside the callback is safe — each holds its own independent lock — but is
// still discouraged style; see the borrow guard in borrowguard.go, which
// flags any nested call into this package for a test to catch.
//
// Returns *NilBufferError when the buffer is nil, destroyed, or a zero value.
// Any error fn returns is passed through unchanged.
func (b *Buffer) WithBytes(fn func([]byte) error) error {
	if b == nil || fn == nil || b.destroyed.Load() {
		return &NilBufferError{}
	}
	enterBorrow("Buffer.WithBytes")
	defer exitBorrow()
	return nilBufferIfGone(b.enc.WithBytes(fn))
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
// after Destroy or on a zero-value Buffer.
func (b *Buffer) Size() int {
	if b == nil || b.destroyed.Load() {
		return 0
	}
	enterBorrow("Buffer.Size")
	defer exitBorrow()
	return b.enc.Size()
}

// Destroy wipes and releases the mlocked memory, returning its reservation to
// the process allocator. Safe to call multiple times — subsequent calls are
// no-ops. Safe to call on a nil receiver.
//
// Any non-fatal release problem go-enclave reports is DISCARDED here — this
// is the deferable form, matching enclave.MemoryEnclave.Destroy's own
// contract exactly, and a deferred call has nowhere to hand an error to. Use
// DestroyErr to observe it. A detected canary corruption is not a discarded
// case: it PANICS, from underneath this call exactly as it does from
// DestroyErr, because that is a state a caller cannot act on either way (see
// DestroyErr).
func (b *Buffer) Destroy() {
	_ = b.DestroyErr()
}

// DestroyErr is Destroy with go-enclave's non-fatal release report kept
// instead of discarded.
//
// It returns enclave.ErrReleasedWithoutWipe when the environment denied the
// mprotect that makes a frozen region writable again before wiping it, so the
// pages were unmapped WITHOUT this package's in-process zeroing — nothing
// leaked to another process either way (the kernel zeroes an anonymous page
// before reuse), but the defense-in-depth wipe this package promises did not
// run, which is worth a caller's own logging or metrics rather than silently
// dropping.
//
// It returns nil for an ordinary release, and for a nil or already-destroyed
// Buffer. Like the underlying enclave.MemoryEnclave.Destroy, it PANICS if
// the canary guarding the enclave failed to verify: that means memory this
// package did not own was written, and the state a caller would use to
// recover is the state that is already wrong.
//
// The ErrReleasedWithoutWipe branch is delegated to
// enclave.MemoryEnclave.Destroy rather than reproduced here, and go-qrdb's
// own test suite cannot force it: triggering a denied mprotect is exercised
// only from INSIDE go-enclave's own package-internal tests (unexported
// syscall seams), and reproducing it from this package would mean widening
// b.enc from the concrete *enclave.MemoryEnclave to an interface purely to
// inject a fake — an architecture change this delegation deliberately keeps
// out of scope (go-qrdb#114 review, round 2: tracked as a known gap rather
// than papered over with a seam that exists for one test).
//
// An abandoned attempt does NOT leave b permanently marked destroyed. The CAS
// below flips b.destroyed to true before the release runs, which is right for
// the ordinary concurrent-Destroy case (only one caller proceeds; the rest see
// nil immediately) but would be wrong for any attempt that never reaches the
// end of b.enc.Destroy: a go-enclave panic after a failed unlock or unmap,
// or an armed borrow guard's violation handler panicking or calling
// runtime.Goexit from checkNoBorrow before the release even starts. Without
// the reset, a caller that recovers and retries would find b.destroyed already
// true and get a false "nil, already destroyed" for a release that never
// completed, while the mapping stays live and registered (go-qrdb#114 review,
// round 4, non-blocking). The reset is keyed on completion rather than on
// recover(), so it covers the checkNoBorrow window as well as the release
// itself, runs on runtime.Goexit (where recover returns nil), and lets the
// original panic propagate with its own stack instead of a re-panic.
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
	checkNoBorrow("Buffer.Destroy")
	err := b.enc.Destroy()
	completed = true
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
