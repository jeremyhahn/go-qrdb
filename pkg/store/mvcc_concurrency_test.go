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

package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/clock"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/mvcc"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/statedigest"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// waitBound is how long a test waits for an operation that must not depend
// on the stalled commit. It bounds a failure, not a success: a correct run
// finishes as soon as the operation does.
const waitBound = 10 * time.Second

// stallClock is the sync observer's clock. Once armed, the next synced commit
// to finish its sync blocks inside Since, after the batch is durable and
// before Commit returns, until the test releases it: an fsync as slow as the
// test needs, without sleeping.
type stallClock struct {
	clock.Clock
	armed   atomic.Bool
	stalled chan struct{}
	release chan struct{}
	once    sync.Once
}

func newStallClock() *stallClock {
	return &stallClock{
		Clock:   clock.Real(),
		stalled: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (c *stallClock) Since(t time.Time) time.Duration {
	if c.armed.CompareAndSwap(true, false) {
		close(c.stalled)
		<-c.release
	}
	return c.Clock.Since(t)
}

// unstall lets the stalled commit return; it is safe to call more than once.
func (c *stallClock) unstall() { c.once.Do(func() { close(c.release) }) }

// openStallable opens an MVCC state machine in dir whose synced commits are
// timed by clk, with hub attached when non-nil.
func openStallable(t *testing.T, dir string, clk clock.Clock, hub *watch.Hub) (*MVCCStateMachine, uint64) {
	t.Helper()
	s := NewMVCCStateMachine(nil)
	s.syncClock = clk
	if hub != nil {
		s.SetWatchHub(hub)
	}
	applied, err := s.Open(context.Background(), dir, make(chan struct{}))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s, applied
}

// startStalledUpdate arms clk and applies entries on another goroutine,
// returning once the commit is held inside its sync. The returned channel
// carries Update's error after clk is released.
func startStalledUpdate(t *testing.T, s *MVCCStateMachine, clk *stallClock, entries []sm.Entry) <-chan error {
	t.Helper()
	t.Cleanup(clk.unstall)
	clk.armed.Store(true)
	done := make(chan error, 1)
	go func() {
		done <- s.Update(context.Background(), entries, make([]sm.Result, len(entries)))
	}()
	select {
	case <-clk.stalled:
	case err := <-done:
		t.Fatalf("Update returned %v before its commit synced", err)
	case <-time.After(waitBound):
		t.Fatal("Update never reached its synced commit")
	}
	return done
}

// within runs op on another goroutine and fails the test if it has not
// returned within waitBound.
func within(t *testing.T, what string, op func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		op()
	}()
	select {
	case <-done:
	case <-time.After(waitBound):
		t.Fatalf("%s blocked behind the stalled commit", what)
	}
}

// committedRevision reads the revision revision-scoped reads are bounded by.
func committedRevision(t *testing.T, s *MVCCStateMachine) int64 {
	t.Helper()
	got, err := s.Lookup(context.Background(), service.MVCCRevisionQuery{})
	if err != nil {
		t.Fatalf("Lookup(MVCCRevisionQuery): %v", err)
	}
	return got.(int64)
}

// waitWriterQueued returns once a writer is queued on s.mu, which TryRLock
// then refuses.
func waitWriterQueued(s *MVCCStateMachine) {
	for s.mu.TryRLock() {
		s.mu.RUnlock()
		runtime.Gosched()
	}
}

// watchRlock makes s report, on queued, that a contended reader has started
// its lock waiter and is waiting on its ctx, and on waiterDone that the waiter
// has ended. Set it before the reader starts; it serves one contended reader.
func watchRlock(s *MVCCStateMachine) (queued, waiterDone <-chan struct{}) {
	q := make(chan struct{}, 1)
	d := make(chan struct{}, 1)
	s.rlockQueued = func() { q <- struct{}{} }
	s.rlockWaiterDone = func() { d <- struct{}{} }
	return q, d
}

// await fails the test unless ch delivers within waitBound.
func await(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(waitBound):
		t.Fatalf("%s did not happen within %v", what, waitBound)
	}
}

// TestMVCCConcurrency_ReadsCompleteDuringStalledCommit: a synced commit held
// in its fsync does not hold Lookup, GetHash or Sync, and the batch's
// revision is published only once the commit returns (go-qrdb#1287).
func TestMVCCConcurrency_ReadsCompleteDuringStalledCommit(t *testing.T) {
	clk := newStallClock()
	s, _ := openStallable(t, t.TempDir(), clk, nil)
	applyMVCCEntries(t, s, []sm.Entry{mvccPutEntry(1, []byte("k1"), []byte("v1"), 0)})

	done := startStalledUpdate(t, s, clk, []sm.Entry{mvccPutEntry(2, []byte("k2"), []byte("v2"), 0)})

	within(t, "Lookup", func() {
		got, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("k1")})
		if err != nil {
			t.Errorf("Lookup: %v", err)
			return
		}
		if kv := got.(*service.KeyValue); !bytes.Equal(kv.Value, []byte("v1")) {
			t.Errorf("Lookup(k1) = %q, want v1", kv.Value)
		}
	})
	within(t, "GetHash", func() {
		if _, err := s.GetHash(); err != nil {
			t.Errorf("GetHash: %v", err)
		}
	})
	within(t, "Sync", func() {
		if err := s.Sync(); err != nil {
			t.Errorf("Sync: %v", err)
		}
	})
	within(t, "the revision query", func() {
		if got := committedRevision(t, s); got != 1 {
			t.Errorf("committed revision during the stalled commit = %d, want 1: unpublished", got)
		}
	})

	clk.unstall()
	if err := <-done; err != nil {
		t.Fatalf("stalled Update: %v", err)
	}
	if got := committedRevision(t, s); got != 2 {
		t.Fatalf("committed revision after the commit = %d, want 2", got)
	}
}

// TestMVCCConcurrency_StateDigestDuringStalledCommit proves a state digest
// reads the published view, as Lookup and GetHash do: a batch stalled in its
// sync is in neither the digest's applied index nor its records until Update
// publishes it.
func TestMVCCConcurrency_StateDigestDuringStalledCommit(t *testing.T) {
	clk := newStallClock()
	s, _ := openStallable(t, t.TempDir(), clk, nil)
	applyMVCCEntries(t, s, []sm.Entry{mvccPutEntry(1, []byte("k1"), []byte("v1"), 0)})
	before := lookupStateDigest(t, s)
	hashBefore, err := s.GetHash()
	if err != nil {
		t.Fatalf("GetHash: %v", err)
	}
	if before.AppliedIndex != 1 {
		t.Fatalf("AppliedIndex before the stall = %d, want 1", before.AppliedIndex)
	}

	done := startStalledUpdate(t, s, clk, []sm.Entry{mvccPutEntry(2, []byte("k2"), []byte("v2"), 0)})

	within(t, "the state digest", func() {
		if d := lookupStateDigest(t, s); d != before {
			t.Errorf("digest during the stalled commit = %d/%d/%s, want %d/%d/%s: unpublished",
				d.AppliedIndex, d.Records, d.Hex(), before.AppliedIndex, before.Records, before.Hex())
		}
	})
	within(t, "GetHash", func() {
		if h, err := s.GetHash(); err != nil || h != hashBefore {
			t.Errorf("GetHash during the stalled commit = %#x, %v, want %#x: unpublished", h, err, hashBefore)
		}
	})

	clk.unstall()
	if err := <-done; err != nil {
		t.Fatalf("stalled Update: %v", err)
	}
	after := lookupStateDigest(t, s)
	if after.AppliedIndex != 2 || after.Records <= before.Records || after.Sum == before.Sum {
		t.Fatalf("digest after the commit = %d/%d/%s, want applied index 2 and the k2 record",
			after.AppliedIndex, after.Records, after.Hex())
	}
	if h, err := s.GetHash(); err != nil || h == hashBefore {
		t.Fatalf("GetHash after the commit = %#x, %v, want a change from %#x", h, err, hashBefore)
	}
}

