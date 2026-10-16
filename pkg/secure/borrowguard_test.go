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
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// childEnv names the subprocess mode for the deadlock demonstration below.
// Empty means "this process is the parent".
const childEnv = "QRDB_SECURE_BORROWGUARD_CHILD"

// recordViolation returns a handler that stores the error and ends the
// goroutine the way testing.T.Fatal does.
//
// runtime.Goexit rather than a return, because BorrowViolationFunc must not
// return: the guard panics past a handler that does, so a recording handler
// that returned would be reported as a panic rather than as the violation it
// recorded. Goexit still runs the deferred unlocks, so the borrow is released
// on the way out and the package is left usable for the next test — which a
// real deadlock would not be.
func recordViolation(dst **NestedBorrowError, done *sync.WaitGroup) BorrowViolationFunc {
	return func(err *NestedBorrowError) {
		*dst = err
		done.Done()
		runtime.Goexit()
	}
}

// Reentering the borrowed buffer from inside its own borrow must be REPORTED,
// not entered.
//
// This is the failure the guard exists for: Destroy needs buf's lock for
// writing while this goroutine holds it for reading, which
// TestBorrowGuard_TheUnguardedShapeReallyHangs shows hangs the process with no
// error, no panic and no stack.
func TestBorrowGuard_ReportsADestroyOfTheBorrowedBuffer(t *testing.T) {
	buf, err := NewBuffer(32)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	defer buf.Destroy()

	var got *NestedBorrowError
	var done sync.WaitGroup
	done.Add(1)
	restore := EnableBorrowGuard(recordViolation(&got, &done))
	defer restore()

	// On its own goroutine because the handler ends the goroutine that hit the
	// violation, which must not be the test's own.
	go func() {
		_ = buf.WithBytes(func([]byte) error {
			// The offending act. Reached from the real call sites through
			// injected interfaces, which is why reading the code is not enough.
			buf.Destroy()
			return nil
		})
	}()
	done.Wait()

	if got == nil {
		t.Fatal("no violation reported: the guard did not see the reentering Destroy")
	}
	if got.Op != "Buffer.Destroy" {
		t.Errorf("violation Op = %q, want %q", got.Op, "Buffer.Destroy")
	}
	if !strings.Contains(got.BorrowStack, "WithBytes") {
		t.Errorf("violation does not name the borrow site; BorrowStack was:\n%s", got.BorrowStack)
	}
	var typed *NestedBorrowError
	if !errors.As(error(got), &typed) {
		t.Error("NestedBorrowError does not satisfy errors.As against its own type")
	}
}

// Every entry point that reenters the borrowed buffer must be instrumented,
// not just the one the first test happens to use.
//
// The list is checked exhaustively because the failure mode of an omission is
// silence: an uninstrumented operation deadlocks exactly as before while the
// guard reports the suite green.
func TestBorrowGuard_CoversEveryReentryOfTheBorrowedBuffer(t *testing.T) {
	for _, tc := range []struct {
		op   string
		call func(*Buffer)
	}{
		{"Buffer.WithBytes", func(b *Buffer) { _ = b.WithBytes(func([]byte) error { return nil }) }},
		{"Buffer.Size", func(b *Buffer) { _ = b.Size() }},
		{"Buffer.Destroy", func(b *Buffer) { b.Destroy() }},
		{"NewSecretFromBuffer", func(b *Buffer) {
			if s, err := NewSecretFromBuffer(context.Background(), b); err == nil {
				s.Destroy()
			}
		}},
	} {
		t.Run(tc.op, func(t *testing.T) {
			outer, err := NewBuffer(16)
			if err != nil {
				t.Fatalf("NewBuffer: %v", err)
			}
			defer outer.Destroy()

			var got *NestedBorrowError
			var done sync.WaitGroup
			done.Add(1)
			restore := EnableBorrowGuard(recordViolation(&got, &done))
			defer restore()

			go func() {
				_ = outer.WithBytes(func([]byte) error {
					tc.call(outer)
					return nil
				})
			}()
			done.Wait()

			if got == nil {
				t.Fatalf("%s on the borrowed buffer reported nothing; that entry point is not instrumented", tc.op)
			}
			if got.Op != tc.op {
				t.Errorf("violation Op = %q, want %q", got.Op, tc.op)
			}
		})
	}
}

