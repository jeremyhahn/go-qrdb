// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package secure

import (
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// waitForQueuedWriter blocks until a purge-class operation is holding or
// queued on purgeMu, and reports whether it ever observed one.
//
// It interrogates the lock DIRECTLY rather than inferring from a timeout, and
// that choice is the whole reason this test is not vacuous. TryRLock fails
// exactly when a writer holds or is waiting for the lock. The caller already
// holds purgeMu shared through a live borrow, so a failed TryRLock cannot mean
// anything except that a purge-class operation reached purgeMu.Lock while that
// borrow was in flight — which is precisely the coordination under test.
//
// The alternatives all pass without proving anything: a probe goroutine that
// the scheduler never runs looks identical to one that is genuinely blocked,
// and "the allocation had not finished after N milliseconds" is satisfied by a
// slow machine.
func waitForQueuedWriter(timeout time.Duration) bool {
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		if !purgeMu.TryRLock() {
			return true
		}
		purgeMu.RUnlock()
		time.Sleep(time.Millisecond)
	}
	return false
}

// A purge-class operation must not proceed while a borrow is live.
//
// memguard's failure path is process-wide: a failed allocation calls
// core.Panic, which purges every live enclave and UNMAPS their pages. So an
// unrelated component constructing a Buffer can pull the pages out from under
// a borrow in flight elsewhere in the process. Per-object locking cannot
// prevent it, because memguard's purge does not take any lock this package
// owns (go-qrdb#71).
//
// The assertion is that the allocator is still parked when the borrow ends —
// observed on the lock itself, not timed.
func TestPurgeCoordination_AllocationWaitsForALiveBorrow(t *testing.T) {
	buf, err := NewBufferFromBytes([]byte("borrowed-material"))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	defer buf.Destroy()

	borrowed := make(chan struct{})
	release := make(chan struct{})
	var order atomic.Int32
	var borrowExit, allocExit atomic.Int32

	borrowErr := make(chan error, 1)
	go func() {
		borrowErr <- buf.WithBytes(func(b []byte) error {
			close(borrowed)
			<-release
			// Read AFTER the foreign allocation has had its chance to run. On
			// the uncoordinated code these pages could already be unmapped.
			if string(b) != "borrowed-material" {
				t.Errorf("borrowed material changed underneath the borrow: %q", b)
			}
			// Recorded HERE, while the read lock is still held. Recording it
			// after WithBytes returns would race the woken allocator, which by
			// then may already have recorded its own completion — a flake
			// against correct code.
			borrowExit.Store(order.Add(1))
			return nil
		})
	}()

	<-borrowed

	allocDone := make(chan struct{})
	go func() {
		defer close(allocDone)
		// A purge-class operation from an unrelated component.
		other, oerr := NewBufferFromBytes([]byte("foreign-material"))
		allocExit.Store(order.Add(1))
		if oerr == nil {
			other.Destroy()
		}
	}()

	// Being scheduled is not the same as having reached the lock, so observe
	// the queued writer directly.
	if !waitForQueuedWriter(10 * time.Second) {
		close(release)
		<-allocDone
		t.Fatal("no purge-class operation ever queued on purgeMu while a borrow was " +
			"live — the allocation either completed before the borrow started or " +
			"never took the lock, and this test would prove nothing")
	}

	// It is parked on purgeMu, so it cannot have purged.
	select {
	case <-allocDone:
		t.Fatal("a purge-class operation completed while a borrow was still in flight")
	default:
	}

	close(release)

	if berr := <-borrowErr; berr != nil {
		t.Fatalf("WithBytes: %v", berr)
	}
	<-allocDone

	if borrowExit.Load() == 0 || allocExit.Load() == 0 {
		t.Fatal("one of the two paths never recorded completion")
	}
	if borrowExit.Load() > allocExit.Load() {
		t.Fatalf("the allocation completed first (borrow=%d alloc=%d); it must wait "+
			"for the borrow to finish", borrowExit.Load(), allocExit.Load())
	}
}