// holdScans makes every full-state scan on s stop once it holds its view and
// has released s.mu: it offers the view on the returned channel, which has
// room for n, and waits until resume is called. A scan that finds the channel
// full does not wait to offer. The test's cleanup resumes the scans before
// openStallable's Close, which waits for them.
func holdScans(t *testing.T, s *MVCCStateMachine, n int) (held <-chan *readView, resume func()) {
	h := make(chan *readView, n)
	release := make(chan struct{})
	resume = sync.OnceFunc(func() { close(release) })
	t.Cleanup(resume)
	s.scanHeld = func(v *readView) {
		select {
		case h <- v:
		default:
		}
		<-release
	}
	return h, resume
}

// awaitView receives a held scan's view within waitBound.
func awaitView(t *testing.T, held <-chan *readView, what string) *readView {
	t.Helper()
	select {
	case v := <-held:
		return v
	case <-time.After(waitBound):
		t.Fatalf("%s did not reach its scan within %v", what, waitBound)
		return nil
	}
}

type lookupResult struct {
	got interface{}
	err error
}

// TestMVCCConcurrency_StateDigestDoesNotBlockPublish holds a state digest scan
// and a GetHash scan open and asserts that an Update publishes, and a point
// Lookup reads it, while they run: a full scan holds its view, not s.mu. The
// scans then report the view they captured, from before the Update.
func TestMVCCConcurrency_StateDigestDoesNotBlockPublish(t *testing.T) {
	s, _ := openStallable(t, t.TempDir(), clock.Real(), nil)
	applyMVCCEntries(t, s, []sm.Entry{mvccPutEntry(1, []byte("k"), []byte("v1"), 0)})
	before := lookupStateDigest(t, s)
	hashBefore, err := s.GetHash()
	if err != nil {
		t.Fatalf("GetHash: %v", err)
	}

	held, resume := holdScans(t, s, 2)
	digest := make(chan lookupResult, 1)
	go func() {
		got, err := s.Lookup(context.Background(), statedigest.Query{})
		digest <- lookupResult{got, err}
	}()
	type hashResult struct {
		h   uint64
		err error
	}
	hash := make(chan hashResult, 1)
	go func() {
		h, err := s.GetHash()
		hash <- hashResult{h, err}
	}()
	awaitView(t, held, "the first scan")
	awaitView(t, held, "the second scan")

	within(t, "Update beside the held scans", func() {
		applyMVCCEntries(t, s, []sm.Entry{mvccPutEntry(2, []byte("k"), []byte("v2"), 0)})
	})
	within(t, "Lookup beside the held scans", func() {
		got, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("k")})
		if err != nil {
			t.Errorf("Lookup(k): %v", err)
			return
		}
		if kv := got.(*service.KeyValue); !bytes.Equal(kv.Value, []byte("v2")) {
			t.Errorf("Lookup(k) beside the held scans = %q, want the published v2", kv.Value)
		}
	})

	resume()
	select {
	case r := <-digest:
		if r.err != nil {
			t.Fatalf("held digest scan: %v", r.err)
		}
		if d := r.got.(statedigest.Digest); d != before {
			t.Fatalf("held digest = %d/%d/%s, want the captured view %d/%d/%s",
				d.AppliedIndex, d.Records, d.Hex(), before.AppliedIndex, before.Records, before.Hex())
		}
	case <-time.After(waitBound):
		t.Fatal("the held digest scan did not end once resumed")
	}
	select {
	case r := <-hash:
		if r.err != nil || r.h != hashBefore {
			t.Fatalf("held GetHash = %#x, %v, want the captured view's %#x", r.h, r.err, hashBefore)
		}
	case <-time.After(waitBound):
		t.Fatal("the held GetHash scan did not end once resumed")
	}
	if after := lookupStateDigest(t, s); after.AppliedIndex != 2 || after.Sum == before.Sum {
		t.Fatalf("digest after the scans = %d/%s, want applied index 2 and a new sum", after.AppliedIndex, after.Hex())
	}
}

// TestMVCCConcurrency_CloseWaitsForDigestScan holds a state digest scan open
// across Close and asserts that Close does not close the database under the
// scan's snapshot, returns once the scan ends, and that a scan after it is
// refused.
func TestMVCCConcurrency_CloseWaitsForDigestScan(t *testing.T) {
	s, _ := openStallable(t, t.TempDir(), clock.Real(), nil)
	applyMVCCEntries(t, s, []sm.Entry{mvccPutEntry(1, []byte("k"), []byte("v1"), 0)})
	before := lookupStateDigest(t, s)

	held, resume := holdScans(t, s, 1)
	waiting := make(chan struct{}, 1)
	s.closeWaiting = func() { waiting <- struct{}{} }
	digest := make(chan lookupResult, 1)
	go func() {
		got, err := s.Lookup(context.Background(), statedigest.Query{})
		digest <- lookupResult{got, err}
	}()
	v := awaitView(t, held, "the digest scan")

	closed := make(chan error, 1)
	go func() { closed <- s.Close(context.Background()) }()
	await(t, waiting, "Close reaching its scan wait")
	select {
	case err := <-closed:
		t.Fatalf("Close returned %v while a scan held its view", err)
	default:
	}
	if _, err := v.snap.Get([]byte("k")); err != nil {
		t.Fatalf("held scan's snapshot unreadable while Close waits: %v", err)
	}

	resume()
	select {
	case r := <-digest:
		if r.err != nil {
			t.Fatalf("held digest scan: %v", r.err)
		}
		if d := r.got.(statedigest.Digest); d != before {
			t.Fatalf("held digest = %d/%d/%s, want %d/%d/%s",
				d.AppliedIndex, d.Records, d.Hex(), before.AppliedIndex, before.Records, before.Hex())
		}
	case <-time.After(waitBound):
		t.Fatal("the held digest scan did not end once resumed")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(waitBound):
		t.Fatal("Close did not return once the scan ended")
	}

	var notOpen *NotOpenError
	if _, err := s.Lookup(context.Background(), statedigest.Query{}); !errors.As(err, &notOpen) {
		t.Fatalf("digest after Close: err = %v, want NotOpenError", err)
	}
	if _, err := s.GetHash(); !errors.As(err, &notOpen) {
		t.Fatalf("GetHash after Close: err = %v, want NotOpenError", err)
	}
}