// Operations on OTHER objects from inside a borrow are legitimate
// (go-qrdb#145): each enclave has its own lock, so a callee handed the borrowed
// bytes may use protected memory of its own. The armed guard must report none
// of them, and every one must complete — this is the false-positive half that
// lets a borrow be held across a barrier, KEK or verifier.
func TestBorrowGuard_OtherObjectsInsideABorrowAreNotViolations(t *testing.T) {
	outer, err := NewBuffer(16)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	defer outer.Destroy()
	sealed, err := NewSecret(context.Background(), []byte("opened inside a borrow"))
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	defer sealed.Destroy()

	defer GuardBorrows(t.Fatalf)()
	if err := outer.WithBytes(func([]byte) error {
		other, err := NewBuffer(8)
		if err != nil {
			return err
		}
		if other.Size() != 8 {
			t.Errorf("other.Size = %d, want 8", other.Size())
		}
		if err := other.WithBytes(func(b []byte) error {
			copy(b, "abcdefgh")
			return nil
		}); err != nil {
			return err
		}
		fromBuf, err := NewSecretFromBuffer(context.Background(), other)
		if err != nil {
			return err
		}
		fromBuf.Destroy()
		heap, err := NewBufferFromBytes([]byte("abcdefgh"))
		if err != nil {
			return err
		}
		heap.Destroy()
		opened, err := sealed.Open(context.Background())
		if err != nil {
			return err
		}
		opened.Destroy()
		fresh, err := NewSecret(context.Background(), []byte("sealed inside a borrow"))
		if err != nil {
			return err
		}
		fresh.Destroy()
		return nil
	}); err != nil {
		t.Fatalf("WithBytes: %v", err)
	}
}

// A borrow of the same buffer taken while another is live on the same
// goroutine is a violation too, and for a subtler reason than Destroy: RWMutex
// admits no NEW reader once a writer is queued, so the inner borrow waits for a
// Destroy that is itself waiting for the outer borrow.
func TestBorrowGuard_ReportsANestedBorrow(t *testing.T) {
	buf, err := NewBuffer(32)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	defer buf.Destroy()

	var got *NestedBorrowError
	var done sync.WaitGroup
	done.Add(1)
	restore := EnableBorrowGuard(recordViolation(&got, &done))
	defer restore()

	go func() {
		_ = buf.WithBytes(func([]byte) error {
			_ = buf.Size() // borrow-class, nested
			return nil
		})
	}()
	done.Wait()

	if got == nil {
		t.Fatal("a nested borrow reported nothing")
	}
	if got.Op != "Buffer.Size" {
		t.Errorf("violation Op = %q, want %q", got.Op, "Buffer.Size")
	}
}

// The guard tracks every buffer a goroutine is borrowing, not just the
// innermost: reentering the OUTER buffer from inside a nested borrow of a
// different one is still the self-deadlock, and must still be reported with
// the outer borrow's stack. The reentry comes after the inner borrow has
// ended, so it also proves that exit removes only its own buffer.
func TestBorrowGuard_TracksEveryHeldBuffer(t *testing.T) {
	outer, err := NewBuffer(16)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	defer outer.Destroy()
	inner, err := NewBuffer(16)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	defer inner.Destroy()

	var got *NestedBorrowError
	var done sync.WaitGroup
	done.Add(1)
	restore := EnableBorrowGuard(recordViolation(&got, &done))
	defer restore()

	go func() {
		_ = outer.WithBytes(func([]byte) error {
			_ = inner.WithBytes(func([]byte) error { return nil })
			_ = outer.Size()
			return nil
		})
	}()
	done.Wait()

	if got == nil {
		t.Fatal("reentering the outer buffer after a nested borrow of another reported nothing")
	}
	if got.Op != "Buffer.Size" {
		t.Errorf("violation Op = %q, want %q", got.Op, "Buffer.Size")
	}
	if !strings.Contains(got.BorrowStack, "TestBorrowGuard_TracksEveryHeldBuffer") {
		t.Errorf("violation does not carry the outer borrow's stack:\n%s", got.BorrowStack)
	}
}

