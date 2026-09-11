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

package database

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// gateStorage is a REAL storage.Storage -- every operation is delegated to an
// in-memory engine -- with one addition: onGet is invoked at the top of Get.
//
// It exists because a test double that merely reports "PutIfAbsent succeeded"
// proves nothing about atomicity. Running the observation inside the guarded
// operation's own read is what makes the window visible, so a test can assert
// that a competing write is BLOCKED there rather than assert it happened to
// lose a race (go-qrdb#489).
type gateStorage struct {
	storage.Storage

	onGet atomic.Pointer[func()]
}

func newGateStorage(t *testing.T) *gateStorage {
	t.Helper()
	inner := memory.NewStorage()
	t.Cleanup(func() { _ = inner.Close() })
	return &gateStorage{Storage: inner}
}

// hook installs fn as the observer run at the start of the next Get, and
// returns a function that removes it.
func (g *gateStorage) hook(fn func()) func() {
	g.onGet.Store(&fn)
	return func() { g.onGet.Store(nil) }
}

func (g *gateStorage) Get(key []byte) ([]byte, error) {
	if fn := g.onGet.Load(); fn != nil {
		(*fn)()
	}
	return g.Storage.Get(key)
}

// racers is the number of concurrent claimants used by the contention tests.
// It is well above GOMAXPROCS on any runner so goroutines genuinely overlap.
const racers = 64

// ---------------------------------------------------------------------------
// Capability
// ---------------------------------------------------------------------------

func TestStandaloneBackend_SatisfiesAtomicBackend(t *testing.T) {
	// The compile-time assertion lives beside the methods; this is the
	// runtime half, because BackendKVService discovers the capability by
	// type assertion on a database.Backend and that is what must succeed.
	var b Backend = newTestBackend(t)
	if _, ok := b.(AtomicBackend); !ok {
		t.Fatal("StandaloneBackend does not satisfy AtomicBackend as a database.Backend")
	}
}

func TestRemoteBackend_DoesNotSatisfyAtomicBackend(t *testing.T) {
	// The refusal path must stay reachable: RemoteBackend is the case
	// AtomicBackend's doc comment names as needing a wire operation first,
	// and it is why the capability is discovered by assertion at all.
	var b Backend = NewRemoteBackend(nil)
	if _, ok := b.(AtomicBackend); ok {
		t.Fatal("RemoteBackend satisfies AtomicBackend; the refusal path is unreachable")
	}
}

// ---------------------------------------------------------------------------
// Exactly one winner under real contention
// ---------------------------------------------------------------------------

func TestStandaloneBackend_PutIfAbsentOneWinnerUnderContention(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()
	key := []byte("claim")

	var stored atomic.Int64
	var winner atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ok, err := b.PutIfAbsent(ctx, key, []byte(strconv.Itoa(i)))
			if err != nil {
				t.Errorf("PutIfAbsent: unexpected error %v", err)
				return
			}
			if ok {
				stored.Add(1)
				winner.Store(int64(i))
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if got := stored.Load(); got != 1 {
		t.Fatalf("PutIfAbsent: %d callers stored, want exactly 1", got)
	}
	got, err := b.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: unexpected error %v", err)
	}
	if want := []byte(strconv.FormatInt(winner.Load(), 10)); !bytes.Equal(got, want) {
		t.Fatalf("Get: stored value %q is not the winner's %q", got, want)
	}
}

func TestStandaloneBackend_CompareAndSwapOneWinnerUnderContention(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()
	key := []byte("swap")
	expected := []byte("v0")
	if err := b.Put(ctx, key, expected); err != nil {
		t.Fatalf("Put: %v", err)
	}

	var swapped atomic.Int64
	var winner atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ok, err := b.CompareAndSwap(ctx, key, expected, []byte("next"+strconv.Itoa(i)))
			if err != nil {
				t.Errorf("CompareAndSwap: unexpected error %v", err)
				return
			}
			if ok {
				swapped.Add(1)
				winner.Store(int64(i))
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if got := swapped.Load(); got != 1 {
		t.Fatalf("CompareAndSwap: %d callers swapped, want exactly 1", got)
	}
	got, err := b.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: unexpected error %v", err)
	}
	if want := []byte("next" + strconv.FormatInt(winner.Load(), 10)); !bytes.Equal(got, want) {
		t.Fatalf("Get: stored value %q is not the winner's %q", got, want)
	}
}