// TestMVCCConcurrency_LookupHonoursCtxWhileQueued: a Lookup queued behind a
// writer returns ctx's error, wrapped in ReadError, as soon as ctx ends, while
// a commit is still stalled in its fsync and Close is queued behind it.
func TestMVCCConcurrency_LookupHonoursCtxWhileQueued(t *testing.T) {
	clk := newStallClock()
	dir := t.TempDir()
	s, _ := openStallable(t, dir, clk, nil)
	applyMVCCEntries(t, s, []sm.Entry{mvccPutEntry(1, []byte("k1"), []byte("v1"), 0)})

	done := startStalledUpdate(t, s, clk, []sm.Entry{mvccPutEntry(2, []byte("k2"), []byte("v2"), 0)})
	closed := make(chan error, 1)
	go func() { closed <- s.Close(context.Background()) }()
	waitWriterQueued(s)

	queued, waiterDone := watchRlock(s)
	ctx, cancel := context.WithCancel(context.Background())
	lookup := make(chan error, 1)
	go func() {
		_, err := s.Lookup(ctx, service.MVCCGetQuery{Key: []byte("k1")})
		lookup <- err
	}()
	await(t, queued, "Lookup queueing behind the waiting writer")
	cancel()
	select {
	case err := <-lookup:
		var re *ReadError
		if !errors.As(err, &re) || !errors.Is(err, context.Canceled) {
			t.Fatalf("Lookup = %v, want a ReadError wrapping context.Canceled", err)
		}
	case <-time.After(waitBound):
		t.Fatal("Lookup ignored its cancelled ctx while queued")
	}

	// Close took the lock between the commit and its publication: Update says
	// the store closed, and the commit it made is durable all the same.
	clk.unstall()
	if err := <-done; !errors.As(err, new(*NotOpenError)) {
		t.Fatalf("Update after Close = %v, want NotOpenError", err)
	}
	if err := <-closed; err != nil {
		t.Fatalf("Close: %v", err)
	}
	await(t, waiterDone, "the abandoned waiter ending")
	reopened, applied := openStallable(t, dir, nil, nil)
	if applied != 2 {
		t.Fatalf("reopened at applied index %d, want 2", applied)
	}
	got, err := reopened.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("k2")})
	if err != nil {
		t.Fatalf("Lookup(k2) after reopen: %v", err)
	}
	if kv := got.(*service.KeyValue); !bytes.Equal(kv.Value, []byte("v2")) {
		t.Fatalf("Lookup(k2) after reopen = %q, want v2", kv.Value)
	}
}

// TestMVCCConcurrency_LookupAbandonedLockIsReleased: a Lookup that gives up
// waiting does not leak the read lock its waiter is later granted.
func TestMVCCConcurrency_LookupAbandonedLockIsReleased(t *testing.T) {
	s, _ := openStallable(t, t.TempDir(), nil, nil)

	s.mu.Lock()
	queued, waiterDone := watchRlock(s)
	ctx, cancel := context.WithCancel(context.Background())
	lookup := make(chan error, 1)
	go func() {
		_, err := s.Lookup(ctx, service.MVCCGetQuery{Key: []byte("k1")})
		lookup <- err
	}()
	await(t, queued, "Lookup queueing behind the write lock")
	cancel()
	select {
	case err := <-lookup:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Lookup = %v, want context.Canceled", err)
		}
	case <-time.After(waitBound):
		t.Fatal("Lookup ignored its cancelled ctx while queued")
	}

	// The waiter is still queued for the read lock; releasing the write lock
	// grants it, and with nobody left to take the grant it must give it back.
	s.mu.Unlock()
	await(t, waiterDone, "the abandoned waiter ending")
	if !s.mu.TryLock() {
		t.Fatal("the abandoned waiter leaked its read hold: a writer cannot take the lock")
	}
	s.mu.Unlock()
}

// TestMVCCConcurrency_LookupEndedCtxNeverQueues: a ctx already ended is
// refused before Lookup queues on the lock at all.
func TestMVCCConcurrency_LookupEndedCtxNeverQueues(t *testing.T) {
	s, _ := openStallable(t, t.TempDir(), nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), -time.Second)
	defer cancel()

	_, err := s.Lookup(ctx, service.MVCCGetQuery{Key: []byte("k1")})
	var re *ReadError
	if !errors.As(err, &re) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Lookup = %v, want a ReadError wrapping context.DeadlineExceeded", err)
	}
}

// TestMVCCConcurrency_CancelledReadersLeaveNoWaiters cancels many readers
// queued behind a held write lock and checks they leave no goroutine parked
// on it: the contended readers share one lock notifier, so while the writer
// still holds the lock at most one goroutine waits for it, however many
// readers gave up.
func TestMVCCConcurrency_CancelledReadersLeaveNoWaiters(t *testing.T) {
	const readers = 200
	s, _ := openStallable(t, t.TempDir(), nil, nil)

	s.mu.Lock()
	// A failure below must release the lock, or the store's Close cleanup
	// waits on it for ever.
	held := true
	t.Cleanup(func() {
		if held {
			s.mu.Unlock()
		}
	})
	baseline := runtime.NumGoroutine()
	var queued sync.WaitGroup
	queued.Add(readers)
	s.rlockQueued = queued.Done
	ctx, cancel := context.WithCancel(context.Background())
	lookups := make(chan error, readers)
	for range readers {
		go func() {
			_, err := s.Lookup(ctx, service.MVCCGetQuery{Key: []byte("k1")})
			lookups <- err
		}()
	}
	within(t, "every reader queueing behind the write lock", queued.Wait)
	cancel()
	for range readers {
		select {
		case err := <-lookups:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Lookup = %v, want context.Canceled", err)
			}
		case <-time.After(waitBound):
			t.Fatal("a Lookup ignored its cancelled ctx while queued")
		}
	}

	if n := s.rlockWaiters.Load(); n > 1 {
		t.Fatalf("%d lock waiters live after %d readers gave up, want at most 1", n, readers)
	}
	// Every reader goroutine has returned; what remains beyond the baseline
	// is the notifier and any storage background work, far below one
	// goroutine per cancelled reader.
	if grown := runtime.NumGoroutine() - baseline; grown >= readers/4 {
		t.Fatalf("%d goroutines outlive %d cancelled readers while the writer holds the lock", grown, readers)
	}

	waiterDone := make(chan struct{}, 1)
	s.rlockWaiterDone = func() { waiterDone <- struct{}{} }
	held = false
	s.mu.Unlock()
	await(t, waiterDone, "the lock notifier ending")
	if n := s.rlockWaiters.Load(); n != 0 {
		t.Fatalf("%d lock waiters live after the writer released, want 0", n)
	}
	if !s.mu.TryLock() {
		t.Fatal("the lock notifier leaked its read hold: a writer cannot take the lock")
	}
	s.mu.Unlock()
}

