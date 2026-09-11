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

// The borrow guard turns the ONE failure mode this package cannot report into
// one a test can catch.
//
// purgeMu is an RWMutex and Go's RWMutex is not reentrant. A goroutine holding
// a borrow (shared) that reaches a purge-class operation (exclusive) blocks on
// itself, forever, with no error, no panic and no stack — the daemon simply
// stops. The only symptom is a CI job that hangs until its wall-clock limit
// and reports a timeout naming whichever test happened to be running.
//
// That symptom is useless for the thing it is a symptom of, and the hazard is
// not hypothetical: it is why converting a Buffer.Bytes() reader to WithBytes
// cannot be mechanical (go-qrdb#74). Whether a callback is safe is a question
// about everything the callback can reach, transitively, through interfaces
// the caller does not own.
//
// So the guard makes the violation LOUD instead of silent. While it is armed,
// every purge-class and borrow-class entry point asks whether THIS goroutine
// already holds a borrow, and reports a *NestedBorrowError before taking the
// lock that would wedge. A bad conversion then fails one test with both stacks
// attached, rather than hanging the suite.
//
// # Why goroutine identity, and what it costs
//
// "Does the current goroutine hold a borrow" is not answerable from the mutex:
// RWMutex does not name its readers, and borrows from OTHER goroutines are
// legitimate and concurrent. Nothing but goroutine identity distinguishes the
// two, and Go exposes it only through runtime.Stack's header.
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

// borrowFrame records how deep the owning goroutine's borrows are nested and
// where the outermost one was taken. The stack is captured once, at the outer
// borrow, because that is the frame a violation report needs and the inner
// one is already on the reporting goroutine's live stack.
type borrowFrame struct {
	depth int
	stack []byte
}

// BorrowViolationFunc is called when an armed guard catches a goroutine
// entering a purge-class or borrow-class operation while it already holds a
// borrow.
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
// While armed, a goroutine that already holds a borrow and then enters any
// purge-class operation (NewBuffer, NewBufferFromBytes, NewSecret,
// NewSecretFromBuffer, Secret.Open, either Destroy, Purge) or any further
// borrow-class operation (Buffer.Bytes, Buffer.Size, Buffer.WithBytes) is
// reported to onViolation with a *NestedBorrowError, BEFORE the lock that
// would wedge is taken.
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

// guardEnterBorrow records that the current goroutine has taken a borrow, and
// reports a violation first if it already held one.
//
// Called only with the guard armed; the caller does the atomic check so the
// disarmed path pays nothing beyond it.
func guardEnterBorrow(op string) {
	id, ok := goroutineID()
	if !ok {
		return
	}
	if v, loaded := borrowFrames.Load(id); loaded {
		// reportBorrowViolation does not return, so there is no depth to
		// increment past it: a second borrow on this goroutine IS the
		// violation, not a state worth recording.
		if f, isFrame := v.(*borrowFrame); isFrame && f.depth > 0 {
			reportBorrowViolation(op, f.stack)
		}
	}
	borrowFrames.Store(id, &borrowFrame{depth: 1, stack: currentStack()})
}

// guardExitBorrow undoes guardEnterBorrow. Unbalanced calls — a borrow taken
// before the guard was armed and released after — are ignored rather than
// driven negative.
func guardExitBorrow() {
	id, ok := goroutineID()
	if !ok {
		return
	}
	v, loaded := borrowFrames.Load(id)
	if !loaded {
		return
	}
	f, isFrame := v.(*borrowFrame)
	if !isFrame {
		return
	}
	f.depth--
	if f.depth <= 0 {
		borrowFrames.Delete(id)
	}
}

// guardAssertNoBorrow reports a violation when the current goroutine enters a
// purge-class operation while holding a borrow. Called before the exclusive
// lock, which is the whole point — after it, there is nothing left to report
// from.
func guardAssertNoBorrow(op string) {
	id, ok := goroutineID()
	if !ok {
		return
	}
	v, loaded := borrowFrames.Load(id)
	if !loaded {
		return
	}
	if f, isFrame := v.(*borrowFrame); isFrame && f.depth > 0 {
		reportBorrowViolation(op, f.stack)
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
// converted call site in go-qrdb#74 needs the same three things and gets them
// wrong in the same three ways otherwise:
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
// deadlock would leave the purge lock held for the life of the binary and every
// later test blocked behind it, which is the failure this replaces.
func GuardBorrows(report func(format string, args ...any)) (stop func()) {
	var caught atomic.Pointer[NestedBorrowError]
	restore := EnableBorrowGuard(func(e *NestedBorrowError) {
		caught.CompareAndSwap(nil, e)
		runtime.Goexit()
	})
	return func() {
		restore()
		if e := caught.Load(); e != nil && report != nil {
			report("secure: this path took a NESTED BORROW, which deadlocks "+
				"outside the guard: %v", e)
		}
	}
}
