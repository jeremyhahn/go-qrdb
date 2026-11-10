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

package pebble

import (
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/seal/quiesce"
)

// drainForever bounds a drain the test expects to finish; it never runs out.
const drainForever = time.Minute

// quiesceFS is a file system whose barrier has a quiesce gate, as encfs's
// does over a StorageBarrier.
type quiesceFS struct {
	vfs.FS
	gate *quiesce.Gate
}

func (fs quiesceFS) Quiesce() *quiesce.Gate { return fs.gate }

// openQuiesced opens a database under a watch over a gated file system.
func openQuiesced(t *testing.T, gate *quiesce.Gate) (*pebble.DB, *SealWatch) {
	t.Helper()
	db, w, err := Open("db", &pebble.Options{FS: quiesceFS{FS: vfs.NewMem(), gate: gate}}, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		w.Close()
		_ = db.Close()
	})
	return db, w
}

// heldBy returns the stores a drain with no wait reports as held.
func heldBy(g *quiesce.Gate) []string {
	var timeout *quiesce.DrainTimeoutError
	if errors.As(g.Drain(-1), &timeout) {
		return timeout.Stores
	}
	return nil
}

func TestSealWatch_AttachPicksUpTheGate(t *testing.T) {
	var gate quiesce.Gate
	w := NewSealWatch(NewLogger(nil), nil)
	w.Attach(&pebble.Options{FS: quiesceFS{FS: vfs.NewMem(), gate: &gate}})
	if w.gate != &gate {
		t.Fatal("Attach did not pick up the file system's gate")
	}
}

func TestSealWatch_AttachWithoutAGateLeavesItNil(t *testing.T) {
	w := NewSealWatch(NewLogger(nil), nil)
	w.Attach(&pebble.Options{FS: vfs.NewMem()})
	if w.gate != nil {
		t.Fatal("Attach found a gate on a plain file system")
	}
}

func TestOpen_RegistersTheStoreUnderItsDirectory(t *testing.T) {
	var gate quiesce.Gate
	_, w := openQuiesced(t, &gate)
	if w.lease.Name() != "db" {
		t.Fatalf("lease name = %q; want db", w.lease.Name())
	}
	if err := w.Admit(); err != nil {
		t.Fatal(err)
	}
	defer w.Done()
	if got := heldBy(&gate); !slices.Equal(got, []string{"db"}) {
		t.Fatalf("held = %v; want [db]", got)
	}
}