// TestMVCCConcurrency_UpdateReadsSyncRace runs Update beside Lookup, GetHash,
// Sync and the revision query, for the race detector, and checks every
// reader sees the committed revision only advance.
func TestMVCCConcurrency_UpdateReadsSyncRace(t *testing.T) {
	const applies = 40
	s, _ := openStallable(t, t.TempDir(), nil, nil)
	applyMVCCEntries(t, s, []sm.Entry{mvccPutEntry(1, []byte("k"), []byte("0"), 0)})

	stop := make(chan struct{})
	var readers sync.WaitGroup
	reader := func(read func() error) {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if err := read(); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	reader(func() error {
		_, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("k")})
		return err
	})
	reader(func() error {
		_, err := s.GetHash()
		return err
	})
	reader(s.Sync)
	var last int64
	reader(func() error {
		got, err := s.Lookup(context.Background(), service.MVCCRevisionQuery{})
		if err != nil {
			return err
		}
		if rev := got.(int64); rev < last {
			return errors.New("committed revision went back from " + strconv.FormatInt(last, 10) + " to " + strconv.FormatInt(rev, 10))
		} else {
			last = rev
		}
		return nil
	})

	for i := uint64(2); i <= applies; i++ {
		applyMVCCEntries(t, s, []sm.Entry{mvccPutEntry(i, []byte("k"), []byte(strconv.FormatUint(i, 10)), 0)})
	}
	close(stop)
	readers.Wait()

	if got := committedRevision(t, s); got != applies {
		t.Fatalf("committed revision = %d, want %d", got, applies)
	}
}

// TestMVCCConcurrency_PublishesInApplyOrder: watch events arrive in the order
// the batches were applied, while a reader runs beside every commit.
func TestMVCCConcurrency_PublishesInApplyOrder(t *testing.T) {
	const applies = 20
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	s, _ := openStallable(t, t.TempDir(), nil, hub)
	w, err := hub.WatchPrefix(context.Background(), "order/")
	if err != nil {
		t.Fatalf("WatchPrefix: %v", err)
	}

	stop := make(chan struct{})
	reading := make(chan struct{})
	go func() {
		defer close(reading)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = s.GetHash()
		}
	}()
	for i := uint64(1); i <= applies; i++ {
		applyMVCCEntries(t, s, []sm.Entry{mvccPutEntry(i, []byte("order/"+strconv.FormatUint(i, 10)), []byte("v"), 0)})
	}
	close(stop)
	<-reading

	var revs []int64
	for len(revs) < applies {
		for _, ev := range receiveEvents(t, w) {
			revs = append(revs, ev.Kv.ModRevision)
		}
	}
	for i, rev := range revs {
		if rev != int64(i+1) {
			t.Fatalf("watch revisions %v, want 1..%d in apply order", revs, applies)
		}
	}
}

// TestMVCCConcurrency_OutOfOrderApplyIsRefused: an Update whose sequence is
// not the next to publish is refused with ApplyOrderError rather than
// publishing its revision out of order.
func TestMVCCConcurrency_OutOfOrderApplyIsRefused(t *testing.T) {
	s, _ := openStallable(t, t.TempDir(), nil, nil)
	applyMVCCEntries(t, s, []sm.Entry{mvccPutEntry(1, []byte("k"), []byte("1"), 0)})

	// An Update that claimed a sequence and never published: the shape a
	// second, concurrent apply would leave.
	s.applySeq.Add(1)

	err := s.Update(context.Background(), []sm.Entry{mvccPutEntry(2, []byte("k"), []byte("2"), 0)}, make([]sm.Result, 1))
	var oe *ApplyOrderError
	if !errors.As(err, &oe) || oe.Seq != 3 || oe.Published != 1 {
		t.Fatalf("Update = %v, want ApplyOrderError{Seq: 3, Published: 1}", err)
	}
	if oe.Error() == "" {
		t.Fatal("ApplyOrderError has an empty message")
	}
	if got := committedRevision(t, s); got != 1 {
		t.Fatalf("committed revision = %d, want 1: the refused batch is unpublished", got)
	}
}

