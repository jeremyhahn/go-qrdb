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

package pebble_test

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// newClosedTestStore returns an open PebbleDB store rooted in a temp dir.
func newClosedTestStore(t *testing.T) *pebble.PebbleStorage {
	t.Helper()
	s, err := pebble.New(&pebble.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("pebble.New: %v", err)
	}
	return s
}

// TestCloseUnderConcurrentWritersReturnsErrors is the reproduction from
// go-qrdb#371: open a store, run goroutines writing in a loop, close it under
// them. Before the fix a writer that had already passed its closed check went
// on to touch a released DB and panicked `pebble: closed` out of a goroutine
// with nothing above it, taking the daemon down.
//
// Every write must end as either a success (it beat the close) or an error
// that matches ErrStorageClosed (it did not). There is no third outcome, and a
// panic in any writer fails the test.
func TestCloseUnderConcurrentWritersReturnsErrors(t *testing.T) {
	const (
		writers          = 8
		writesPerWriter  = 400
		closeAfterWrites = 50
	)

	s := newClosedTestStore(t)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		bad     []error
		started = make(chan struct{})
	)

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					mu.Lock()
					bad = append(bad, &escapedPanicError{value: r})
					mu.Unlock()
				}
			}()
			<-started
			key := []byte{byte('a' + id)}
			for i := 0; i < writesPerWriter; i++ {
				err := s.Set(key, []byte("v"))
				if err == nil || errors.Is(err, storage.ErrStorageClosed) {
					continue
				}
				mu.Lock()
				bad = append(bad, err)
				mu.Unlock()
				return
			}
		}(w)
	}

	closeErr := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-started
		for i := 0; i < closeAfterWrites; i++ {
			_, _ = s.Exists([]byte("a"))
		}
		closeErr <- s.Close()
	}()

	close(started)
	wg.Wait()

	if err := <-closeErr; err != nil {
		t.Fatalf("Close: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, err := range bad {
		t.Errorf("writer produced a result that is neither success nor ErrStorageClosed: %v", err)
	}
}

// escapedPanicError reports a panic that reached a writer goroutine's
// boundary. It is an error type so the harness can collect it alongside the
// unexpected error values without a second channel.
type escapedPanicError struct{ value any }

// Error returns a human-readable description of the escaped panic.
func (e *escapedPanicError) Error() string {
	if err, ok := e.value.(error); ok {
		return "a panic escaped the writer: " + err.Error()
	}
	return "a panic escaped the writer"
}

// TestCloseUnderConcurrentBatchCommitsReturnsErrors is the same race on the
// batch path, which is the one the MVCC and KV engines actually take: an apply
// is Batch-then-Commit, and PebbleDB hands out a batch on a closed DB happily
// and only panics at Commit.
func TestCloseUnderConcurrentBatchCommitsReturnsErrors(t *testing.T) {
	const (
		writers         = 8
		writesPerWriter = 300
	)

	s := newClosedTestStore(t)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		bad     []error
		started = make(chan struct{})
	)

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					mu.Lock()
					bad = append(bad, &escapedPanicError{value: r})
					mu.Unlock()
				}
			}()
			<-started
			key := []byte{byte('a' + id)}
			for i := 0; i < writesPerWriter; i++ {
				b := s.Batch()
				if err := b.Set(key, []byte("v")); err != nil && !errors.Is(err, storage.ErrStorageClosed) {
					mu.Lock()
					bad = append(bad, err)
					mu.Unlock()
					_ = b.Close()
					return
				}
				// A read through the batch is what MVCC's put does before it
				// writes; it is the frame the reported panic came from.
				if _, err := b.Get(key); err != nil && !errors.Is(err, storage.ErrStorageClosed) {
					mu.Lock()
					bad = append(bad, err)
					mu.Unlock()
					_ = b.Close()
					return
				}
				if err := b.Commit(); err != nil && !errors.Is(err, storage.ErrStorageClosed) {
					mu.Lock()
					bad = append(bad, err)
					mu.Unlock()
					_ = b.Close()
					return
				}
				_ = b.Close()
			}
		}(w)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-started
		for i := 0; i < 50; i++ {
			_, _ = s.Exists([]byte("a"))
		}
		_ = s.Close()
	}()

	close(started)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	for _, err := range bad {
		t.Errorf("batch writer produced a result that is neither success nor ErrStorageClosed: %v", err)
	}
}

