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

package statemachine

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"
)

// pausingBackend is a ConcurrentBackend that stops in the middle of an
// applied batch: its Put of pauseKey announces itself on paused and waits
// for release before writing.
type pausingBackend struct {
	*ConcurrentBackend
	pauseKey string
	paused   chan struct{}
	release  chan struct{}
}

func (b *pausingBackend) Put(key, value []byte) error {
	if string(key) == b.pauseKey {
		close(b.paused)
		<-b.release
	}
	return b.ConcurrentBackend.Put(key, value)
}

// putEntry is a committed kv.OpPut entry for key and value.
func putEntry(index uint64, key, value string) sm.Entry {
	cmd := make([]byte, 0, 3+len(key)+len(value))
	cmd = append(cmd, kv.OpPut)
	cmd = binary.LittleEndian.AppendUint16(cmd, uint16(len(key)))
	cmd = append(cmd, key...)
	cmd = append(cmd, value...)
	return sm.Entry{Index: index, Cmd: cmd}
}

// TestKVStateMachine_GetHashSeesWholeBatches: go-quicraft's concurrent tier
// calls GetHash while Update applies a batch, and replicas compare the hash
// to detect divergence. A hash taken halfway through a batch is of a state no
// replica settles in, so GetHash waits for the batch and hashes it whole.
func TestKVStateMachine_GetHashSeesWholeBatches(t *testing.T) {
	backend := &pausingBackend{ConcurrentBackend: NewConcurrentBackend(), pauseKey: "k2", paused: make(chan struct{}), release: make(chan struct{})}
	k := NewKVStateMachine(backend)
	batch := []sm.Entry{putEntry(1, "k1", "v1"), putEntry(2, "k2", "v2")}

	applied := make(chan error, 1)
	go func() { applied <- k.Update(batch, make([]sm.Result, len(batch))) }()
	<-backend.paused

	hashed := make(chan uint64, 1)
	go func() {
		h, err := k.GetHash()
		if err != nil {
			t.Errorf("GetHash: %v", err)
		}
		hashed <- h
	}()
	select {
	case h := <-hashed:
		close(backend.release)
		<-applied
		t.Fatalf("GetHash returned %d halfway through a batch; it must wait for the whole batch", h)
	case <-time.After(100 * time.Millisecond):
	}
	close(backend.release)
	if err := <-applied; err != nil {
		t.Fatalf("Update: %v", err)
	}
	got := <-hashed

	whole := NewKVStateMachine(NewConcurrentBackend())
	if err := whole.Update(batch, make([]sm.Result, len(batch))); err != nil {
		t.Fatalf("Update: %v", err)
	}
	want, err := whole.GetHash()
	if err != nil {
		t.Fatalf("GetHash: %v", err)
	}
	if got != want {
		t.Fatalf("GetHash taken across a batch = %d, want the whole batch's %d", got, want)
	}
}

// pausingReader pauses after the first pauseAfter bytes read through it,
// announcing on paused and waiting for release before returning any
// further bytes -- the same handshake pausingBackend's Put gives a batch,
// applied to a snapshot stream's Read instead. It pauses only once: a
// recovery loop that happens to re-read is not this fixture's concern.
type pausingReader struct {
	io.Reader
	pauseAfter int
	read       int
	paused     chan struct{}
	release    chan struct{}
	firedPause bool
}

func (r *pausingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += n
	if !r.firedPause && r.read >= r.pauseAfter {
		r.firedPause = true
		close(r.paused)
		<-r.release
	}
	return n, err
}

// snapshotPauseAfter is the byte offset a pausingReader must pause at to
// land between a two-entry snapshot's first and second entries: 4 bytes for
// the entry count, plus one complete [keyLen:4][key][valLen:4][value]
// record. It assumes every entry in the snapshot encodes to the same
// length, which every test below arranges (two-character keys and values),
// so which of the two entries a map happened to iterate first never
// matters.
const snapshotPauseAfter = 4 + 4 + 2 + 4 + 2

