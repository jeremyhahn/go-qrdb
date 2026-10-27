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
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
)

// The borrow guard turns a failure mode this package cannot always report
// into one a test can catch.
//
// Each memory enclave carries its own read/write lock, and a Buffer's
// borrow (WithBytes, Size) holds that lock for reading. The one way to deadlock
// on it is the hazard every sync.RWMutex has: the goroutine holding a Buffer's
// borrow reenters THAT Buffer — Destroy needs the lock for writing, and a
// second borrow waits behind any writer already queued — and blocks on a lock
// only it could release. There is no error, no panic and no stack; the
// goroutine simply stops.
//
// Nothing else nests badly. Allocating, opening, borrowing or destroying a
// DIFFERENT Buffer or Secret from inside a borrow takes a different lock, so
// it cannot contend with the borrow held (go-qrdb#145). That is what lets a
// borrow be held across a callee the caller does not own — a barrier, a KEK
// provider, a verifier — which may use protected memory of its own: the rule
// is about the borrowed object, not about which package the callee reaches.
//
// What makes the remaining hazard worth a detector is that whether a callback
// can reach the borrowed Buffer is a question about everything it can reach,
// transitively, through interfaces the caller does not own (go-qrdb#74). So
// the guard makes a violation LOUD instead of silent. While it is armed, every
// entry point that borrows or destroys a Buffer asks whether THIS goroutine is
// already borrowing THAT Buffer, and reports a *NestedBorrowError rather than
// proceeding into the self-deadlock. A bad conversion then fails one test with
// both stacks attached, rather than hanging the suite.
//
// # Why goroutine identity, and what it costs
//
// "Does the current goroutine hold this borrow" is not answerable from the
// mutex: RWMutex does not name its readers, and borrows of the same Buffer
// from OTHER goroutines are legitimate and concurrent. Nothing but goroutine
// identity distinguishes the two, and Go exposes it only through
// runtime.Stack's header.
//
// That is far too expensive for the DEK read path, so the guard is opt-in and
// off by default. Measured on this tree, a Buffer.WithBytes round trip is
// 19 ns disarmed and 69 µs armed — three and a half thousand times, because
// arming adds a goroutine-ID lookup and a stack capture per borrow. Disarmed
// the whole cost is the one atomic load below. This is a TEST facility and the
// production default is the disarmed one.
//
// It is compiled into the ORDINARY build, with no build tag, on purpose: the
// code it instruments is production code, and packages outside this one
// (pkg/seal, pkg/dek, pkg/ceremony, pkg/credentials) must be able to arm it
// from their own tests.

// borrowGuardArmed reports whether the guard is on. Read on every instrumented
// operation, so it is an atomic rather than anything requiring a lock.
var borrowGuardArmed atomic.Bool

// checkNotBorrowed, enterBorrow and exitBorrow are the ONLY places this
// package touches the borrow-guard bookkeeping, so the guard hook has exactly
// one home per call shape. checkNotBorrowed marks an operation that needs the
// Buffer's lock for writing or that borrows it internally (Destroy,
// NewSecretFromBuffer); enterBorrow/exitBorrow bracket a borrow (Size,
// WithBytes).

// checkNotBorrowed reports (if the guard is armed) a violation when the
// calling goroutine is already borrowing b.
func checkNotBorrowed(op string, b *Buffer) {
	if borrowGuardArmed.Load() {
		guardAssertNotBorrowed(op, b)
	}
}

// enterBorrow records (if the guard is armed) that the calling goroutine has
// borrowed b, reporting a violation first if it was already borrowing b.
func enterBorrow(op string, b *Buffer) {
	if borrowGuardArmed.Load() {
		guardEnterBorrow(op, b)
	}
}

// exitBorrow undoes what enterBorrow recorded for b, if the guard is armed.
func exitBorrow(b *Buffer) {
	if borrowGuardArmed.Load() {
		guardExitBorrow(b)
	}
}

// borrowViolation holds the installed handler. atomic.Pointer to a func value
// rather than atomic.Value, for the same lock-free type-safe swap Secret uses.
var borrowViolation atomic.Pointer[BorrowViolationFunc]

// borrowGuardMu serializes EnableBorrowGuard against its own restore, so two
// tests arming the guard concurrently cannot interleave into a state where the
// handler belongs to one and the armed flag to the other.
var borrowGuardMu sync.Mutex

// borrowFrames maps a goroutine ID to that goroutine's *borrowFrame.
//
// Only the owning goroutine reads or writes its own frame, so the frame needs
// no lock of its own; the map needs one because different goroutines add and
// remove entries concurrently.
var borrowFrames sync.Map