// TestMVCCConcurrency_ReopenAtDurableAppliedIndex: closing and reopening
// returns the applied index of the last committed batch, including one whose
// commit was stalled in its sync.
func TestMVCCConcurrency_ReopenAtDurableAppliedIndex(t *testing.T) {
	clk := newStallClock()
	dir := t.TempDir()
	s, applied := openStallable(t, dir, clk, nil)
	if applied != 0 {
		t.Fatalf("fresh store applied index = %d, want 0", applied)
	}
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("k"), []byte("1"), 0),
		mvccPutEntry(2, []byte("k"), []byte("2"), 0),
	})
	done := startStalledUpdate(t, s, clk, []sm.Entry{mvccPutEntry(3, []byte("k"), []byte("3"), 0)})
	clk.unstall()
	if err := <-done; err != nil {
		t.Fatalf("stalled Update: %v", err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, applied := openStallable(t, dir, nil, nil)
	if applied != 3 {
		t.Fatalf("reopened at applied index %d, want 3", applied)
	}
	if got := committedRevision(t, reopened); got != 3 {
		t.Fatalf("reopened committed revision = %d, want 3", got)
	}
}

// TestMVCCConcurrency_UpdateNotOpen: Update on a store never opened claims no
// apply sequence.
func TestMVCCConcurrency_UpdateNotOpen(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	err := s.Update(context.Background(), []sm.Entry{mvccPutEntry(1, []byte("k"), []byte("1"), 0)}, make([]sm.Result, 1))
	if !errors.As(err, new(*NotOpenError)) {
		t.Fatalf("Update = %v, want NotOpenError", err)
	}
	if got := s.applySeq.Load(); got != 0 {
		t.Fatalf("applySeq = %d, want 0", got)
	}
}

// walStallFS holds the next WAL sync, once armed, inside the sync itself:
// before the write reaches the disk, after Pebble's commit pipeline has
// already made the batch visible in live storage. The held sync then
// completes. It cannot fail the sync instead: Pebble v2 treats a failed WAL
// sync as a fatal commit error and ends the process, so a synced Commit never
// returns that failure.
type walStallFS struct {
	vfs.FS
	armed   atomic.Bool
	stalled chan struct{}
	release chan struct{}
	once    sync.Once
}

func newWALStallFS() *walStallFS {
	return &walStallFS{
		FS:      vfs.Default,
		stalled: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (fs *walStallFS) Create(name string, category vfs.DiskWriteCategory) (vfs.File, error) {
	return fs.wrap(name)(fs.FS.Create(name, category))
}

func (fs *walStallFS) ReuseForWrite(oldname, newname string, category vfs.DiskWriteCategory) (vfs.File, error) {
	return fs.wrap(newname)(fs.FS.ReuseForWrite(oldname, newname, category))
}

// wrap returns the function that holds the syncs of name when it is a WAL.
func (fs *walStallFS) wrap(name string) func(vfs.File, error) (vfs.File, error) {
	return func(f vfs.File, err error) (vfs.File, error) {
		if err != nil || !strings.HasSuffix(name, ".log") {
			return f, err
		}
		return &walStallFile{File: f, fs: fs}, nil
	}
}

// hold blocks an armed sync until unstall.
func (fs *walStallFS) hold() {
	if fs.armed.CompareAndSwap(true, false) {
		close(fs.stalled)
		<-fs.release
	}
}

// unstall lets the held sync finish; it is safe to call more than once.
func (fs *walStallFS) unstall() { fs.once.Do(func() { close(fs.release) }) }

type walStallFile struct {
	vfs.File
	fs *walStallFS
}

func (f *walStallFile) Sync() error {
	f.fs.hold()
	return f.File.Sync()
}

func (f *walStallFile) SyncData() error {
	f.fs.hold()
	return f.File.SyncData()
}

func (f *walStallFile) SyncTo(length int64) (bool, error) {
	f.fs.hold()
	return f.File.SyncTo(length)
}

// openWALStallable opens an MVCC state machine whose WAL syncs fs can hold.
func openWALStallable(t *testing.T, fs *walStallFS) *MVCCStateMachine {
	t.Helper()
	s := NewMVCCStateMachine(&pebble.Options{FS: fs})
	if _, err := s.Open(context.Background(), t.TempDir(), make(chan struct{})); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

// startWALStalledUpdate arms fs and applies entries on another goroutine,
// returning once the commit's WAL sync is held. The returned channel carries
// Update's error after fs is released.
func startWALStalledUpdate(t *testing.T, s *MVCCStateMachine, fs *walStallFS, entries []sm.Entry) <-chan error {
	t.Helper()
	t.Cleanup(fs.unstall)
	fs.armed.Store(true)
	done := make(chan error, 1)
	go func() {
		done <- s.Update(context.Background(), entries, make([]sm.Result, len(entries)))
	}()
	select {
	case <-fs.stalled:
	case err := <-done:
		t.Fatalf("Update returned %v before its WAL sync", err)
	case <-time.After(waitBound):
		t.Fatal("Update never reached its WAL sync")
	}
	return done
}

// assertKeyAbsent fails unless Lookup answers key as absent through both the
// MVCC Get query and the raw-key convenience query.
func assertKeyAbsent(t *testing.T, s *MVCCStateMachine, key, why string) {
	t.Helper()
	var nf *mvcc.KeyNotFoundError
	if got, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte(key)}); !errors.As(err, &nf) {
		t.Errorf("Lookup(MVCCGetQuery %s) = %v, %v; want KeyNotFoundError: %s", key, got, err, why)
	}
	if got, err := s.Lookup(context.Background(), []byte(key)); err != nil || got != nil {
		t.Errorf("Lookup(%s) = %v, %v; want nil: %s", key, got, err, why)
	}
}

// TestMVCCConcurrency_UnsyncedBatchInvisible: a batch whose WAL sync has not
// completed is invisible to Lookup, though Pebble already shows it in live
// storage, and is visible once Update publishes it (go-qrdb#1287).
func TestMVCCConcurrency_UnsyncedBatchInvisible(t *testing.T) {
	fs := newWALStallFS()
	s := openWALStallable(t, fs)
	applyMVCCEntries(t, s, []sm.Entry{mvccPutEntry(1, []byte("k1"), []byte("v1"), 0)})

	done := startWALStalledUpdate(t, s, fs, []sm.Entry{mvccPutEntry(2, []byte("k2"), []byte("v2"), 0)})
	within(t, "Lookup", func() {
		assertKeyAbsent(t, s, "k2", "the batch has not synced")
		if got := committedRevision(t, s); got != 1 {
			t.Errorf("committed revision during the held sync = %d, want 1", got)
		}
	})

	fs.unstall()
	if err := <-done; err != nil {
		t.Fatalf("stalled Update: %v", err)
	}
	got, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("k2")})
	if err != nil {
		t.Fatalf("Lookup(k2) after the commit: %v", err)
	}
	if kv := got.(*service.KeyValue); !bytes.Equal(kv.Value, []byte("v2")) {
		t.Fatalf("Lookup(k2) = %q, want v2", kv.Value)
	}
}

// TestMVCCConcurrency_UnsyncedIndexEntryInvisible holds a batch between
// storage visibility and publication and checks that a secondary-index
// query and scan answer from the published snapshot, as a KV read does: the
// held batch's index entry must not appear while its KV write is invisible,
// and must appear once the batch is published (#1287).
func TestMVCCConcurrency_UnsyncedIndexEntryInvisible(t *testing.T) {
	fs := newWALStallFS()
	s := openWALStallable(t, fs)
	applyMVCCEntries(t, s, []sm.Entry{
		indexCreateEntry(1, "name_idx", "name", false, false),
		mvccPutEntry(2, []byte("doc:1"), []byte(`{"name":"alice"}`), 0),
	})

	indexKeys := func() []string {
		t.Helper()
		got, err := s.Lookup(context.Background(), service.IndexQueryRequest{IndexName: "name_idx", Value: []byte("alice")})
		if err != nil {
			t.Fatalf("IndexQueryRequest: %v", err)
		}
		var keys []string
		for _, k := range got.([][]byte) {
			keys = append(keys, string(k))
		}
		sort.Strings(keys)
		return keys
	}
	scanKeys := func() []string {
		t.Helper()
		got, err := s.Lookup(context.Background(), service.IndexScanRequest{IndexName: "name_idx"})
		if err != nil {
			t.Fatalf("IndexScanRequest: %v", err)
		}
		var keys []string
		for _, ks := range got.(map[string][][]byte) {
			for _, k := range ks {
				keys = append(keys, string(k))
			}
		}
		sort.Strings(keys)
		return keys
	}

	done := startWALStalledUpdate(t, s, fs, []sm.Entry{mvccPutEntry(3, []byte("doc:2"), []byte(`{"name":"alice"}`), 0)})
	within(t, "index Lookup", func() {
		assertKeyAbsent(t, s, "doc:2", "the batch has not synced")
		if got := indexKeys(); !slices.Equal(got, []string{"doc:1"}) {
			t.Errorf("index query during the held sync = %q, want [doc:1]", got)
		}
		if got := scanKeys(); !slices.Equal(got, []string{"doc:1"}) {
			t.Errorf("index scan during the held sync = %q, want [doc:1]", got)
		}
	})

	fs.unstall()
	if err := <-done; err != nil {
		t.Fatalf("stalled Update: %v", err)
	}
	want := []string{"doc:1", "doc:2"}
	if got := indexKeys(); !slices.Equal(got, want) {
		t.Fatalf("index query after the commit = %q, want %q", got, want)
	}
	if got := scanKeys(); !slices.Equal(got, want) {
		t.Fatalf("index scan after the commit = %q, want %q", got, want)
	}
}

