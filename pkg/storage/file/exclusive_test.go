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

package file

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/util/filelock"
)

// The cross-process write lock is an OS lock on an open file, so two Engine
// values over one directory in this process contend exactly as two processes
// would. The multi-process proof lives in pkg/database; these tests pin the
// engine's own lock handling, including every path that must release it.

func openPair(t *testing.T) (*Engine, *Engine) {
	t.Helper()
	dir := t.TempDir()
	a, err := New(dir)
	if err != nil {
		t.Fatalf("New a: %v", err)
	}
	b, err := New(dir)
	if err != nil {
		t.Fatalf("New b: %v", err)
	}
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	return a, b
}

// blocked reports whether done stays unreadable for a short grace period.
func blocked(done <-chan struct{}) bool {
	select {
	case <-done:
		return false
	case <-time.After(100 * time.Millisecond):
		return true
	}
}

func TestNew_CreatesLockFileAndStillTreatsDirectoryAsFresh(t *testing.T) {
	dir := t.TempDir()
	e, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, lockFileName)); err != nil {
		t.Fatalf("lock file: %v", err)
	}
	// Reopen: the lock file alone must not make the directory look like data
	// of an unmarked layout, and an opened one must be accepted again.
	if err := os.Remove(filepath.Join(dir, formatMarkerFileName)); err != nil {
		t.Fatal(err)
	}
	e2, err := New(dir)
	if err != nil {
		t.Fatalf("reopen with only the lock file present: %v", err)
	}
	_ = e2.Close()
}

func TestExclusive_BlocksOtherEngineWritesUntilReturn(t *testing.T) {
	a, b := openPair(t)

	inside := make(chan struct{})
	release := make(chan struct{})
	exclusiveDone := make(chan error, 1)
	go func() {
		exclusiveDone <- a.Exclusive(func(storage.ReadWriter) error {
			close(inside)
			<-release
			return nil
		})
	}()
	<-inside

	setDone := make(chan struct{})
	go func() {
		defer close(setDone)
		_ = b.Set([]byte("k"), []byte("v"))
	}()
	if !blocked(setDone) {
		t.Fatal("a Set on another engine completed while Exclusive held the lock")
	}

	close(release)
	if err := <-exclusiveDone; err != nil {
		t.Fatalf("Exclusive: %v", err)
	}
	select {
	case <-setDone:
	case <-time.After(10 * time.Second):
		t.Fatal("Set stayed blocked after Exclusive returned")
	}
}

func TestExclusive_ReadWriterReadsWritesAndDeletesWithoutRelocking(t *testing.T) {
	e, _ := openPair(t)
	err := e.Exclusive(func(rw storage.ReadWriter) error {
		if got, err := rw.Get([]byte("k")); err != nil || got != nil {
			t.Errorf("Get absent = %q, %v", got, err)
		}
		if err := rw.Set([]byte("k"), []byte("v")); err != nil {
			return err
		}
		if got, err := rw.Get([]byte("k")); err != nil || string(got) != "v" {
			t.Errorf("Get after Set = %q, %v", got, err)
		}
		return rw.Delete([]byte("k"))
	})
	if err != nil {
		t.Fatalf("Exclusive: %v", err)
	}
	if got, _ := e.Get([]byte("k")); got != nil {
		t.Fatalf("key survived Delete: %q", got)
	}
}

func TestExclusive_CallbackErrorIsReturnedAndLockReleased(t *testing.T) {
	a, b := openPair(t)
	sentinel := errors.New("callback failed")
	if err := a.Exclusive(func(storage.ReadWriter) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("Exclusive = %v, want the callback's error", err)
	}
	// Both the in-process mutex and the OS lock must be free again.
	if err := b.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set on the other engine after a failed Exclusive: %v", err)
	}
	if err := a.Set([]byte("k2"), []byte("v")); err != nil {
		t.Fatalf("Set on the same engine after a failed Exclusive: %v", err)
	}
}