func TestStandaloneBackend_CompareAndDeleteOneWinnerUnderContention(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()
	key := []byte("release")
	expected := []byte("held")
	if err := b.Put(ctx, key, expected); err != nil {
		t.Fatalf("Put: %v", err)
	}

	var deleted atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := b.CompareAndDelete(ctx, key, expected)
			if err != nil {
				t.Errorf("CompareAndDelete: unexpected error %v", err)
				return
			}
			if ok {
				deleted.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := deleted.Load(); got != 1 {
		t.Fatalf("CompareAndDelete: %d callers deleted, want exactly 1", got)
	}
	got, err := b.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: unexpected error %v", err)
	}
	if got != nil {
		t.Fatalf("Get: key still holds %q after the guarded delete", got)
	}
}

// ---------------------------------------------------------------------------
// The unguarded writes cannot land inside a guarded window
//
// Each of these starts a competing unguarded write from INSIDE the guarded
// operation's read, then asserts it has not completed while the guard holds
// the lock. That is the property the whole design rests on: a guarantee that
// only holds when callers cooperate is advisory, and advisory is worse than a
// refusal because a refusal is honest.
// ---------------------------------------------------------------------------

func TestStandaloneBackend_PutCannotLandInsideGuardedWindow(t *testing.T) {
	g := newGateStorage(t)
	b := NewStandaloneBackend(g)
	ctx := context.Background()
	key := []byte("window")

	done := make(chan struct{})
	observed := make(chan bool, 1)
	unhook := g.hook(func() {
		go func() {
			if err := b.Put(ctx, key, []byte("intruder")); err != nil {
				t.Errorf("Put: unexpected error %v", err)
			}
			close(done)
		}()
		select {
		case <-done:
			observed <- false
		case <-time.After(50 * time.Millisecond):
			observed <- true
		}
	})

	stored, err := b.PutIfAbsent(ctx, key, []byte("guarded"))
	unhook()
	if !<-observed {
		t.Fatal("Put completed INSIDE the guarded window: the guard is advisory, not atomic")
	}
	<-done
	if err != nil {
		t.Fatalf("PutIfAbsent: unexpected error %v", err)
	}
	if !stored {
		t.Fatal("PutIfAbsent: did not store into an empty key")
	}

	// The intruder is not lost, only ordered after the guard.
	got, err := b.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: unexpected error %v", err)
	}
	if !bytes.Equal(got, []byte("intruder")) {
		t.Fatalf("Get: got %q, want the intruder's write applied after the guard", got)
	}
}