// TestMVCCConcurrency_UnsyncedTSPointInvisible is the time-series case of
// TestMVCCConcurrency_UnsyncedIndexEntryInvisible: a data point in a batch
// held between storage visibility and publication must not appear in a
// time-series query, and must once the batch is published.
func TestMVCCConcurrency_UnsyncedTSPointInvisible(t *testing.T) {
	fs := newWALStallFS()
	s := NewMVCCStateMachine(&pebble.Options{FS: fs})
	s.enableTimeSeries = true
	if _, err := s.Open(context.Background(), t.TempDir(), make(chan struct{})); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	at := time.Unix(1_700_000_000, 0).UTC()
	applyMVCCEntries(t, s, []sm.Entry{tsWriteEntry(1, "cpu.usage", at.UnixNano(), 1, nil)})
	points := func() int {
		t.Helper()
		got, err := s.Lookup(context.Background(), service.TSQueryRequest{
			Metric: "cpu.usage",
			Start:  at.Add(-time.Minute),
			End:    at.Add(time.Minute),
		})
		if err != nil {
			t.Fatalf("TSQueryRequest: %v", err)
		}
		return len(got.([]service.DataPoint))
	}

	done := startWALStalledUpdate(t, s, fs, []sm.Entry{tsWriteEntry(2, "cpu.usage", at.Add(time.Second).UnixNano(), 2, nil)})
	within(t, "time-series Lookup", func() {
		if got := points(); got != 1 {
			t.Errorf("time-series points during the held sync = %d, want 1", got)
		}
	})

	fs.unstall()
	if err := <-done; err != nil {
		t.Fatalf("stalled Update: %v", err)
	}
	if got := points(); got != 2 {
		t.Fatalf("time-series points after the commit = %d, want 2", got)
	}
}

// rangeKeys answers a Range over [start, end) and returns its keys.
func rangeKeys(t *testing.T, s *MVCCStateMachine, q service.MVCCRangeQuery) ([]string, *service.MVCCRangeResult) {
	t.Helper()
	got, err := s.Lookup(context.Background(), q)
	if err != nil {
		t.Fatalf("Lookup(%+v): %v", q, err)
	}
	res := got.(*service.MVCCRangeResult)
	keys := make([]string, len(res.KeyValues))
	for i, kv := range res.KeyValues {
		keys[i] = string(kv.Key)
	}
	return keys, res
}

// TestMVCCConcurrency_RangeDuringStalledDelete: while a batch deleting b and
// inserting d is committed but not yet published, Range answers the published
// revision with b still present and d absent, and a count-only Range counts
// the published keys alone (go-qrdb#1287).
func TestMVCCConcurrency_RangeDuringStalledDelete(t *testing.T) {
	clk := newStallClock()
	s, _ := openStallable(t, t.TempDir(), clk, nil)
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("a"), []byte("1"), 0),
		mvccPutEntry(2, []byte("b"), []byte("2"), 0),
		mvccPutEntry(3, []byte("c"), []byte("3"), 0),
	})
	full := service.MVCCRangeQuery{Start: []byte("a"), End: []byte("e")}
	count := service.MVCCRangeQuery{Start: []byte("a"), End: []byte("e"), CountOnly: true}

	done := startStalledUpdate(t, s, clk, []sm.Entry{
		mvccDeleteEntry(4, []byte("b")),
		mvccPutEntry(5, []byte("d"), []byte("4"), 0),
	})
	within(t, "Range", func() {
		keys, res := rangeKeys(t, s, full)
		if want := "a,b,c"; strings.Join(keys, ",") != want || res.Revision != 3 {
			t.Errorf("Range during the stalled commit = %v at %d, want [%s] at 3", keys, res.Revision, want)
		}
		if _, res := rangeKeys(t, s, count); res.Count != 3 || res.Revision != 3 {
			t.Errorf("count-only Range during the stalled commit = %d at %d, want 3 at 3", res.Count, res.Revision)
		}
	})

	clk.unstall()
	if err := <-done; err != nil {
		t.Fatalf("stalled Update: %v", err)
	}
	keys, res := rangeKeys(t, s, full)
	if want := "a,c,d"; strings.Join(keys, ",") != want || res.Revision != 5 {
		t.Fatalf("Range after the commit = %v at %d, want [%s] at 5", keys, res.Revision, want)
	}
}

// TestMVCCConcurrency_SupersededViewClosesOnLastRelease: a reader holding a
// view keeps reading its state after Update supersedes it, and the view's
// snapshot closes when that reader releases it.
func TestMVCCConcurrency_SupersededViewClosesOnLastRelease(t *testing.T) {
	s, _ := openStallable(t, t.TempDir(), clock.Real(), nil)
	applyMVCCEntries(t, s, []sm.Entry{mvccPutEntry(1, []byte("k"), []byte("v1"), 0)})

	held := s.acquireView()
	applyMVCCEntries(t, s, []sm.Entry{mvccPutEntry(2, []byte("k"), []byte("v2"), 0)})
	if s.view.Load() == held {
		t.Fatal("Update did not supersede the held view")
	}

	got, err := s.engine.LookupAt(context.Background(), held.snap, held.rev, service.MVCCGetQuery{Key: []byte("k")})
	if err != nil {
		t.Fatalf("LookupAt(held view): %v", err)
	}
	if kv := got.(*service.KeyValue); !bytes.Equal(kv.Value, []byte("v1")) || held.rev != 1 {
		t.Fatalf("held view reads %q at %d, want v1 at 1", kv.Value, held.rev)
	}

	held.release()
	if n := held.refs.Load(); n != 0 {
		t.Fatalf("superseded view refs after its last release = %d, want 0", n)
	}
	if _, err := held.snap.Get([]byte("k")); err == nil {
		t.Fatal("superseded view's snapshot still open after its last release")
	}
}

// TestMVCCConcurrency_ViewsReleasedUnderConcurrentReads: readers running
// beside a stream of Updates leave only the current view's snapshot open, and
// Close releases that one.
func TestMVCCConcurrency_ViewsReleasedUnderConcurrentReads(t *testing.T) {
	s, _ := openStallable(t, t.TempDir(), clock.Real(), nil)
	applyMVCCEntries(t, s, []sm.Entry{mvccPutEntry(1, []byte("k"), []byte("v"), 0)})

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := s.Lookup(context.Background(), service.MVCCRangeQuery{Start: []byte("k"), End: []byte("l")}); err != nil {
					t.Errorf("Lookup: %v", err)
					return
				}
				if _, err := s.GetHash(); err != nil {
					t.Errorf("GetHash: %v", err)
					return
				}
			}
		}()
	}
	for i := uint64(2); i <= 50; i++ {
		applyMVCCEntries(t, s, []sm.Entry{mvccPutEntry(i, []byte("k"), []byte(strconv.FormatUint(i, 10)), 0)})
	}
	close(stop)
	readers.Wait()

	if n := s.db.Metrics().Snapshots.Count; n != 1 {
		t.Fatalf("open snapshots after the readers stopped = %d, want 1 (the current view)", n)
	}
	cur := s.view.Load()
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if n := cur.refs.Load(); n != 0 {
		t.Fatalf("current view refs after Close = %d, want 0", n)
	}
	if s.acquireView() != nil {
		t.Fatal("a view is still published after Close")
	}
}

// snapshotRefusingStorage is storage whose Snapshot fails.
type snapshotRefusingStorage struct {
	storage.Storage
}

var errSnapshotRefused = errors.New("snapshot refused")

func (snapshotRefusingStorage) Snapshot() (storage.Snapshot, error) {
	return nil, errSnapshotRefused
}