func TestExclusive_CallbackPanicReleasesLock(t *testing.T) {
	a, b := openPair(t)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("expected the callback's panic to propagate")
			}
		}()
		_ = a.Exclusive(func(storage.ReadWriter) error { panic("boom") })
	}()
	if err := b.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set after a panicking Exclusive: %v", err)
	}
}

func TestExclusive_ClosedEngineReturnsClosedError(t *testing.T) {
	e, _ := openPair(t)
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	called := false
	err := e.Exclusive(func(storage.ReadWriter) error { called = true; return nil })
	var ce *storage.ClosedError
	if !errors.As(err, &ce) || called {
		t.Fatalf("Exclusive on closed engine = %v (callback ran: %v), want ClosedError", err, called)
	}
}

func TestClose_ReleasesLockAndIsIdempotent(t *testing.T) {
	a, b := openPair(t)
	held := make(chan struct{})
	go func() {
		defer close(held)
		_ = a.Set([]byte("warm"), []byte("v"))
	}()
	<-held
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
	if err := b.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set on the other engine after Close: %v", err)
	}
}

func TestSet_LockFailureIsTypedAndLeaksNothing(t *testing.T) {
	a, _ := openPair(t)
	// Break the engine's lock file out from under it: every write must now
	// report a typed lock error, and must not leave writeMu held (a second
	// call returning at all, rather than hanging, proves it).
	if err := a.lock.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		err := a.Set([]byte("k"), []byte("v"))
		var le *filelock.Error
		if !errors.As(err, &le) || le.Op != "lock" {
			t.Fatalf("Set %d = %v, want *filelock.Error with Op lock", i, err)
		}
	}
	if err := a.Delete([]byte("k")); err == nil {
		t.Fatal("Delete with a broken lock = nil, want an error")
	}
	if err := a.Batch().Commit(); err != nil {
		// An empty batch commits nothing and takes no lock.
		t.Fatalf("empty Commit = %v", err)
	}
}

func TestExclusive_UnlockFailureIsReportedAndMutexReleased(t *testing.T) {
	a, _ := openPair(t)
	err := a.Exclusive(func(storage.ReadWriter) error {
		// Close the lock file inside the critical section so releasing fails.
		return a.lock.Close()
	})
	var le *filelock.Error
	if !errors.As(err, &le) || le.Op != "unlock" {
		t.Fatalf("Exclusive = %v, want *filelock.Error with Op unlock", err)
	}
	// writeMu must have been released: this returns (with an error) rather than hanging.
	if err := a.Set([]byte("k"), []byte("v")); err == nil {
		t.Fatal("Set after an unlock failure = nil, want the closed lock to refuse")
	}
}

func TestNew_WaitsForWriterInFlightBeforeRecovering(t *testing.T) {
	a, _ := openPair(t)
	dir := a.dir

	inside := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = a.Exclusive(func(storage.ReadWriter) error {
			close(inside)
			<-release
			return nil
		})
	}()
	<-inside

	opened := make(chan struct{})
	go func() {
		defer close(opened)
		if e, err := New(dir); err == nil {
			_ = e.Close()
		}
	}()
	if !blocked(opened) {
		t.Fatal("New completed its open-time recovery while another engine held the write lock")
	}
	close(release)
	select {
	case <-opened:
	case <-time.After(10 * time.Second):
		t.Fatal("New stayed blocked after the writer released the lock")
	}
}

func TestBatchCommit_BlocksOnOtherEngineLock(t *testing.T) {
	a, b := openPair(t)
	inside := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = a.Exclusive(func(storage.ReadWriter) error {
			close(inside)
			<-release
			return nil
		})
	}()
	<-inside

	batch := b.Batch()
	defer func() { _ = batch.Close() }()
	if err := batch.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatal(err)
	}
	committed := make(chan error, 1)
	go func() { committed <- batch.Commit() }()
	select {
	case <-committed:
		t.Fatal("a batch committed while another engine held the write lock")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-committed; err != nil {
		t.Fatalf("Commit after release: %v", err)
	}
}