// TestKVStateMachine_RecoverSnapshotFencesGetHash is
// TestKVStateMachine_GetHashSeesWholeBatches's counterpart for recovery
// (go-qrdb#584, same class as B1): a hash taken mid-recovery -- after
// RecoverSnapshot has cleared the backend but before every entry is
// restored -- is a state no replica settles in, exactly like a hash taken
// mid-Update. RecoverSnapshot now holds the batch lock exclusively for the
// whole recovery, so GetHash waits for it rather than racing it.
func TestKVStateMachine_RecoverSnapshotFencesGetHash(t *testing.T) {
	src := NewKVStateMachine(NewConcurrentBackend())
	batch := []sm.Entry{putEntry(1, "k1", "v1"), putEntry(2, "k2", "v2")}
	if err := src.Update(batch, make([]sm.Result, len(batch))); err != nil {
		t.Fatalf("Update: %v", err)
	}
	var buf bytes.Buffer
	if err := src.SaveSnapshot(&buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	k := NewKVStateMachine(NewConcurrentBackend())
	pr := &pausingReader{Reader: bytes.NewReader(buf.Bytes()), pauseAfter: snapshotPauseAfter, paused: make(chan struct{}), release: make(chan struct{})}

	recovered := make(chan error, 1)
	go func() { recovered <- k.RecoverSnapshot(pr, nil) }()
	<-pr.paused

	hashed := make(chan uint64, 1)
	go func() {
		h, err := k.GetHash()
		if err != nil {
			t.Errorf("GetHash: %v", err)
		}
		hashed <- h
	}()
	select {
	case h := <-hashed:
		close(pr.release)
		<-recovered
		t.Fatalf("GetHash returned %d mid-recovery; it must wait for the whole recovery", h)
	case <-time.After(100 * time.Millisecond):
	}
	close(pr.release)
	if err := <-recovered; err != nil {
		t.Fatalf("RecoverSnapshot: %v", err)
	}
	got := <-hashed

	want, err := src.GetHash()
	if err != nil {
		t.Fatalf("GetHash: %v", err)
	}
	if got != want {
		t.Fatalf("GetHash after recovery = %d, want the source's %d", got, want)
	}
}

// TestConcurrentStateMachine_RecoverFromSnapshotFencesGetHash is
// TestKVStateMachine_RecoverSnapshotFencesGetHash for
// ConcurrentStateMachine, which fences its own GetHash the same way
// (go-qrdb#584).
func TestConcurrentStateMachine_RecoverFromSnapshotFencesGetHash(t *testing.T) {
	src, _ := newTestConcurrentSM()
	concurrentApplyPut(t, src, "k1", "v1")
	concurrentApplyPut(t, src, "k2", "v2")
	snapCtx, err := src.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}
	var buf bytes.Buffer
	if err := src.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	csm, _ := newTestConcurrentSM()
	pr := &pausingReader{Reader: bytes.NewReader(buf.Bytes()), pauseAfter: snapshotPauseAfter, paused: make(chan struct{}), release: make(chan struct{})}

	recovered := make(chan error, 1)
	go func() { recovered <- csm.RecoverFromSnapshot(context.Background(), pr, nil) }()
	<-pr.paused

	hashed := make(chan uint64, 1)
	go func() {
		h, err := csm.GetHash()
		if err != nil {
			t.Errorf("GetHash: %v", err)
		}
		hashed <- h
	}()
	select {
	case h := <-hashed:
		close(pr.release)
		<-recovered
		t.Fatalf("GetHash returned %d mid-recovery; it must wait for the whole recovery", h)
	case <-time.After(100 * time.Millisecond):
	}
	close(pr.release)
	if err := <-recovered; err != nil {
		t.Fatalf("RecoverFromSnapshot: %v", err)
	}
	got := <-hashed

	want, err := src.GetHash()
	if err != nil {
		t.Fatalf("GetHash: %v", err)
	}
	if got != want {
		t.Fatalf("GetHash after recovery = %d, want the source's %d", got, want)
	}
}