// TestMVCCConcurrency_PublishFailureKeepsPreviousView: when Update cannot take
// the view of a committed batch it returns PublishError and publishes nothing:
// readers keep the previous state and revision.
func TestMVCCConcurrency_PublishFailureKeepsPreviousView(t *testing.T) {
	s, _ := openStallable(t, t.TempDir(), clock.Real(), nil)
	applyMVCCEntries(t, s, []sm.Entry{mvccPutEntry(1, []byte("k1"), []byte("v1"), 0)})
	s.mu.Lock()
	s.storage = snapshotRefusingStorage{Storage: s.storage}
	s.mu.Unlock()

	entries := []sm.Entry{mvccPutEntry(2, []byte("k2"), []byte("v2"), 0)}
	err := s.Update(context.Background(), entries, make([]sm.Result, len(entries)))
	var pe *PublishError
	if !errors.As(err, &pe) || !errors.Is(err, errSnapshotRefused) || pe.Rev != 2 {
		t.Fatalf("Update with a refused snapshot = %v, want PublishError at revision 2", err)
	}
	if !strings.Contains(err.Error(), "publish revision 2") {
		t.Fatalf("PublishError text %q does not name the revision", err.Error())
	}
	assertKeyAbsent(t, s, "k2", "its view was never published")
	if got := committedRevision(t, s); got != 1 {
		t.Fatalf("committed revision after the failed publication = %d, want 1", got)
	}
}

// openWALStallableWithLessor is openWALStallable with lr attached before Open.
func openWALStallableWithLessor(t *testing.T, fs *walStallFS, lr *lease.Lessor) *MVCCStateMachine {
	t.Helper()
	s := NewMVCCStateMachine(&pebble.Options{FS: fs})
	s.SetLessor(lr)
	if _, err := s.Open(context.Background(), t.TempDir(), make(chan struct{})); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		lr.Close()
		_ = s.Close(context.Background())
	})
	return s
}

// readBesideExclusiveBatch stalls entries at their WAL sync, asserts the
// batch holds s.mu exclusively there -- so no reader runs beside its build --
// and runs read beside it, returning what read saw once the batch published.
func readBesideExclusiveBatch(t *testing.T, s *MVCCStateMachine, fs *walStallFS, entries []sm.Entry, read func() string) string {
	t.Helper()
	done := startWALStalledUpdate(t, s, fs, entries)
	if s.mu.TryRLock() {
		s.mu.RUnlock()
		t.Error("the batch is built under the shared lock: a reader runs beside its build")
	}
	got := make(chan string, 1)
	go func() { got <- read() }()
	fs.unstall()
	if err := <-done; err != nil {
		t.Fatalf("stalled Update: %v", err)
	}
	select {
	case r := <-got:
		return r
	case <-time.After(waitBound):
		t.Fatal("the read beside the batch never completed")
	}
	return ""
}

// TestMVCCConcurrency_IndexRebuildIsAtomic: an index dropped and re-created
// under the same name over another field is replaced in the catalog while
// the batch is built. A query and a scan beside that build must answer from
// one catalog, the published one, never the new index over the old snapshot.
func TestMVCCConcurrency_IndexRebuildIsAtomic(t *testing.T) {
	fs := newWALStallFS()
	s := openWALStallable(t, fs)
	applyMVCCEntries(t, s, []sm.Entry{
		indexCreateEntry(1, "idx", "name", false, false),
		mvccPutEntry(2, []byte("doc:1"), []byte(`{"name":"alice","city":"paris"}`), 0),
	})

	read := func() string {
		q, qerr := s.Lookup(context.Background(), service.IndexQueryRequest{IndexName: "idx", Value: []byte("paris")})
		sc, serr := s.Lookup(context.Background(), service.IndexScanRequest{IndexName: "idx"})
		if qerr != nil || serr != nil {
			return fmt.Sprintf("query err %v, scan err %v", qerr, serr)
		}
		var scan []string
		for v, ks := range sc.(map[string][][]byte) {
			for _, k := range ks {
				scan = append(scan, v+"="+string(k))
			}
		}
		sort.Strings(scan)
		var keys []string
		for _, k := range q.([][]byte) {
			keys = append(keys, string(k))
		}
		return fmt.Sprintf("query %q scan %q", keys, scan)
	}

	got := readBesideExclusiveBatch(t, s, fs, []sm.Entry{
		indexDropEntry(3, "idx"),
		indexCreateEntry(4, "idx", "city", false, false),
	}, read)
	want := fmt.Sprintf("query %q scan %q", []string{"doc:1"}, []string{"paris=doc:1"})
	if got != want {
		t.Errorf("read beside the rebuild = %s, want %s", got, want)
	}
	if got := read(); got != want {
		t.Errorf("read after the rebuild = %s, want %s", got, want)
	}
}

// TestMVCCConcurrency_LeaseRevokeIsAtomic: Lessor.Revoke drops the lease
// while the batch deleting its keys is built. A TTL query and a read of the
// lease-bound key beside that build must agree: both before the revoke or
// both after it.
func TestMVCCConcurrency_LeaseRevokeIsAtomic(t *testing.T) {
	fs := newWALStallFS()
	s := openWALStallableWithLessor(t, fs, lease.NewLessor())
	res := applyMVCCEntries(t, s, []sm.Entry{{Index: 1, Cmd: mustEncode(protocol.EncodeLeaseGrant(300))}})
	leaseID := int64(res[0].Value)
	applyMVCCEntries(t, s, []sm.Entry{mvccPutEntry(2, []byte("session/a"), []byte("alive"), leaseID)})

	read := func() string {
		var lnf *service.LeaseNotFoundError
		var knf *mvcc.KeyNotFoundError
		_, terr := s.Lookup(context.Background(), service.LeaseTimeToLiveQuery{ID: leaseID})
		_, gerr := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("session/a")})
		return fmt.Sprintf("lease gone %t (%v), key gone %t (%v)", errors.As(terr, &lnf), terr, errors.As(gerr, &knf), gerr)
	}

	got := readBesideExclusiveBatch(t, s, fs, []sm.Entry{{Index: 3, Cmd: mustEncode(protocol.EncodeLeaseRevoke(leaseID))}}, read)
	want := read()
	if got != want {
		t.Errorf("read beside the revoke = %s, want %s", got, want)
	}
	var lnf *service.LeaseNotFoundError
	if _, err := s.Lookup(context.Background(), service.LeaseTimeToLiveQuery{ID: leaseID}); !errors.As(err, &lnf) {
		t.Errorf("TTL after the revoke = %v, want LeaseNotFoundError", err)
	}
	assertKeyAbsent(t, s, "session/a", "the revoke deleted it")
}