// TestClosedStoreBatchIsInert asserts a batch obtained from an
// already-closed store refuses every operation instead of buffering writes it
// could never commit.
func TestClosedStoreBatchIsInert(t *testing.T) {
	s := newClosedTestStore(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	b := s.Batch()
	t.Cleanup(func() { _ = b.Close() })

	for name, call := range map[string]func() error{
		"Set":      func() error { return b.Set([]byte("k"), []byte("v")) },
		"Delete":   func() error { return b.Delete([]byte("k")) },
		"Get":      func() error { _, err := b.Get([]byte("k")); return err },
		"Commit":   func() error { return b.Commit() },
		"Rollback": func() error { return b.Rollback() },
	} {
		if err := call(); !errors.Is(err, storage.ErrStorageClosed) {
			t.Errorf("%s = %v, want ErrStorageClosed", name, err)
		}
	}
	if err := b.Close(); err != nil {
		t.Errorf("Close on an inert batch = %v, want nil", err)
	}
}

// TestClosedStoreSnapshotReadsAreRefused asserts a snapshot taken before Close
// reports the closure from its reads rather than panicking through a released
// DB. PebbleDB reports the snapshot as leaked and closes the database anyway,
// so the snapshot's own open flag says nothing about whether it can still read.
func TestClosedStoreSnapshotReadsAreRefused(t *testing.T) {
	s := newClosedTestStore(t)
	if err := s.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Close reports the leaked snapshot; the store is closed regardless.
	if err := s.Close(); err == nil {
		t.Fatal("Close with an open snapshot returned nil, want the leak report")
	}

	if _, err := snap.Get([]byte("k")); !errors.Is(err, storage.ErrStorageClosed) {
		t.Errorf("snapshot Get after store Close = %v, want ErrStorageClosed", err)
	}

	it := snap.NewIterator(nil)
	if it.SeekToFirst() {
		t.Error("snapshot iterator after store Close positioned itself")
	}
	if !errors.Is(it.Error(), storage.ErrStorageClosed) {
		t.Errorf("snapshot iterator Error() = %v, want ErrStorageClosed", it.Error())
	}
	if err := it.Close(); err != nil {
		t.Errorf("snapshot iterator Close = %v, want nil", err)
	}
	if err := snap.Close(); err != nil {
		t.Errorf("snapshot Close after store Close = %v, want nil", err)
	}
}

// TestMetricsOnClosedStoreIsNil asserts Metrics reports the absence of a DB
// rather than a zero-valued snapshot that reads as a genuinely idle store.
func TestMetricsOnClosedStoreIsNil(t *testing.T) {
	s := newClosedTestStore(t)
	if s.Metrics() == nil {
		t.Fatal("Metrics on an open store = nil, want a snapshot")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := s.Metrics(); got != nil {
		t.Errorf("Metrics on a closed store = %v, want nil", got)
	}
}

// TestClosedStoreIteratorIsRefused asserts an iterator requested from a
// closed store never positions and reports the closure.
func TestClosedStoreIteratorIsRefused(t *testing.T) {
	s := newClosedTestStore(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	it := s.NewIterator(nil)
	defer func() { _ = it.Close() }()
	if it.SeekToFirst() {
		t.Error("iterator on a closed store positioned itself")
	}
	if !errors.Is(it.Error(), storage.ErrStorageClosed) {
		t.Errorf("iterator Error() = %v, want ErrStorageClosed", it.Error())
	}
}

// TestCloseFailureIsOpErrorAndStillCloses asserts a PebbleDB close failure
// (here the leaked-iterator report) is returned as OpError{Op: close} and
// the store stays closed, so a second Close is a no-op.
func TestCloseFailureIsOpErrorAndStillCloses(t *testing.T) {
	s := newClosedTestStore(t)
	_ = s.NewIterator(nil) // leaked on purpose: PebbleDB.Close reports it

	err := s.Close()
	var opErr *storage.OpError
	if !errors.As(err, &opErr) || opErr.Op != "close" {
		t.Fatalf("Close with a leaked iterator = %v, want *storage.OpError{Op: close}", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
}

// TestIteratorResumedAfterCloseServesItsPinnedVersion settles what the gate
// deliberately does NOT cover, because the answer is not obvious from reading
// either codebase and the next reader will ask.
//
// The gate makes iterator CREATION atomic with respect to Close. It does not
// wrap each Next on an iterator created while the store was open and advanced
// after it closed — a per-element gate acquisition on the hottest path in the
// storage layer, paid by every scan, to police a caller lifetime bug.
//
// Measured against cockroachdb/pebble v2.1.4: such an iterator does NOT panic.
// It holds its own reference to the LSM version it was created against, so it
// keeps serving that pinned, internally consistent snapshot. What it does not
// do is report the closure — so the "never a panic" invariant holds, while
// "reports ErrStorageClosed" is a property of operations issued THROUGH the
// store, not of a handle the caller kept across Close.
//
// The leak is not silent: PebbleDB names it and PebbleStorage.Close returns it,
// which is the signal a caller gets and the reason this is left as it is.
func TestIteratorResumedAfterCloseServesItsPinnedVersion(t *testing.T) {
	s := newClosedTestStore(t)
	for _, k := range []string{"a", "b", "c"} {
		if err := s.Set([]byte(k), []byte("v")); err != nil {
			t.Fatalf("Set %q: %v", k, err)
		}
	}

	it := s.NewIterator(nil)
	if !it.SeekToFirst() {
		t.Fatal("SeekToFirst on an open store did not position the iterator")
	}

	// Close reports the iterator the caller left open. The DB closes anyway.
	closeErr := s.Close()
	if closeErr == nil {
		t.Fatal("Close with an open iterator returned nil; the leak must be reported")
	}
	if !strings.Contains(closeErr.Error(), "leaked iterators") {
		t.Fatalf("Close = %v, want it to name the leaked iterator", closeErr)
	}

	// Advancing it must not panic. It is allowed to keep serving the version
	// it pinned; it is not allowed to take the process down.
	for name, advance := range map[string]func() bool{
		"Next":        it.Next,
		"Seek":        func() bool { return it.Seek([]byte("c")) },
		"SeekToLast":  it.SeekToLast,
		"Prev":        it.Prev,
		"SeekToFirst": it.SeekToFirst,
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%s on an iterator resumed after Close panicked: %v", name, r)
				}
			}()
			advance()
			_ = it.Key()
			_ = it.Value()
			if err := it.Error(); err != nil {
				t.Fatalf("%s reported %v; the pinned version is expected to read cleanly", name, err)
			}
		})
	}

	if err := it.Close(); err != nil {
		t.Errorf("iterator Close after store Close = %v, want nil", err)
	}
}