// twoPointPausingBackend is a ConcurrentBackend that pauses Put twice: once
// on firstKey, to let a test synchronize on "the batch has started and this
// entry's lock, if any, is held", and once on lastKey, to let it synchronize
// on "every entry in between has already committed" before deciding
// anything about what ran concurrently with them. Both pauses block the
// caller (so whatever lock Put runs under is still held) until the test
// closes the matching release channel.
type twoPointPausingBackend struct {
	*ConcurrentBackend
	firstKey, lastKey         string
	pausedFirst, releaseFirst chan struct{}
	pausedLast, releaseLast   chan struct{}
}

func (b *twoPointPausingBackend) Put(key, value []byte) error {
	switch string(key) {
	case b.firstKey:
		close(b.pausedFirst)
		<-b.releaseFirst
	case b.lastKey:
		close(b.pausedLast)
		<-b.releaseLast
	}
	return b.ConcurrentBackend.Put(key, value)
}

// TestDiskAdapter_BatchLockHeldForWholeLoop is go-qrdb#584 B1's own red/green
// probe. DiskAdapter.Update applies one entry per call to
// KVStateMachine.applyEntries, inside its own per-entry loop; before this
// fix, that loop called KVStateMachine.Update per entry instead, which
// itself takes and releases the batch lock, so the lock was released and
// reacquired between every pair of entries DiskAdapter's own caller hands
// it as one batch -- a gap GetHash's exclusive Lock can land in, with
// nothing in a plain entry (no I/O) to deterministically pause on by
// itself.
//
// A concurrent TryLock probe is the only way to observe that gap from
// outside the package, but it must be started and stopped without racing
// Update's own start and finish -- a probe running before Update's first
// entry, or one still running the instant Update legitimately finishes and
// frees the lock, would acquire it too and look exactly like the bug. Both
// races are closed by pausing the FIRST and LAST entries of the batch (via
// twoPointPausingBackend): the probe starts only once the first pause
// confirms Update is already under way, runs freely across every entry in
// between -- where the bug's gaps actually are -- and is stopped and
// ACKNOWLEDGED stopped (probeStopped) before the last pause is ever
// released, so Update cannot complete, and so cannot free the lock
// legitimately, until this test has already read the probe's verdict.
//
// With the lock held once for the entire loop (this fix), TryLock cannot
// succeed even once in that window, however many times or however fast the
// probe tries -- a guarantee RWMutex itself gives, not a timing race -- so
// the assertion is exactly as deterministic in the direction that matters
// as the pause-based tests above are.
func TestDiskAdapter_BatchLockHeldForWholeLoop(t *testing.T) {
	const n = 1000
	backend := &twoPointPausingBackend{
		ConcurrentBackend: NewConcurrentBackend(),
		firstKey:          "k0", lastKey: "klast",
		pausedFirst: make(chan struct{}), releaseFirst: make(chan struct{}),
		pausedLast: make(chan struct{}), releaseLast: make(chan struct{}),
	}
	a := &DiskAdapter{KVStateMachine: NewKVStateMachine(backend), store: newMemStorage()}
	entries := make([]sm.Entry, n)
	entries[0] = putEntry(1, "k0", "v")
	for i := 1; i < n-1; i++ {
		entries[i] = putEntry(uint64(i+1), "k"+strconv.Itoa(i), "v")
	}
	entries[n-1] = putEntry(uint64(n), "klast", "v")
	results := make([]sm.Result, n)

	applied := make(chan error, 1)
	go func() { applied <- a.Update(context.Background(), entries, results) }()
	<-backend.pausedFirst

	stop := make(chan struct{})
	probeStopped := make(chan struct{})
	acquired := false
	go func() {
		defer close(probeStopped)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if a.batch.TryLock() {
				a.batch.Unlock()
				acquired = true
			}
		}
	}()

	close(backend.releaseFirst)
	<-backend.pausedLast // every entry between the first and the last has now committed
	close(stop)
	<-probeStopped // acquired is now safe to read: the probe goroutine is done writing it

	close(backend.releaseLast)
	if err := <-applied; err != nil {
		t.Fatalf("Update: %v", err)
	}
	if acquired {
		t.Fatal("a concurrent probe acquired the batch write lock while DiskAdapter.Update's own per-entry loop was still running; the lock must be held once for the whole loop, not reacquired per entry")
	}
}