// Destroy is purge-class, not a read.
//
// memguard's buffer destroy calls core.Panic when memcall.Free, Unlock or
// Protect fails, and core.Panic purges every live enclave. Treating Destroy as
// a borrow — which is what it looks like, since it touches only its own buffer
// — leaves exactly the hole this change closes.
//
// Honest limit, carried over from the same fix in go-objstore: this is
// reasoned from memguard's source, not mutation-verified. Destroy only purges
// when a memcall fails, and there is no seam to inject that failure through.
// The test proves Destroy takes the lock exclusively; it does not prove a
// purge would otherwise have happened.
func TestPurgeCoordination_DestroyIsExclusive(t *testing.T) {
	victim, err := NewBufferFromBytes([]byte("borrowed-material"))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	defer victim.Destroy()

	doomed, err := NewBufferFromBytes([]byte("to-be-destroyed"))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}

	borrowed := make(chan struct{})
	release := make(chan struct{})
	borrowErr := make(chan error, 1)
	go func() {
		borrowErr <- victim.WithBytes(func([]byte) error {
			close(borrowed)
			<-release
			return nil
		})
	}()
	<-borrowed

	destroyDone := make(chan struct{})
	go func() {
		defer close(destroyDone)
		doomed.Destroy()
	}()

	if !waitForQueuedWriter(10 * time.Second) {
		close(release)
		<-destroyDone
		t.Fatal("Destroy never queued on purgeMu while a borrow was live; it is " +
			"being treated as a read, which is the defect")
	}

	select {
	case <-destroyDone:
		t.Fatal("Destroy completed while a borrow was still in flight")
	default:
	}

	close(release)
	if berr := <-borrowErr; berr != nil {
		t.Fatalf("WithBytes: %v", berr)
	}
	<-destroyDone
}

// Borrows do not exclude each other. Only purge-class operations are
// exclusive, so two readers must overlap — otherwise the coordination has been
// implemented with a plain Mutex and every concurrent reader of key material
// is serialized for no reason.
func TestPurgeCoordination_BorrowsRunConcurrently(t *testing.T) {
	buf, err := NewBufferFromBytes([]byte("shared-material"))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	defer buf.Destroy()

	first := make(chan struct{})
	second := make(chan struct{})
	done := make(chan error, 2)

	go func() {
		done <- buf.WithBytes(func([]byte) error {
			close(first)
			// Only completes if the second borrow is admitted concurrently.
			<-second
			return nil
		})
	}()

	<-first
	go func() {
		done <- buf.WithBytes(func([]byte) error {
			close(second)
			return nil
		})
	}()

	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("WithBytes: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("two concurrent borrows deadlocked; borrows must be shared, " +
				"not exclusive")
		}
	}
}

// WithBytes refuses a destroyed or nil buffer rather than handing out a slice
// into released pages.
func TestWithBytes_RefusesAnUnusableBuffer(t *testing.T) {
	var nilBuf *Buffer
	if err := nilBuf.WithBytes(func([]byte) error { return nil }); err == nil {
		t.Fatal("WithBytes on a nil Buffer returned no error")
	}

	buf, err := NewBufferFromBytes([]byte("material"))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	buf.Destroy()

	called := false
	if err := buf.WithBytes(func([]byte) error { called = true; return nil }); err == nil {
		t.Fatal("WithBytes on a destroyed Buffer returned no error")
	}
	if called {
		t.Fatal("WithBytes invoked the callback on a destroyed Buffer; the slice " +
			"would point into released pages")
	}

	if err := buf.WithBytes(nil); err == nil {
		t.Fatal("WithBytes with a nil callback returned no error")
	}
}

// The callback's error is the caller's, passed through unchanged, so WithBytes
// can wrap a read that itself fails.
func TestWithBytes_PassesTheCallbackErrorThrough(t *testing.T) {
	buf, err := NewBufferFromBytes([]byte("material"))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	defer buf.Destroy()

	sentinel := &InvalidSizeError{Size: -1}
	if got := buf.WithBytes(func([]byte) error { return sentinel }); got != sentinel {
		t.Fatalf("WithBytes returned %v, want the callback's own error", got)
	}

	var seen string
	if err := buf.WithBytes(func(b []byte) error { seen = string(b); return nil }); err != nil {
		t.Fatalf("WithBytes: %v", err)
	}
	if seen != "material" {
		t.Fatalf("callback saw %q, want %q", seen, "material")
	}
}