// Borrows on DIFFERENT goroutines are legitimate and concurrent, so the guard
// must not confuse one goroutine's borrow for another's.
//
// This is the false-positive half, and it is the assertion that makes the
// goroutine-identity machinery worth having: a guard keyed on anything coarser
// would fail this and would then be disarmed by whoever hit it.
func TestBorrowGuard_DoesNotFireAcrossGoroutines(t *testing.T) {
	buf, err := NewBuffer(32)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	defer buf.Destroy()

	violations := make(chan *NestedBorrowError, 8)
	restore := EnableBorrowGuard(func(e *NestedBorrowError) {
		violations <- e
		runtime.Goexit()
	})
	defer restore()

	// Two goroutines hold OVERLAPPING borrows, which is the ordinary shape:
	// borrows are shared and run concurrently with each other, and only
	// Destroy is exclusive. Each waits for the other to be
	// inside its callback, so the overlap is guaranteed rather than hoped for.
	//
	// It is deliberately not "one borrows while the other allocates": an
	// exclusive operation on the SAME buffer would block on its per-object
	// lock until the shared borrow ended, which is the coordination working,
	// and a rendezvous across it deadlocks the test itself. That mistake was
	// made writing this file, and the stack it produced is the same one an
	// unguarded conversion produces in production.
	const borrowers = 2
	inside := make(chan struct{}, borrowers)
	proceed := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(borrowers)
	for i := 0; i < borrowers; i++ {
		go func() {
			defer wg.Done()
			_ = buf.WithBytes(func([]byte) error {
				inside <- struct{}{}
				<-proceed
				return nil
			})
		}()
	}
	for i := 0; i < borrowers; i++ {
		<-inside
	}
	close(proceed)
	wg.Wait()

	select {
	case v := <-violations:
		t.Fatalf("guard fired across goroutines: %v", v.Op)
	default:
	}
}

// Restoring must leave the guard exactly as it was found, including when two
// armings are nested.
func TestBorrowGuard_RestoreIsNestableAndIdempotent(t *testing.T) {
	if borrowGuardArmed.Load() {
		t.Fatal("guard armed before the test armed it; a previous test leaked its restore")
	}
	outer := EnableBorrowGuard(func(*NestedBorrowError) { runtime.Goexit() })
	if !borrowGuardArmed.Load() {
		t.Fatal("guard not armed after EnableBorrowGuard")
	}
	inner := EnableBorrowGuard(func(*NestedBorrowError) { runtime.Goexit() })
	inner()
	if !borrowGuardArmed.Load() {
		t.Fatal("the inner restore disarmed a guard the outer arming still holds")
	}
	inner() // second call must be a no-op
	if !borrowGuardArmed.Load() {
		t.Fatal("a repeated restore disarmed the outer arming")
	}
	outer()
	if borrowGuardArmed.Load() {
		t.Fatal("guard still armed after the outer restore")
	}
}

// A nil handler is the documented default and must panic rather than return
// into the deadlock.
func TestBorrowGuard_NilHandlerPanics(t *testing.T) {
	buf, err := NewBuffer(32)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	defer buf.Destroy()

	restore := EnableBorrowGuard(nil)
	defer restore()

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = buf.WithBytes(func([]byte) error {
			_ = buf.Size()
			return nil
		})
	}()

	if recovered == nil {
		t.Fatal("a nil handler did not panic")
	}
	if _, ok := recovered.(*NestedBorrowError); !ok {
		t.Fatalf("panic value is %T, want *NestedBorrowError", recovered)
	}
}

// A handler that RETURNS must still not let the caller proceed. The guard
// panics past it, because returning is the one outcome that reaches the
// deadlock the report was about.
func TestBorrowGuard_PanicsPastAHandlerThatReturns(t *testing.T) {
	buf, err := NewBuffer(32)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	defer buf.Destroy()

	called := false
	restore := EnableBorrowGuard(func(*NestedBorrowError) { called = true })
	defer restore()

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = buf.WithBytes(func([]byte) error {
			_ = buf.Size()
			return nil
		})
	}()

	if !called {
		t.Error("handler was not called")
	}
	if _, ok := recovered.(*NestedBorrowError); !ok {
		t.Fatalf("panic value is %T, want *NestedBorrowError — a returning handler was allowed through", recovered)
	}
}