// createGateFS is a gated file system whose first file creation after it is
// armed blocks until release closes, so a test holds pebble.Open in flight
// without sleeping.
type createGateFS struct {
	quiesceFS
	armed   *atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (fs createGateFS) Create(name string, category vfs.DiskWriteCategory) (vfs.File, error) {
	if fs.armed.CompareAndSwap(true, false) {
		close(fs.entered)
		<-fs.release
	}
	return fs.quiesceFS.Create(name, category)
}

func TestOpen_HoldsTheLeaseWhileOpening(t *testing.T) {
	var gate quiesce.Gate
	fs := createGateFS{
		quiesceFS: quiesceFS{FS: vfs.NewMem(), gate: &gate},
		armed:     new(atomic.Bool),
		entered:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	fs.armed.Store(true)
	type opened struct {
		db  *pebble.DB
		w   *SealWatch
		err error
	}
	done := make(chan opened, 1)
	go func() {
		db, w, err := Open("db", &pebble.Options{FS: fs}, nil)
		done <- opened{db, w, err}
	}()
	<-fs.entered

	gate.Close()
	if got := heldBy(&gate); !slices.Equal(got, []string{"db"}) {
		t.Fatalf("held while opening = %v; want [db]", got)
	}
	drained := make(chan error, 1)
	go func() { drained <- gate.Drain(drainForever) }()
	select {
	case err := <-drained:
		t.Fatalf("Drain returned %v with pebble.Open in flight", err)
	default:
	}

	close(fs.release)
	o := <-done
	if o.err != nil {
		t.Fatalf("Open: %v", o.err)
	}
	t.Cleanup(func() {
		o.w.Close()
		_ = o.db.Close()
	})
	if err := <-drained; err != nil {
		t.Fatalf("Drain after Open = %v", err)
	}
	if got := heldBy(&gate); len(got) != 0 {
		t.Fatalf("held after Open = %v; want none", got)
	}
}

func TestOpen_RefusesOnAClosedGate(t *testing.T) {
	var gate quiesce.Gate
	gate.Close()
	db, w, err := Open("db", &pebble.Options{FS: quiesceFS{FS: vfs.NewMem(), gate: &gate}}, nil)
	var sealed *SealedError
	if !errors.As(err, &sealed) || !errors.Is(err, ErrBarrierSealed) {
		t.Fatalf("Open on a closed gate = %v; want a *SealedError", err)
	}
	if db != nil || w != nil {
		t.Fatal("a refused Open returned a database")
	}
	if got := heldBy(&gate); len(got) != 0 {
		t.Fatalf("held after a refused Open = %v; want none", got)
	}
}

func TestOpen_WithoutAGateRegistersNothing(t *testing.T) {
	db, w, err := Open("db", &pebble.Options{FS: vfs.NewMem()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if w.lease != nil {
		t.Fatal("a store with no gate holds a lease")
	}
	if err := w.Admit(); err != nil {
		t.Fatalf("Admit without a gate = %v", err)
	}
	w.Done()
}

func TestSealWatch_AdmitHoldsTheLeaseUntilDone(t *testing.T) {
	var gate quiesce.Gate
	_, w := openQuiesced(t, &gate)
	if err := w.Admit(); err != nil {
		t.Fatal(err)
	}
	gate.Close()
	if got := heldBy(&gate); len(got) != 1 {
		t.Fatalf("held = %v while admitted", got)
	}
	w.Done()
	if err := gate.Drain(-1); err != nil {
		t.Fatalf("Drain after Done = %v", err)
	}
}

func TestSealWatch_AdmitRefusesWithTheSealedErrorWhileSealing(t *testing.T) {
	var gate quiesce.Gate
	_, w := openQuiesced(t, &gate)
	gate.Close()
	var sealed *SealedError
	if err := w.Admit(); !errors.As(err, &sealed) || !errors.Is(err, ErrBarrierSealed) {
		t.Fatalf("Admit while sealing = %v; want a *SealedError", err)
	}
	if got := heldBy(&gate); len(got) != 0 {
		t.Fatalf("a refused Admit left a hold: %v", got)
	}
}

func TestSealWatch_AdmitReleasesWhenTheBarrierIsSealed(t *testing.T) {
	var gate quiesce.Gate
	_, w := openQuiesced(t, &gate)
	w.barrierSealed = func() bool { return true }
	if err := w.Admit(); !errors.Is(err, ErrBarrierSealed) {
		t.Fatalf("Admit under a sealed barrier = %v", err)
	}
	if got := heldBy(&gate); len(got) != 0 {
		t.Fatalf("a refused Admit left a hold: %v", got)
	}
}

func TestSealWatch_DoneWithoutALeaseIsSafe(t *testing.T) {
	w := NewSealWatch(NewLogger(nil), nil)
	w.Done()
	if err := w.Admit(); err != nil {
		t.Fatal(err)
	}
	w.Done()
}

func TestSealWatch_DoneWakesADrain(t *testing.T) {
	var gate quiesce.Gate
	_, w := openQuiesced(t, &gate)
	if err := w.Admit(); err != nil {
		t.Fatal(err)
	}
	gate.Close()
	drained := make(chan error, 1)
	go func() { drained <- gate.Drain(drainForever) }()
	w.Done()
	if err := <-drained; err != nil {
		t.Fatalf("Drain = %v", err)
	}
}

func TestSealWatch_CloseKeepsInFlightWorkInTheDrainUntilDone(t *testing.T) {
	var gate quiesce.Gate
	db, w, err := Open("db", &pebble.Options{FS: quiesceFS{FS: vfs.NewMem(), gate: &gate}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := w.Admit(); err != nil {
		t.Fatal(err)
	}
	w.Close()
	if got := heldBy(&gate); len(got) != 1 || got[0] != "db" {
		t.Fatalf("held = %v after Close with an operation in flight; want [db]", got)
	}
	var sealed *SealedError
	if err := w.Admit(); !errors.As(err, &sealed) {
		t.Fatalf("Admit after Close = %v; want a *SealedError", err)
	}
	w.Done()
	if got := heldBy(&gate); len(got) != 0 {
		t.Fatalf("held = %v after the in-flight operation's Done", got)
	}
	w.Close()
}

func TestSealWatch_CloseOnNilIsSafe(t *testing.T) {
	var w *SealWatch
	w.Close()
}

func TestSealWatch_FlushHoldsTheLease(t *testing.T) {
	var gate quiesce.Gate
	db, w := openQuiesced(t, &gate)
	if err := db.Set([]byte("k"), []byte("v"), pebble.NoSync); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(db); err != nil {
		t.Fatalf("Flush = %v", err)
	}
	if got := heldBy(&gate); len(got) != 0 {
		t.Fatalf("Flush left a hold: %v", got)
	}
	gate.Close()
	if err := w.Flush(db); !errors.Is(err, ErrBarrierSealed) {
		t.Fatalf("Flush while sealing = %v; want ErrBarrierSealed", err)
	}
}

func TestPebbleStorage_WritesAndCompactionsRefuseWhileSealing(t *testing.T) {
	var gate quiesce.Gate
	cfg := TestConfig("db")
	cfg.FS = quiesceFS{FS: vfs.NewMem(), gate: &gate}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatal(err)
	}
	gate.Close()
	var sealed *SealedError
	if err := s.Set([]byte("k2"), []byte("v")); !errors.As(err, &sealed) {
		t.Fatalf("Set while sealing = %v; want a *SealedError", err)
	}
	if err := s.Compact([]byte("a"), []byte("z")); !errors.As(err, &sealed) {
		t.Fatalf("Compact while sealing = %v; want a *SealedError", err)
	}
	gate.Reopen()
	if err := s.Set([]byte("k2"), []byte("v")); err != nil {
		t.Fatalf("Set after reopen = %v", err)
	}
	if err := s.Compact([]byte("a"), []byte("z")); err != nil {
		t.Fatalf("Compact after reopen = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := heldBy(&gate); len(got) != 0 {
		t.Fatalf("held = %v after Close", got)
	}
}

func TestNewFromDB_WithSealWatchLeasesWrites(t *testing.T) {
	var gate quiesce.Gate
	db, w := openQuiesced(t, &gate)
	s := NewFromDB(db, true, WithSealWatch(w))
	if err := s.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatal(err)
	}
	gate.Close()
	var sealed *SealedError
	if err := s.Set([]byte("k2"), []byte("v")); !errors.As(err, &sealed) {
		t.Fatalf("Set while sealing = %v; want a *SealedError", err)
	}
	gate.Reopen()

	// Without a watch the wrapper writes unleased, as before.
	gate.Close()
	defer gate.Reopen()
	if err := NewFromDB(db, true, WithSealWatch(nil)).Set([]byte("k3"), []byte("v")); err != nil {
		t.Fatalf("unwatched Set while sealing = %v", err)
	}
}