// The SIGNAL path must be coordinated too, and it was the one that was not.
//
// memguard.CatchSignal installs a handler that calls memguard.Purge directly,
// taking none of this package's locks. A SIGINT or SIGTERM arriving while a
// borrow is live therefore unmapped the borrowed pages underneath the reader —
// the exact fault purgeMu exists to prevent, on the one purge this package
// does not initiate (go-qrdb#71).
//
// This drives coordinatedPurge, which is what the handler now calls, rather
// than delivering a real signal: a test that sends itself SIGTERM has to
// survive the handler's os.Exit, and what actually needs proving is that the
// purge the handler performs waits for borrows. Delivering the signal would
// test os/signal, not this package.
func TestPurgeCoordination_SignalPurgeWaitsForALiveBorrow(t *testing.T) {
	buf, err := NewBufferFromBytes([]byte("borrowed-material"))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	defer buf.Destroy()

	borrowed := make(chan struct{})
	release := make(chan struct{})
	borrowErr := make(chan error, 1)
	go func() {
		borrowErr <- buf.WithBytes(func(b []byte) error {
			close(borrowed)
			<-release
			if string(b) != "borrowed-material" {
				return &InvalidSizeError{Size: len(b)}
			}
			return nil
		})
	}()
	<-borrowed

	// The purge is indirected for the duration: driving the REAL one destroys
	// every enclave in this test binary, so every later test in the package
	// would run against purged memory. What needs proving is that the purge the
	// signal handler performs waits for borrows, and the lock is real either way.
	restore := swapPurgeFn(func() {})
	defer restore()

	purged := make(chan struct{})
	go func() {
		defer close(purged)
		coordinatedPurge()
	}()

	if !waitForQueuedWriter(10 * time.Second) {
		close(release)
		<-purged
		t.Fatal("the signal-path purge never queued on purgeMu while a borrow was " +
			"live; it is bypassing the coordination, which is the defect")
	}

	select {
	case <-purged:
		t.Fatal("the signal-path purge completed while a borrow was still in flight — " +
			"it would have unmapped the pages the borrow was reading")
	default:
	}

	close(release)
	if berr := <-borrowErr; berr != nil {
		t.Fatalf("WithBytes: %v", berr)
	}
	<-purged
}

// The signal handler must run the COORDINATED purge and then exit.
//
// Driven directly rather than by raising a real signal: handlers are
// process-global and outlive the test that installs them, so a raised SIGTERM
// can be taken by a stray handler that then calls os.Exit and kills the test
// binary mid-run. Measured — it did, as a fast FAIL with no panic and no
// failing test name. watchSignals is the whole body of the handler, so driving
// it tests the same code with none of that.
func TestWatchSignals_PurgesThroughTheCoordinatedPathThenExits(t *testing.T) {
	// The purge must run with purgeMu held EXCLUSIVELY. Asserting that from
	// inside the purge is what catches a handler that calls the raw purge
	// instead of the coordinated one: under the write lock TryRLock must fail,
	// and if the handler bypassed the coordination it would succeed.
	//
	// Without this the test passes whether or not the handler is coordinated,
	// which is the whole failure mode this file is about.
	purged := make(chan bool, 1)
	restore := swapPurgeFn(func() {
		locked := !purgeMu.TryRLock()
		if !locked {
			purgeMu.RUnlock()
		}
		purged <- locked
	})
	defer restore()

	exited := make(chan int, 1)
	realExit := signalExit
	signalExit = func(code int) { exited <- code }
	t.Cleanup(func() { signalExit = realExit })

	ch := make(chan os.Signal, 1)
	done := make(chan struct{})
	go func() { defer close(done); watchSignals(ch) }()

	ch <- syscall.SIGTERM

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the handler never completed")
	}

	select {
	case coordinated := <-purged:
		if !coordinated {
			t.Fatal("the handler purged WITHOUT holding purgeMu exclusively; it is " +
				"calling the raw purge instead of the coordinated one, so a signal " +
				"still unmaps pages underneath a live borrow")
		}
	default:
		t.Fatal("the handler exited without purging; replacing memguard's handler " +
			"must not drop the wipe it performed")
	}
	select {
	case code := <-exited:
		if code != 1 {
			t.Fatalf("handler exited %d, want 1 — memguard's handler exited 1 and "+
				"replacing it must not change that", code)
		}
	default:
		t.Fatal("the handler purged but never exited")
	}
}