// Disarmed is the production default, and nothing may be reported in it.
func TestBorrowGuard_DisarmedReportsNothing(t *testing.T) {
	if borrowGuardArmed.Load() {
		t.Fatal("guard armed at entry")
	}
	buf, err := NewBuffer(32)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	defer buf.Destroy()

	// Size inside a borrow is a violation the ARMED guard reports. Disarmed it
	// must complete, which also confirms the check really is behind the atomic
	// and not compiled in unconditionally.
	if err := buf.WithBytes(func([]byte) error {
		if got := buf.Size(); got != 32 {
			t.Errorf("Size = %d, want 32", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithBytes: %v", err)
	}
}

// parseGoroutineID's rejecting branches are unreachable through the runtime,
// so they are exercised directly. A detector that mis-parsed an ID would give
// two goroutines the same frame and invent a violation, which is worse than
// reporting none.
func TestParseGoroutineID(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
		want   uint64
		wantOK bool
	}{
		{"real header", "goroutine 42 [running]:\nmain.f()", 42, true},
		{"single digit", "goroutine 1 [running]:", 1, true},
		{"large id", "goroutine 18446744073709551615 [running]:", 18446744073709551615, true},
		{"empty", "", 0, false},
		{"prefix only", "goroutine ", 0, false},
		{"wrong prefix", "GOROUTINE 42 [running]:", 0, false},
		{"no digits", "goroutine [running]:", 0, false},
		{"overflows uint64", "goroutine 18446744073709551616 [running]:", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseGoroutineID([]byte(tc.header))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if got != tc.want {
				t.Errorf("id = %d, want %d", got, tc.want)
			}
		})
	}
}

// goroutineID must be stable within a goroutine and distinct between two.
func TestGoroutineID(t *testing.T) {
	a, ok := goroutineID()
	if !ok {
		t.Fatal("goroutineID failed on a live goroutine")
	}
	again, _ := goroutineID()
	if a != again {
		t.Errorf("goroutineID is not stable within a goroutine: %d then %d", a, again)
	}
	other := make(chan uint64, 1)
	go func() {
		id, _ := goroutineID()
		other <- id
	}()
	if b := <-other; b == a {
		t.Errorf("two goroutines reported the same id %d", b)
	}
}

// An unbalanced exit — a borrow released while the guard was armed but taken
// before it was — must be ignored rather than corrupting the frame.
func TestGuardExitBorrow_UnbalancedIsIgnored(t *testing.T) {
	restore := EnableBorrowGuard(func(*NestedBorrowError) { runtime.Goexit() })
	defer restore()

	buf, err := NewBuffer(16)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	defer buf.Destroy()
	other, err := NewBuffer(16)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	defer other.Destroy()

	guardExitBorrow(buf) // no frame for this goroutine
	guardExitBorrow(buf)

	// An exit for a buffer the frame does not hold leaves the frame alone.
	if err := other.WithBytes(func([]byte) error {
		guardExitBorrow(buf)
		return nil
	}); err != nil {
		t.Fatalf("WithBytes: %v", err)
	}

	// A borrow must still be admitted afterwards.
	if err := buf.WithBytes(func(b []byte) error {
		if len(b) != 16 {
			t.Errorf("len = %d, want 16", len(b))
		}
		return nil
	}); err != nil {
		t.Fatalf("WithBytes after unbalanced exits: %v", err)
	}
}

// The mutation proof: WITHOUT the guard this shape hangs forever, and it
// cannot be shown in this process because a wedged goroutine holds this
// buffer's own per-object lock shared for the life of the binary — reentering
// it exclusively from Destroy, called from inside the buffer's own WithBytes,
// self-deadlocks under any RWMutex — and every later test in the package
// would block behind it. So it runs in a subprocess, twice.
//
// unguarded: killed at the deadline, having produced nothing.
// guarded:   exits promptly reporting *NestedBorrowError.
//
// A guard whose tests all pass with the guard removed proves nothing; this is
// the test that fails if the mechanism is gutted.
func TestBorrowGuard_TheUnguardedShapeReallyHangs(t *testing.T) {
	const deadlockTestName = "TestBorrowGuard_TheUnguardedShapeReallyHangs"

	if mode := os.Getenv(childEnv); mode != "" {
		runDeadlockChild(mode == "guarded")
		return
	}

	const deadline = 3 * time.Second
	for _, tc := range []struct {
		mode      string
		wantHang  bool
		wantInOut string
	}{
		{"unguarded", true, ""},
		{"guarded", false, "attempted on a buffer this goroutine is borrowing"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			// #nosec G204,G702 -- re-exec of this test binary: argv[0] is os.Args[0], the path `go test` invoked, and the arguments are compile-time literals plus the GOCOVERDIR the go tool exported for this run. No shell is involved.
			cmd := exec.Command(os.Args[0], subprocessTestArgs(deadlockTestName, deadline)...)
			cmd.Env = append(os.Environ(), childEnv+"="+tc.mode)
			out, err := cmd.CombinedOutput()

			switch {
			case tc.wantHang && err == nil:
				t.Fatalf("the unguarded shape returned; the deadlock this guard exists for is gone,\n"+
					"so either the buffer's own lock stopped being exclusive or the child stopped exercising it.\nchild output:\n%s", out)
			case !tc.wantHang && err == nil:
				t.Fatalf("the guarded shape exited 0; it must report a violation.\nchild output:\n%s", out)
			}
			if tc.wantInOut != "" && !strings.Contains(string(out), tc.wantInOut) {
				t.Fatalf("child output does not report the violation.\nwant substring: %q\ngot:\n%s", tc.wantInOut, out)
			}
			if tc.wantHang && strings.Contains(string(out), "attempted on a buffer this goroutine is borrowing") {
				t.Fatalf("the UNguarded child reported a violation; the guard is armed when it should not be.\n%s", out)
			}
		})
	}
}