// borrowFrame records the Buffers the owning goroutine is borrowing, innermost
// last. Borrows of different Buffers nest legitimately, so the frame is a
// list rather than a depth.
type borrowFrame struct {
	held []heldBorrow
}

// heldBorrow is one borrow on the owning goroutine: the Buffer and the stack
// it was taken on. The stack is captured at the borrow because that is the
// frame a violation report needs; the reentering call is already on the
// reporting goroutine's live stack.
type heldBorrow struct {
	buf   *Buffer
	stack []byte
}

// lookup returns the innermost borrow of b in f, if any.
func (f *borrowFrame) lookup(b *Buffer) (heldBorrow, bool) {
	for i := len(f.held) - 1; i >= 0; i-- {
		if f.held[i].buf == b {
			return f.held[i], true
		}
	}
	return heldBorrow{}, false
}

// BorrowViolationFunc is called when an armed guard catches a goroutine
// borrowing or destroying a Buffer it is already borrowing.
//
// It MUST NOT return. Returning would let the caller proceed into the very
// deadlock the guard just detected, so the guard panics with the same error if
// a handler returns anyway. Panicking, or runtime.Goexit (which is what
// testing.T.Fatal does), are both correct — each unwinds the deferred unlocks
// on the way out.
type BorrowViolationFunc func(*NestedBorrowError)

// EnableBorrowGuard arms nested-borrow detection process-wide and returns a
// function that restores the previous state.
//
// While armed, a goroutine that is borrowing a Buffer and then reenters THAT
// Buffer — Buffer.WithBytes, Buffer.Size, Buffer.Destroy, or
// NewSecretFromBuffer with it — is reported to onViolation with a
// *NestedBorrowError, before the call is allowed to proceed. Operations on any
// other Buffer or Secret are not violations and are not reported.
//
// A nil onViolation panics with the error, which is the right default: the
// alternative is to return and hang.
//
// This is a TEST facility. Arming it costs a runtime.Stack call per
// instrumented operation and it is not intended for production processes.
// Nested calls restore correctly, so a test that arms it may call another that
// does too.
func EnableBorrowGuard(onViolation BorrowViolationFunc) (restore func()) {
	if onViolation == nil {
		onViolation = panicOnViolation
	}
	borrowGuardMu.Lock()
	defer borrowGuardMu.Unlock()

	prevArmed := borrowGuardArmed.Load()
	prevHandler := borrowViolation.Load()

	borrowViolation.Store(&onViolation)
	borrowGuardArmed.Store(true)

	var once sync.Once
	return func() {
		once.Do(func() {
			borrowGuardMu.Lock()
			defer borrowGuardMu.Unlock()
			borrowGuardArmed.Store(prevArmed)
			borrowViolation.Store(prevHandler)
			if !prevArmed {
				// Frames recorded under this arming are meaningless once it
				// ends: their exits may already have been skipped, and a stale
				// entry would report a violation against a borrow that is long
				// over. Clearing is only correct when nothing else is armed.
				borrowFrames.Clear()
			}
		})
	}
}

// panicOnViolation is the default handler.
func panicOnViolation(err *NestedBorrowError) { panic(err) }

// currentFrame returns goroutine id's frame, or nil when it has none.
func currentFrame(id uint64) *borrowFrame {
	v, loaded := borrowFrames.Load(id)
	if !loaded {
		return nil
	}
	f, _ := v.(*borrowFrame)
	return f
}

// guardEnterBorrow records that the current goroutine has borrowed b, and
// reports a violation first if it was already borrowing b.
//
// Called only with the guard armed; the caller does the atomic check so the
// disarmed path pays nothing beyond it.
func guardEnterBorrow(op string, b *Buffer) {
	id, ok := goroutineID()
	if !ok {
		return
	}
	f := currentFrame(id)
	if f != nil {
		// reportBorrowViolation does not return, so a reentry is never
		// recorded: it IS the violation, not a state worth tracking.
		if h, held := f.lookup(b); held {
			reportBorrowViolation(op, h.stack)
		}
	} else {
		f = &borrowFrame{}
		borrowFrames.Store(id, f)
	}
	f.held = append(f.held, heldBorrow{buf: b, stack: currentStack()})
}