// pausingWatch is a WatchManager that stops Update after its notification
// for pauseKey: announced on paused, held until release.
type pausingWatch struct {
	pauseKey string
	paused   chan struct{}
	release  chan struct{}
}

func (w *pausingWatch) Notify(key string, _ []byte, _ uint64, _ bool) {
	if key == w.pauseKey {
		close(w.paused)
		<-w.release
	}
}

// TestConcurrentStateMachine_GetHashSeesWholeBatches is
// TestKVStateMachine_GetHashSeesWholeBatches for the concurrent state
// machine, which go-quicraft hashes the same way while it applies.
func TestConcurrentStateMachine_GetHashSeesWholeBatches(t *testing.T) {
	watch := &pausingWatch{pauseKey: "k1", paused: make(chan struct{}), release: make(chan struct{})}
	csm, _ := newTestConcurrentSM(WithConcurrentWatchManager(watch))
	batch := []sm.Entry{putEntry(1, "k1", "v1"), putEntry(2, "k2", "v2")}

	applied := make(chan error, 1)
	go func() { applied <- csm.Update(context.Background(), batch, make([]sm.Result, len(batch))) }()
	<-watch.paused

	hashed := make(chan uint64, 1)
	go func() {
		h, err := csm.GetHash()
		if err != nil {
			t.Errorf("GetHash: %v", err)
		}
		hashed <- h
	}()
	select {
	case h := <-hashed:
		close(watch.release)
		<-applied
		t.Fatalf("GetHash returned %d halfway through a batch; it must wait for the whole batch", h)
	case <-time.After(100 * time.Millisecond):
	}
	close(watch.release)
	if err := <-applied; err != nil {
		t.Fatalf("Update: %v", err)
	}
	got := <-hashed

	whole, _ := newTestConcurrentSM()
	if err := whole.Update(context.Background(), batch, make([]sm.Result, len(batch))); err != nil {
		t.Fatalf("Update: %v", err)
	}
	want, err := whole.GetHash()
	if err != nil {
		t.Fatalf("GetHash: %v", err)
	}
	if got != want {
		t.Fatalf("GetHash taken across a batch = %d, want the whole batch's %d", got, want)
	}
}

// TestConcurrentTiers_ReadAndHashDuringUpdate proves go-quicraft's concurrent
// tier promise end to end for the sharded state machine: Lookup takes no
// part in the batch lock and answers immediately from whatever has already
// landed in the cache, while GetHash -- started the same way, against the
// same in-flight batch -- waits out the whole batch so replicas never
// compare a half-applied hash. Run under -race, it also proves neither path
// races the batch's own cache writes.
// TestKVStateMachine_LookupDoesNotWaitForABatch: only GetHash waits for a
// batch; a read still runs while Update applies one, which is what the
// concurrent tier is for.
func TestKVStateMachine_LookupDoesNotWaitForABatch(t *testing.T) {
	backend := &pausingBackend{ConcurrentBackend: NewConcurrentBackend(), pauseKey: "k2", paused: make(chan struct{}), release: make(chan struct{})}
	k := NewKVStateMachine(backend)
	batch := []sm.Entry{putEntry(1, "k1", "v1"), putEntry(2, "k2", "v2")}
	applied := make(chan error, 1)
	go func() { applied <- k.Update(batch, make([]sm.Result, len(batch))) }()
	<-backend.paused
	defer func() {
		close(backend.release)
		<-applied
	}()

	got, err := backend.Get([]byte("k1"))
	if err != nil || string(got) != "v1" {
		t.Fatalf("a read during a batch = %q, %v; want the applied k1", got, err)
	}
}