// TestMVCCConcurrency_StalledAuditAppend_VerifySeesConsistentChain stalls an
// audit append in its WAL sync and verifies the tenant's chain beside it.
// audit.Chain.Append advances the chain's in-memory head before the batch
// commits, and Verify reads the head from there rather than from the
// published snapshot, so a verify beside the append must wait for it: one
// that ran on the shared path would match the old events against the new head
// and report a healthy chain broken ("missing sequence").
func TestMVCCConcurrency_StalledAuditAppend_VerifySeesConsistentChain(t *testing.T) {
	fs := newWALStallFS()
	s := NewMVCCStateMachine(&pebble.Options{FS: fs})
	s.SetLogs(vlog.NewChain())
	if _, err := s.Open(context.Background(), t.TempDir(), make(chan struct{})); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	appendEntry := func(idx uint64, action string) sm.Entry {
		ev := audit.AuditEvent{
			TenantID:  "acme",
			Resource:  "barrier",
			Action:    action,
			Actor:     "operator",
			Timestamp: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
		}
		return sm.Entry{Index: idx, Cmd: mustEncode(protocol.EncodeAuditAppend("acme", ev))}
	}
	applyMVCCEntries(t, s, []sm.Entry{appendEntry(1, "unseal")})

	read := func() string {
		res, err := s.Lookup(context.Background(), audit.VerifyRequest{TenantID: "acme"})
		if err != nil {
			return "verify: " + err.Error()
		}
		report, ok := res.(*audit.ChainContinuityReport)
		if !ok {
			return fmt.Sprintf("verify result %T", res)
		}
		return fmt.Sprintf("verified %t, records %d, head %d, reason %q", report.Verified, report.Records, report.Head, report.Reason)
	}

	got := readBesideExclusiveBatch(t, s, fs, []sm.Entry{appendEntry(2, "seal")}, read)
	want := read()
	if got != want {
		t.Errorf("verify beside the append = %s, want %s", got, want)
	}
	if want != `verified true, records 2, head 2, reason ""` {
		t.Errorf("verify after the append = %s, want a verified two-record chain", want)
	}
}

// The same append, wrapped in an OpGuarded as a service write guard wraps
// it: handleGuarded dispatches the inner append directly, so it advances the
// chain head in memory exactly as the bare one does, and a verify beside the
// stalled batch must still see the chain as it was.
func TestMVCCConcurrency_StalledGuardedAuditAppend_VerifySeesConsistentChain(t *testing.T) {
	fs := newWALStallFS()
	s := NewMVCCStateMachine(&pebble.Options{FS: fs})
	s.SetLogs(vlog.NewChain())
	if _, err := s.Open(context.Background(), t.TempDir(), make(chan struct{})); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	appendEntry := func(idx uint64, action string) sm.Entry {
		ev := audit.AuditEvent{
			TenantID:  "acme",
			Resource:  "barrier",
			Action:    action,
			Actor:     "operator",
			Timestamp: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
		}
		fence := mustEncode(protocol.EncodeCompare([]byte("fence"), protocol.CompareTargetVersion, protocol.CompareResultEqual, nil, 0, 0, 0))
		inner := mustEncode(protocol.EncodeAuditAppend("acme", ev))
		return sm.Entry{Index: idx, Cmd: mustEncode(protocol.EncodeGuarded([][]byte{fence}, inner))}
	}
	applyMVCCEntries(t, s, []sm.Entry{appendEntry(1, "unseal")})

	read := func() string {
		res, err := s.Lookup(context.Background(), audit.VerifyRequest{TenantID: "acme"})
		if err != nil {
			return "verify: " + err.Error()
		}
		report, ok := res.(*audit.ChainContinuityReport)
		if !ok {
			return fmt.Sprintf("verify result %T", res)
		}
		return fmt.Sprintf("verified %t, records %d, head %d, reason %q", report.Verified, report.Records, report.Head, report.Reason)
	}

	got := readBesideExclusiveBatch(t, s, fs, []sm.Entry{appendEntry(2, "seal")}, read)
	want := read()
	if got != want {
		t.Errorf("verify beside the guarded append = %s, want %s", got, want)
	}
	if want != `verified true, records 2, head 2, reason ""` {
		t.Errorf("verify after the guarded append = %s, want a verified two-record chain", want)
	}
}

// TestMVCCConcurrency_CompactionIsAtomic: a compaction publishes its floor
// while the batch is built. A historical read beside that build must not see
// the new floor before the batch publishes: the batch holds the lock, and
// the read answers from the published, compacted state.
func TestMVCCConcurrency_CompactionIsAtomic(t *testing.T) {
	fs := newWALStallFS()
	s := openWALStallable(t, fs)
	for i, v := range []string{"v1", "v2", "v3"} {
		applyMVCCEntries(t, s, []sm.Entry{mvccPutEntry(uint64(i+1), []byte("key"), []byte(v), 0)})
	}

	read := func() string {
		var ce *mvcc.RevisionCompactedError
		_, err := s.Lookup(context.Background(), service.MVCCGetAtRevisionQuery{Key: []byte("key"), Revision: 1})
		return fmt.Sprintf("revision 1 compacted %t (%v)", errors.As(err, &ce), err)
	}

	got := readBesideExclusiveBatch(t, s, fs, []sm.Entry{mvccCompactEntry(4, 2)}, read)
	if want := read(); got != want {
		t.Errorf("read beside the compaction = %s, want %s", got, want)
	}
	var ce *mvcc.RevisionCompactedError
	if _, err := s.Lookup(context.Background(), service.MVCCGetAtRevisionQuery{Key: []byte("key"), Revision: 1}); !errors.As(err, &ce) {
		t.Errorf("read at revision 1 after the compaction = %v, want RevisionCompactedError", err)
	}
}

// TestMVCCConcurrency_PlainBatchKeepsSharedPath: a batch of plain KV writes,
// with no lease in the lessor, is built under the shared lock.
func TestMVCCConcurrency_PlainBatchKeepsSharedPath(t *testing.T) {
	s := openWALStallableWithLessor(t, newWALStallFS(), lease.NewLessor())
	entries := []sm.Entry{
		mvccPutEntry(1, []byte("a"), []byte("1"), 0),
		{Index: 2, Cmd: mustEncode(protocol.EncodeMVCCDelete([]byte("a")))},
	}
	if s.engine.RequiresExclusiveApply(entries) {
		t.Fatal("a plain KV batch takes the exclusive path")
	}
	if s.engine.RequiresExclusiveApply(nil) {
		t.Fatal("an empty batch takes the exclusive path")
	}
}

// TestMVCCConcurrency_SideStateBatchTakesExclusivePath: every batch kind that
// changes the index catalog, the lessor or the compaction floors in place,
// any write once a lease exists, and an undecodable entry are exclusive.
func TestMVCCConcurrency_SideStateBatchTakesExclusivePath(t *testing.T) {
	s := openWALStallableWithLessor(t, newWALStallFS(), lease.NewLessor())
	put := mvccPutEntry(9, []byte("a"), []byte("1"), 0)
	cases := map[string]sm.Entry{
		"index create":    indexCreateEntry(1, "idx", "name", false, false),
		"index drop":      indexDropEntry(1, "idx"),
		"lease grant":     {Index: 1, Cmd: mustEncode(protocol.EncodeLeaseGrant(60))},
		"lease revoke":    {Index: 1, Cmd: mustEncode(protocol.EncodeLeaseRevoke(7))},
		"compact":         mvccCompactEntry(1, 1),
		"undecodable cmd": {Index: 1, Cmd: nil},
	}
	for name, e := range cases {
		if !s.engine.RequiresExclusiveApply([]sm.Entry{put, e}) {
			t.Errorf("%s: the batch keeps the shared path", name)
		}
	}

	applyMVCCEntries(t, s, []sm.Entry{{Index: 1, Cmd: mustEncode(protocol.EncodeLeaseGrant(60))}})
	if !s.engine.RequiresExclusiveApply([]sm.Entry{put}) {
		t.Error("a write with a lease in the lessor keeps the shared path")
	}
}