func TestStandaloneBackend_DeleteCannotLandInsideGuardedWindow(t *testing.T) {
	g := newGateStorage(t)
	b := NewStandaloneBackend(g)
	ctx := context.Background()
	key := []byte("window")
	if err := b.Put(ctx, key, []byte("v0")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	done := make(chan struct{})
	observed := make(chan bool, 1)
	unhook := g.hook(func() {
		go func() {
			if err := b.Delete(ctx, key); err != nil {
				t.Errorf("Delete: unexpected error %v", err)
			}
			close(done)
		}()
		select {
		case <-done:
			observed <- false
		case <-time.After(50 * time.Millisecond):
			observed <- true
		}
	})

	swapped, err := b.CompareAndSwap(ctx, key, []byte("v0"), []byte("v1"))
	unhook()
	if !<-observed {
		t.Fatal("Delete completed INSIDE the guarded window: the swap compared a value another writer had already removed")
	}
	<-done
	if err != nil {
		t.Fatalf("CompareAndSwap: unexpected error %v", err)
	}
	if !swapped {
		t.Fatal("CompareAndSwap: did not swap a value that still matched")
	}
}

func TestStandaloneBackend_BatchCannotLandInsideGuardedWindow(t *testing.T) {
	g := newGateStorage(t)
	b := NewStandaloneBackend(g)
	ctx := context.Background()
	key := []byte("window")

	done := make(chan struct{})
	observed := make(chan bool, 1)
	unhook := g.hook(func() {
		go func() {
			ops := []BatchOp{{Op: BatchOpPut, Key: key, Value: []byte("batched")}}
			if err := b.Batch(ctx, ops); err != nil {
				t.Errorf("Batch: unexpected error %v", err)
			}
			close(done)
		}()
		select {
		case <-done:
			observed <- false
		case <-time.After(50 * time.Millisecond):
			observed <- true
		}
	})

	stored, err := b.PutIfAbsent(ctx, key, []byte("guarded"))
	unhook()
	if !<-observed {
		t.Fatal("Batch committed INSIDE the guarded window: a batch can overwrite what a guarded create decided")
	}
	<-done
	if err != nil {
		t.Fatalf("PutIfAbsent: unexpected error %v", err)
	}
	if !stored {
		t.Fatal("PutIfAbsent: did not store into an empty key")
	}
}

// ---------------------------------------------------------------------------
// Single-caller semantics
// ---------------------------------------------------------------------------

func TestStandaloneBackend_PutIfAbsentDeclinesOccupiedKey(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()
	key := []byte("occupied")
	if err := b.Put(ctx, key, []byte("first")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	stored, err := b.PutIfAbsent(ctx, key, []byte("second"))
	if err != nil {
		t.Fatalf("PutIfAbsent: unexpected error %v", err)
	}
	if stored {
		t.Fatal("PutIfAbsent: reported a store into an occupied key")
	}
	got, err := b.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, []byte("first")) {
		t.Fatalf("Get: got %q, want the store left unchanged at %q", got, "first")
	}
}

func TestStandaloneBackend_CompareAndSwapDoesNotCreate(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	swapped, err := b.CompareAndSwap(context.Background(), []byte("absent"), []byte("v0"), []byte("v1"))
	if err != nil {
		t.Fatalf("CompareAndSwap: unexpected error %v", err)
	}
	if swapped {
		t.Fatal("CompareAndSwap: created an absent key; a swap is not a create")
	}
	got, err := b.Get(ctx, []byte("absent"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatalf("Get: key now holds %q; it must still be absent", got)
	}
}

func TestStandaloneBackend_CompareAndSwapDeclinesMismatch(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()
	key := []byte("mismatch")
	if err := b.Put(ctx, key, []byte("actual")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	swapped, err := b.CompareAndSwap(ctx, key, []byte("stale"), []byte("next"))
	if err != nil {
		t.Fatalf("CompareAndSwap: unexpected error %v", err)
	}
	if swapped {
		t.Fatal("CompareAndSwap: swapped on a stale expectation")
	}
	got, err := b.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, []byte("actual")) {
		t.Fatalf("Get: got %q, want %q", got, "actual")
	}
}

func TestStandaloneBackend_CompareAndDeleteDeclinesMismatchAndAbsent(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	deleted, err := b.CompareAndDelete(ctx, []byte("absent"), []byte("v0"))
	if err != nil {
		t.Fatalf("CompareAndDelete (absent): unexpected error %v", err)
	}
	if deleted {
		t.Fatal("CompareAndDelete: reported a delete of an absent key")
	}

	key := []byte("present")
	if err := b.Put(ctx, key, []byte("actual")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	deleted, err = b.CompareAndDelete(ctx, key, []byte("stale"))
	if err != nil {
		t.Fatalf("CompareAndDelete (mismatch): unexpected error %v", err)
	}
	if deleted {
		t.Fatal("CompareAndDelete: deleted on a stale expectation")
	}
	got, err := b.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, []byte("actual")) {
		t.Fatalf("Get: got %q, want the value left in place", got)
	}
}

// ---------------------------------------------------------------------------
// Error paths
// ---------------------------------------------------------------------------

// errStorage is an in-memory engine whose Get and Set can be made to fail, so
// the guarded verbs' error returns are exercised against a store that is
// otherwise real.
type errStorage struct {
	storage.Storage

	getErr    error
	setErr    error
	deleteErr error
}

func (e *errStorage) Get(key []byte) ([]byte, error) {
	if e.getErr != nil {
		return nil, e.getErr
	}
	return e.Storage.Get(key)
}

func (e *errStorage) Set(key, value []byte) error {
	if e.setErr != nil {
		return e.setErr
	}
	return e.Storage.Set(key, value)
}

func (e *errStorage) Delete(key []byte) error {
	if e.deleteErr != nil {
		return e.deleteErr
	}
	return e.Storage.Delete(key)
}

func newErrStorage(t *testing.T) *errStorage {
	t.Helper()
	inner := memory.NewStorage()
	t.Cleanup(func() { _ = inner.Close() })
	return &errStorage{Storage: inner}
}

func TestStandaloneBackend_GuardedVerbsPropagateStoreErrors(t *testing.T) {
	readFailure := errors.New("read failed")
	writeFailure := errors.New("write failed")

	tests := []struct {
		name  string
		setup func(*errStorage)
		seed  bool
		call  func(context.Context, *StandaloneBackend) (bool, error)
		want  error
	}{
		{
			name:  "PutIfAbsent read fails",
			setup: func(s *errStorage) { s.getErr = readFailure },
			call: func(ctx context.Context, b *StandaloneBackend) (bool, error) {
				return b.PutIfAbsent(ctx, []byte("k"), []byte("v"))
			},
			want: readFailure,
		},
		{
			name:  "PutIfAbsent write fails",
			setup: func(s *errStorage) { s.setErr = writeFailure },
			call: func(ctx context.Context, b *StandaloneBackend) (bool, error) {
				return b.PutIfAbsent(ctx, []byte("k"), []byte("v"))
			},
			want: writeFailure,
		},
		{
			name:  "CompareAndSwap read fails",
			setup: func(s *errStorage) { s.getErr = readFailure },
			call: func(ctx context.Context, b *StandaloneBackend) (bool, error) {
				return b.CompareAndSwap(ctx, []byte("k"), []byte("v0"), []byte("v1"))
			},
			want: readFailure,
		},
		{
			name:  "CompareAndSwap write fails",
			seed:  true,
			setup: func(s *errStorage) { s.setErr = writeFailure },
			call: func(ctx context.Context, b *StandaloneBackend) (bool, error) {
				return b.CompareAndSwap(ctx, []byte("k"), []byte("v0"), []byte("v1"))
			},
			want: writeFailure,
		},
		{
			name:  "CompareAndDelete read fails",
			setup: func(s *errStorage) { s.getErr = readFailure },
			call: func(ctx context.Context, b *StandaloneBackend) (bool, error) {
				return b.CompareAndDelete(ctx, []byte("k"), []byte("v0"))
			},
			want: readFailure,
		},
		{
			name:  "CompareAndDelete delete fails",
			seed:  true,
			setup: func(s *errStorage) { s.deleteErr = writeFailure },
			call: func(ctx context.Context, b *StandaloneBackend) (bool, error) {
				return b.CompareAndDelete(ctx, []byte("k"), []byte("v0"))
			},
			want: writeFailure,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newErrStorage(t)
			b := NewStandaloneBackend(s)
			ctx := context.Background()
			if tc.seed {
				if err := b.Put(ctx, []byte("k"), []byte("v0")); err != nil {
					t.Fatalf("seed Put: %v", err)
				}
			}
			tc.setup(s)

			ok, err := tc.call(ctx, b)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got error %v, want %v", err, tc.want)
			}
			if ok {
				t.Fatal("a failed guarded verb reported success")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Batch keeps its documented atomicity
// ---------------------------------------------------------------------------

func TestStandaloneBackend_BatchStaysAllOrNothingUnderConcurrentGuards(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	const batches = 32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < batches; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ops := []BatchOp{
				{Op: BatchOpPut, Key: []byte("a/" + strconv.Itoa(i)), Value: []byte("x")},
				{Op: BatchOpPut, Key: []byte("b/" + strconv.Itoa(i)), Value: []byte("x")},
			}
			if err := b.Batch(ctx, ops); err != nil {
				t.Errorf("Batch: unexpected error %v", err)
			}
		}(i)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if _, err := b.PutIfAbsent(ctx, []byte("guard/"+strconv.Itoa(i)), []byte("x")); err != nil {
				t.Errorf("PutIfAbsent: unexpected error %v", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	// Every batch applied both halves or neither; none applied one.
	for i := 0; i < batches; i++ {
		a, err := b.Get(ctx, []byte("a/"+strconv.Itoa(i)))
		if err != nil {
			t.Fatalf("Get a/%d: %v", i, err)
		}
		bb, err := b.Get(ctx, []byte("b/"+strconv.Itoa(i)))
		if err != nil {
			t.Fatalf("Get b/%d: %v", i, err)
		}
		if (a == nil) != (bb == nil) {
			t.Fatalf("batch %d applied only one half: a=%q b=%q", i, a, bb)
		}
		if a == nil {
			t.Fatalf("batch %d applied neither half", i)
		}
	}
}

// ---------------------------------------------------------------------------
// The composition the dedicated-store path actually builds
// ---------------------------------------------------------------------------

func TestBarrierBackendOverStandalone_SatisfiesAtomicBackend(t *testing.T) {
	// buildDedicatedStoreServices wraps the standalone backend in a barrier,
	// and BarrierBackend satisfies AtomicBackend only when its inner backend
	// does -- so the composition, not either half alone, is what must assert.
	var b Backend = NewBarrierBackend(newTestBackend(t), &xorBarrier{mask: 0x5A})
	if _, ok := b.(AtomicBackend); !ok {
		t.Fatal("BarrierBackend over StandaloneBackend does not satisfy AtomicBackend")
	}
}

func TestBarrierBackendOverStandalone_PutIfAbsentOneWinner(t *testing.T) {
	b := NewBarrierBackend(newTestBackend(t), &xorBarrier{mask: 0x5A})
	ctx := context.Background()
	key := []byte("claim")

	var stored atomic.Int64
	var winner atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ok, err := b.PutIfAbsent(ctx, key, []byte(strconv.Itoa(i)))
			if err != nil {
				t.Errorf("PutIfAbsent: unexpected error %v", err)
				return
			}
			if ok {
				stored.Add(1)
				winner.Store(int64(i))
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if got := stored.Load(); got != 1 {
		t.Fatalf("PutIfAbsent: %d callers stored, want exactly 1", got)
	}
	got, err := b.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if want := []byte(strconv.FormatInt(winner.Load(), 10)); !bytes.Equal(got, want) {
		t.Fatalf("Get: decrypted %q, want the winner's %q", got, want)
	}
}

func TestBarrierBackendOverStandalone_CompareAndSwapOneWinner(t *testing.T) {
	b := NewBarrierBackend(newTestBackend(t), &xorBarrier{mask: 0x5A})
	ctx := context.Background()
	key := []byte("swap")
	if err := b.Put(ctx, key, []byte("v0")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	var swapped atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ok, err := b.CompareAndSwap(ctx, key, []byte("v0"), []byte("next"+strconv.Itoa(i)))
			if err != nil {
				t.Errorf("CompareAndSwap: unexpected error %v", err)
				return
			}
			if ok {
				swapped.Add(1)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if got := swapped.Load(); got != 1 {
		t.Fatalf("CompareAndSwap: %d callers swapped, want exactly 1", got)
	}
}