// guardExitBorrow undoes guardEnterBorrow for b. Unbalanced calls — a borrow
// taken before the guard was armed and released after — are ignored.
func guardExitBorrow(b *Buffer) {
	id, ok := goroutineID()
	if !ok {
		return
	}
	f := currentFrame(id)
	if f == nil {
		return
	}
	for i := len(f.held) - 1; i >= 0; i-- {
		if f.held[i].buf == b {
			f.held = append(f.held[:i], f.held[i+1:]...)
			break
		}
	}
	if len(f.held) == 0 {
		borrowFrames.Delete(id)
	}
}

// guardAssertNotBorrowed reports a violation when the current goroutine enters
// an operation on b while borrowing b. Called before the operation takes b's
// lock, which is the whole point — after it, there is nothing left to report
// from.
func guardAssertNotBorrowed(op string, b *Buffer) {
	id, ok := goroutineID()
	if !ok {
		return
	}
	if f := currentFrame(id); f != nil {
		if h, held := f.lookup(b); held {
			reportBorrowViolation(op, h.stack)
		}
	}
}

// reportBorrowViolation hands the violation to the installed handler and
// panics if the handler returns. See BorrowViolationFunc for why returning is
// not an option.
func reportBorrowViolation(op string, borrowStack []byte) {
	err := &NestedBorrowError{Op: op, BorrowStack: string(borrowStack)}
	if h := borrowViolation.Load(); h != nil && *h != nil {
		(*h)(err)
	}
	panic(err)
}

// currentStack captures the calling goroutine's stack for the violation
// report. Sized to hold the frames that matter without allocating repeatedly;
// a truncated trace still names the borrow site, which is what the report is
// for.
func currentStack() []byte {
	buf := make([]byte, 4096)
	n := runtime.Stack(buf, false)
	return buf[:n]
}

// goroutineIDPrefix is the fixed opening of runtime.Stack's header line,
// "goroutine 42 [running]:".
const goroutineIDPrefix = "goroutine "

// goroutineID returns the calling goroutine's ID.
//
// runtime.Stack's header is the only place the runtime exposes it. all=false
// does not stop the world, and a 64-byte buffer truncates after the header,
// which is all this reads.
func goroutineID() (uint64, bool) {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	return parseGoroutineID(buf[:n])
}

// parseGoroutineID extracts the ID from a runtime.Stack header line.
//
// Split out from goroutineID because its failure branches are unreachable
// through the runtime — the header format is fixed — and an unreachable branch
// in a detector is exactly the kind that is wrong when it finally runs. A
// false ok=true here would make two goroutines share a frame and invent a
// violation, so it fails closed to (0, false) and the guard skips tracking
// rather than reporting something it cannot substantiate.
func parseGoroutineID(header []byte) (uint64, bool) {
	if len(header) <= len(goroutineIDPrefix) {
		return 0, false
	}
	if string(header[:len(goroutineIDPrefix)]) != goroutineIDPrefix {
		return 0, false
	}
	rest := header[len(goroutineIDPrefix):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	id, err := strconv.ParseUint(string(rest[:end]), 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// GuardBorrows arms the detector for the duration of a test and returns a stop
// function that reports the first violation caught, if any.
//
// It is the one-line form of EnableBorrowGuard, and it exists because every
// site that holds a borrow across a callee (go-qrdb#74, go-qrdb#145) needs the
// same three things and gets them wrong in the same three ways otherwise:
//
//	func TestSomething(t *testing.T) {
//		defer secure.GuardBorrows(t.Fatalf)()
//		...drive the converted path...
//	}
//
// report is t.Fatalf or t.Errorf; nothing here imports testing.
//
// DEFERRED, not called at the end. A violation ends the goroutine that raised
// it — including the test's own, when the converted path runs on it — so a stop
// call written as the last statement of the test is simply never reached, and
// the run reports an unexplained Goexit instead of the deadlock. Deferred, the
// report happens on the way out.
//
// The violating goroutine is ended rather than left to proceed, so its deferred
// unlocks run and the process stays usable for the rest of the suite; a genuine
// deadlock would leave that goroutine blocked for the life of the binary and
// the test waiting on it hung, which is the failure this replaces.
func GuardBorrows(report func(format string, args ...any)) (stop func()) {
	var caught atomic.Pointer[NestedBorrowError]
	restore := EnableBorrowGuard(func(e *NestedBorrowError) {
		caught.CompareAndSwap(nil, e)
		runtime.Goexit()
	})
	return func() {
		restore()
		if e := caught.Load(); e != nil && report != nil {
			report("secure: this path reentered a Buffer it was borrowing, "+
				"which deadlocks outside the guard: %v", e)
		}
	}
}