// runDeadlockChild performs the offending act in the subprocess. Guarded it
// panics with *NestedBorrowError; unguarded it never returns and the parent
// kills it at the test timeout.
//
// The offending act is THIS buffer's own Destroy, called from inside its own
// WithBytes callback — the one nesting go-enclave's per-object locks cannot
// survive, and the hazard any sync.RWMutex has: WithBytes holds buf's lock for reading, Destroy needs it
// for writing, and a write request blocks behind a read only THIS goroutine
// holds and only THIS goroutine could release — which it never does, because
// it is the one waiting.
func runDeadlockChild(guarded bool) {
	if guarded {
		defer EnableBorrowGuard(nil)()
	}
	buf, err := NewBuffer(32)
	if err != nil {
		return
	}
	_ = buf.WithBytes(func([]byte) error {
		buf.Destroy()
		return nil
	})
}

// GuardBorrows must report a violation raised on the goroutine it guards.
func TestGuardBorrows_ReportsAViolation(t *testing.T) {
	buf, err := NewBuffer(32)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	defer buf.Destroy()

	var reported string
	func() {
		defer GuardBorrows(func(format string, args ...any) {
			reported = format
			_ = args
		})()
		// On its own goroutine, so the violation's Goexit does not end this
		// test before the deferred stop runs.
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = buf.WithBytes(func([]byte) error {
				_ = buf.Size()
				return nil
			})
		}()
		<-done
	}()

	if !strings.Contains(reported, "reentered a Buffer it was borrowing") {
		t.Fatalf("GuardBorrows reported %q, want a nested-borrow report", reported)
	}
}

// A clean path must report nothing, or every converted site would fail.
func TestGuardBorrows_SilentOnACleanPath(t *testing.T) {
	buf, err := NewBufferFromBytes([]byte("clean"))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	defer buf.Destroy()

	called := false
	func() {
		defer GuardBorrows(func(string, ...any) { called = true })()
		if _, err := CopyOut(buf); err != nil {
			t.Errorf("CopyOut: %v", err)
		}
	}()
	if called {
		t.Fatal("GuardBorrows reported a violation on a clean path")
	}
}

// A nil report must not panic — the guard is a diagnostic and must never be
// the thing that breaks a run.
func TestGuardBorrows_NilReportIsSafe(t *testing.T) {
	stop := GuardBorrows(nil)
	stop()
	if borrowGuardArmed.Load() {
		t.Fatal("guard still armed after stop")
	}
}