// swapPurgeFn replaces the purge for the duration of a test and returns a
// restore func. Package-level state, so tests using it must not run in
// parallel — none of them do.
func swapPurgeFn(fn func()) func() {
	prev := purgeFn
	purgeFn = fn
	return func() { purgeFn = prev }
}

// Calling back into this package from inside a WithBytes callback deadlocks,
// and this pins BOTH ways it does.
//
// The hazard is not hypothetical. pkg/seal/tenant_keys.go:235 calls
// secure.NewSecret(dek) with dek borrowed from an enclosing WithBytes, which
// puts purgeMu.Lock() inside purgeMu.RLock() on one goroutine. sync.RWMutex is
// not reentrant, so that Lock waits for a reader which is the caller itself: it
// never returns. That is why go-qrdb#74 migrates those callers rather than this
// PR doing it — the migration is the fix, and this test is the guard rail that
// stops the shape being reintroduced.
//
// It proves the deadlock WITHOUT performing it. Actually calling NewSecret
// inside a borrow would hang this test forever rather than fail it, which is
// the worst possible way to encode a known defect: a hung suite reports no
// test name and no diagnosis. TryLock and TryRLock answer the same question
// instantly — "would a nested call block here?" — and a false from TryLock IS
// the deadlock condition, since the only thing standing between blocking and
// deadlocking is that the blocked goroutine holds the very read lock it waits
// on.
func TestPurgeCoordination_NestedCallsFromInsideABorrowWouldDeadlock(t *testing.T) {
	buf, err := NewBufferFromBytes([]byte("borrowed-material"))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	defer buf.Destroy()

	t.Run("a purge-class call cannot proceed", func(t *testing.T) {
		err := buf.WithBytes(func(_ []byte) error {
			// This is the tenant_keys.go:235 shape: NewSecret, NewBuffer and
			// Destroy all take purgeMu exclusively, so each of them reaches
			// exactly this lock from inside the borrow.
			if purgeMu.TryLock() {
				purgeMu.Unlock()
				t.Error("purgeMu was available for exclusive taking inside a borrow — " +
					"the borrow is not holding the read lock, so nothing serialises a " +
					"purge against material being read")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("WithBytes: %v", err)
		}
	})

	t.Run("a nested borrow is refused once a purge has queued", func(t *testing.T) {
		// The subtler half, and the reason the rule is "no nested borrows"
		// rather than "no nested WRITES". Go's RWMutex blocks new readers once
		// a writer is queued, so a second borrow taken from inside the first
		// is not merely inelegant: it waits behind a writer that is itself
		// waiting for the outer borrow to finish.
		queued := make(chan struct{})
		allocDone := make(chan struct{})

		werr := buf.WithBytes(func(_ []byte) error {
			// Nothing is queued yet, so a nested borrow WOULD still be served.
			// Asserting this first is what makes the assertion after it mean
			// something: it shows the refusal below is caused by the queued
			// writer and not by borrows being exclusive all along.
			if !purgeMu.TryRLock() {
				t.Fatal("a nested borrow was refused with no writer queued; " +
					"borrows must run concurrently with each other")
			}
			purgeMu.RUnlock()

			go func() {
				defer close(allocDone)
				<-queued
				other, oerr := NewBufferFromBytes([]byte("foreign-material"))
				if oerr == nil {
					other.Destroy()
				}
			}()
			close(queued)

			// Observe the writer actually parked on the lock rather than
			// assuming it was scheduled.
			if !waitForQueuedWriter(10 * time.Second) {
				t.Fatal("no purge-class operation ever queued on purgeMu, so the " +
					"nested-borrow refusal below would prove nothing")
			}

			if purgeMu.TryRLock() {
				purgeMu.RUnlock()
				t.Error("a nested borrow was admitted while a purge-class operation " +
					"was queued — it would be admitted ahead of a writer that is " +
					"waiting for this very borrow to end")
			}
			return nil
		})
		if werr != nil {
			t.Fatalf("WithBytes: %v", werr)
		}

		// The queued allocation proceeds once the outer borrow releases, which
		// is the whole point: the coordination delays it, it does not lose it.
		<-allocDone
	})
}
