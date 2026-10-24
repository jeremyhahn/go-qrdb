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
	"errors"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// atomicBackends is every Backend in the package, each over fresh storage.
// Backend.ApplyAtomic is a required method, so a backend missing from this
// list is one whose all-or-nothing promise nothing in this file checks.
func atomicBackends(t *testing.T) map[string]func() Backend {
	t.Helper()
	return map[string]func() Backend{
		"standard":   func() Backend { return NewStandardBackend() },
		"concurrent": func() Backend { return NewConcurrentBackend() },
		"highperf":   func() Backend { return NewHighPerfBackend(nil) },
		"fast": func() Backend {
			b := NewFastBackend(1, 1, newMemStorage(), nil)
			t.Cleanup(func() { _ = b.Close() })
			return b
		},
		"disk": func() Backend { return NewDiskBackend(newMemStorage()) },
	}
}

func put(key, value string) Mutation { return Mutation{Key: []byte(key), Value: []byte(value)} }
func del(key string) Mutation        { return Mutation{Key: []byte(key), Delete: true} }

func mustGet(t *testing.T, b Backend, key string) []byte {
	t.Helper()
	v, err := b.Get([]byte(key))
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	return v
}

// TestApplyAtomic_AppliesTheSetInOrder pins the contract every backend
// shares: puts and deletes land, a later mutation of a key wins over an
// earlier one in the same set, a stored empty value stays distinguishable from
// an absent key, and an empty set changes nothing.
func TestApplyAtomic_AppliesTheSetInOrder(t *testing.T) {
	for name, newBackend := range atomicBackends(t) {
		t.Run(name, func(t *testing.T) {
			b := newBackend()
			if err := b.PutDurable([]byte("gone"), []byte("x")); err != nil {
				t.Fatal(err)
			}

			err := b.ApplyAtomic([]Mutation{
				put("a", "1"),
				put("b", "first"),
				del("gone"),
				put("b", "second"),
				put("empty", ""),
				put("c", "doomed"),
				del("c"),
			})
			if err != nil {
				t.Fatalf("ApplyAtomic: %v", err)
			}

			if got := mustGet(t, b, "a"); string(got) != "1" {
				t.Errorf("a = %q; want 1", got)
			}
			if got := mustGet(t, b, "b"); string(got) != "second" {
				t.Errorf("b = %q; want the LATER mutation, second", got)
			}
			if got := mustGet(t, b, "gone"); got != nil {
				t.Errorf("gone = %q; want absent after its delete", got)
			}
			if got := mustGet(t, b, "c"); got != nil {
				t.Errorf("c = %q; a delete after a put in the same set must win", got)
			}
			if got := mustGet(t, b, "empty"); got == nil || len(got) != 0 {
				t.Errorf("empty = %#v; want a PRESENT empty value, not an absent key", got)
			}

			if err := b.ApplyAtomic(nil); err != nil {
				t.Fatalf("ApplyAtomic(empty): %v", err)
			}
			if got := mustGet(t, b, "a"); string(got) != "1" {
				t.Errorf("an empty set changed a to %q", got)
			}
		})
	}
}

// TestApplyAtomic_DoesNotRetainTheCallersBuffers pins the copy: apply
// payloads are recycled buffers, and a backend that kept the caller's slices
// would change a committed value when the buffer is reused.
func TestApplyAtomic_DoesNotRetainTheCallersBuffers(t *testing.T) {
	for name, newBackend := range atomicBackends(t) {
		t.Run(name, func(t *testing.T) {
			b := newBackend()
			key := []byte("k")
			val := []byte("value")
			if err := b.ApplyAtomic([]Mutation{{Key: key, Value: val}}); err != nil {
				t.Fatal(err)
			}
			copy(key, "x")
			copy(val, "XXXXX")
			if got := mustGet(t, b, "k"); string(got) != "value" {
				t.Errorf("committed value changed with the caller's buffer: %q", got)
			}
		})
	}
}

// snapshotPairs decodes the backends' shared snapshot format.
func snapshotPairs(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	r := bytes.NewReader(raw)
	var n uint32
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		t.Fatalf("snapshot count: %v", err)
	}
	out := make(map[string]string, n)
	read := func() []byte {
		var l uint32
		if err := binary.Read(r, binary.LittleEndian, &l); err != nil {
			t.Fatalf("snapshot length: %v", err)
		}
		b := make([]byte, l)
		if _, err := io.ReadFull(r, b); err != nil {
			t.Fatalf("snapshot body: %v", err)
		}
		return b
	}
	for i := uint32(0); i < n; i++ {
		k := read()
		out[string(k)] = string(read())
	}
	return out
}

// TestApplyAtomic_ReadersNeverSeeAPartialSet is the reader half of the
// all-or-nothing promise, on the backends read concurrently with Update:
// every set writes the same counter to keys in DIFFERENT shards, and neither a
// snapshot nor a Scan taken concurrently may hold two different counters. A
// backend that applied a set key by key, releasing each shard as it went, or
// a sweep that released each shard before reading the next, fails this under
// load.
//
// FastBackend is absent on purpose: it serves a regular (sm.StateMachine)
// state machine, whose reads go-quicraft never runs concurrently with Update.
func TestApplyAtomic_ReadersNeverSeeAPartialSet(t *testing.T) {
	backends := map[string]func() Backend{
		"standard":   func() Backend { return NewStandardBackend() },
		"concurrent": func() Backend { return NewConcurrentBackend() },
		"highperf":   func() Backend { return NewHighPerfBackend(nil) },
	}
	readers := map[string]func(t *testing.T, b Backend) map[string]string{
		"snapshot": func(t *testing.T, b Backend) map[string]string {
			var buf bytes.Buffer
			if err := b.SaveSnapshot(&buf, nil); err != nil {
				t.Fatalf("SaveSnapshot: %v", err)
			}
			return snapshotPairs(t, buf.Bytes())
		},
		"scan": func(t *testing.T, b Backend) map[string]string {
			got, err := b.Scan(nil, 0)
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			out := make(map[string]string, len(got))
			for k, v := range got {
				out[k] = string(v)
			}
			return out
		},
	}
	keys := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot"}

	for name, newBackend := range backends {
		for readerName, read := range readers {
			t.Run(name+"/"+readerName, func(t *testing.T) {
				b := newBackend()
				var done atomic.Bool
				var wg sync.WaitGroup
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := 0; i < 2000; i++ {
						set := make([]Mutation, len(keys))
						for j, k := range keys {
							set[j] = put(k, strconv.Itoa(i))
						}
						if err := b.ApplyAtomic(set); err != nil {
							t.Errorf("ApplyAtomic: %v", err)
							break
						}
					}
					done.Store(true)
				}()

				for !done.Load() {
					pairs := read(t, b)
					first, seen := pairs[keys[0]]
					for _, k := range keys[1:] {
						v, ok := pairs[k]
						if ok != seen || v != first {
							t.Fatalf("%s saw a partial set: %s=%q but %s=%q", readerName, keys[0], first, k, v)
						}
					}
				}
				wg.Wait()
			})
		}
	}
}

// failCommitStorage is memStorage whose batch Commit fails on demand, the
// fault ApplyAtomic must report and leave no trace of.
type failCommitStorage struct {
	*memStorage
	failing atomic.Bool
	err     error
}

func (s *failCommitStorage) Batch() storage.Batch {
	return &failCommitBatch{memBatch: &memBatch{store: s.memStorage}, owner: s}
}

type failCommitBatch struct {
	*memBatch
	owner *failCommitStorage
}

func (b *failCommitBatch) Commit() error {
	if b.owner.failing.Load() {
		return b.owner.err
	}
	return b.memBatch.Commit()
}

// TestApplyAtomic_FailedCommitLeavesNothingBehind covers the backend whose
// storage recovery reads: a storage fault is RETURNED, and neither a reader
// nor storage sees any of the set. (FastBackend's storage is a write-through
// copy recovery never reads — see its ApplyAtomic — so it has no commit to
// fail.)
func TestApplyAtomic_FailedCommitLeavesNothingBehind(t *testing.T) {
	fault := errors.New("disk on fire")

	for _, tc := range []struct {
		name string
		new  func(storage.Storage) Backend
	}{
		{"disk", func(s storage.Storage) Backend { return NewDiskBackend(s) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stg := &failCommitStorage{memStorage: newMemStorage(), err: fault}
			b := tc.new(stg)
			t.Cleanup(func() { _ = b.Close() })

			if err := b.PutDurable([]byte("held"), []byte("before")); err != nil {
				t.Fatal(err)
			}
			if err := b.PutDurable([]byte("retired"), []byte("x")); err != nil {
				t.Fatal(err)
			}
			if err := b.DeleteDurable([]byte("retired")); err != nil {
				t.Fatal(err)
			}

			stg.failing.Store(true)
			err := b.ApplyAtomic([]Mutation{put("held", "after"), put("fresh", "new"), put("retired", "back"), del("held2")})
			if !errors.Is(err, fault) {
				t.Fatalf("ApplyAtomic over a failing commit = %v; want the storage fault", err)
			}
			var se *StorageError
			if !errors.As(err, &se) {
				t.Fatalf("ApplyAtomic error %T is not a StorageError", err)
			}
			stg.failing.Store(false)

			if got := mustGet(t, b, "held"); string(got) != "before" {
				t.Errorf("held = %q; a failed set must leave the prior value", got)
			}
			if got := mustGet(t, b, "fresh"); got != nil {
				t.Errorf("fresh = %q; a failed set must not leave its write readable", got)
			}
			if got := mustGet(t, b, "retired"); got != nil {
				t.Errorf("retired = %q; a failed set must restore the delete, not resurrect", got)
			}
			if got, _ := stg.memStorage.Get([]byte("fresh")); got != nil {
				t.Errorf("storage holds fresh=%q from a commit that failed", got)
			}
		})
	}
}

// orderRecordingStorage records every storage write in the order it lands,
// and parks the first Set until released.
type orderRecordingStorage struct {
	*memStorage
	mu      sync.Mutex
	log     []string
	holdSet chan struct{}
	held    chan struct{}
	parked  sync.Once
}

func (s *orderRecordingStorage) Set(key, value []byte) error {
	first := false
	s.parked.Do(func() { first = true })
	if first {
		close(s.held)
		<-s.holdSet
	}
	s.mu.Lock()
	s.log = append(s.log, "set:"+string(key)+"="+string(value))
	s.mu.Unlock()
	return s.memStorage.Set(key, value)
}

// TestFastBackend_ApplyAtomic_WritesThroughInApplyOrder pins the fast
// backend's write-through: the set is visible to readers at once, and its
// storage write lands AFTER an earlier asynchronous write to the same key that
// is still parked in storage — never before it, where the older write would
// land last and leave storage holding it.
func TestFastBackend_ApplyAtomic_WritesThroughInApplyOrder(t *testing.T) {
	stg := &orderRecordingStorage{memStorage: newMemStorage(), holdSet: make(chan struct{}), held: make(chan struct{})}
	b := NewFastBackend(1, 1, stg, nil)
	var release sync.Once
	releaseParked := func() { release.Do(func() { close(stg.holdSet) }) }
	// Cleanups run last-in first-out: release the parked write before Close
	// waits for it.
	t.Cleanup(func() { _ = b.Close() })
	t.Cleanup(releaseParked)

	if err := b.Put([]byte("k"), []byte("async")); err != nil {
		t.Fatal(err)
	}
	<-stg.held // the async write is parked inside storage

	if err := b.ApplyAtomic([]Mutation{put("k", "atomic")}); err != nil {
		t.Fatalf("ApplyAtomic: %v", err)
	}
	if got := mustGet(t, b, "k"); string(got) != "atomic" {
		t.Fatalf("k = %q right after ApplyAtomic; the set must be visible at once", got)
	}

	releaseParked()
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if got, _ := stg.memStorage.Get([]byte("k")); string(got) != "atomic" {
		t.Fatalf("storage holds k=%q; the set's write must land after the earlier async write", got)
	}
	stg.mu.Lock()
	defer stg.mu.Unlock()
	if len(stg.log) != 2 || stg.log[0] != "set:k=async" || stg.log[1] != "set:k=atomic" {
		t.Fatalf("storage write order = %v; want [set:k=async set:k=atomic]", stg.log)
	}
}

// TestDiskAdapter_TransactionCommitsItsIndexWithItsWrites is the crash test
// for the disk state machine. The final applied-index write after the entries
// is made to FAIL — the process dying between a transaction's commit and that
// write. The transaction's own entry index must already be durable, committed
// in the same storage batch as its writes, so a restart resumes AFTER it and
// never replays it over its own result, where its compare would decide the
// other way.
func TestDiskAdapter_TransactionCommitsItsIndexWithItsWrites(t *testing.T) {
	stg := &markerCrashStorage{memStorage: newMemStorage()}
	a := newDiskAdapter(stg)
	ctx := context.Background()

	cmp := mustEncode(protocol.EncodeCompare([]byte("claim"), txnTargetVersion, txnResultEqual, nil, 0, 0, 0))
	onSuccess := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("claim"), []byte("mine"), nil, 0))
	onFailure := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("lost"), []byte("yes"), nil, 0))
	txn := mustEncode(protocol.EncodeTransaction([][]byte{cmp}, [][]byte{onSuccess}, [][]byte{onFailure}))

	stg.crashing.Store(true)
	results := make([]sm.Result, 1)
	if err := a.Update(ctx, []sm.Entry{{Index: 7, Cmd: txn}}, results); err == nil {
		t.Fatal("Update succeeded although the trailing applied-index write failed")
	}
	stg.crashing.Store(false)

	restarted := newDiskAdapter(stg)
	idx, err := restarted.Open(ctx, "", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if idx != 7 {
		t.Fatalf("restart resumes at index %d; want 7 — the transaction's index must be "+
			"committed with its writes, or it is replayed over its own result", idx)
	}
	if got, _ := stg.Get([]byte("claim")); string(got) != "mine" {
		t.Fatalf("claim = %q; want the success branch's write", got)
	}
}

// TestDiskAdapter_GuardedSingleKeyWritesCommitTheirIndex is the crash test for
// the single-key guarded verbs. From an absent key: entry 1 is a
// CompareAndSwap expecting "a", which DECLINES; entry 2 is a PutIfAbsent of
// "a", which creates the key. If the process dies after entry 2's write and
// before its index is durable, Raft replays both entries over the result —
// and now the swap SUCCEEDS (the key holds "a") and the create declines,
// leaving "b" on this replica and "a" on every other. Entry 2's index must be
// committed with its write, so the restart resumes after it.
func TestDiskAdapter_GuardedSingleKeyWritesCommitTheirIndex(t *testing.T) {
	stg := &markerCrashStorage{memStorage: newMemStorage()}
	a := newDiskAdapter(stg)
	ctx := context.Background()

	cas := mustEncode(protocol.EncodeCompareAndSwap([]byte("k"), []byte("a"), []byte("b")))
	pia := mustEncode(kv.EncodePutIfAbsent([]byte("k"), []byte("a")))

	stg.crashing.Store(true)
	results := make([]sm.Result, 2)
	if err := a.Update(ctx, []sm.Entry{{Index: 1, Cmd: cas}, {Index: 2, Cmd: pia}}, results); err == nil {
		t.Fatal("Update succeeded although the trailing applied-index write failed")
	}
	stg.crashing.Store(false)
	if results[0].Value != protocol.ResultNotSwapped || results[1].Value != kv.ResultStored {
		t.Fatalf("first application: swap=%d create=%d; want declined then stored", results[0].Value, results[1].Value)
	}

	restarted := newDiskAdapter(stg)
	idx, err := restarted.Open(ctx, "", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if idx != 2 {
		t.Fatalf("restart resumes at index %d; want 2 — the create's index must be committed "+
			"with its write, or the swap replays over it and succeeds", idx)
	}
	if got, _ := stg.Get([]byte("k")); string(got) != "a" {
		t.Fatalf("k = %q; want a", got)
	}
}

// TestKVFastCreateFunc_StartsFromAnEmptyKeyspace pins the fast state machine's
// restart: Raft rebuilds it from a snapshot or a full replay, so whatever its
// storage holds from the previous run is state AFTER the entries about to be
// replayed. A transaction replayed over it must decide as it did the first
// time — here, a guarded create of a key the previous run created.
func TestKVFastCreateFunc_StartsFromAnEmptyKeyspace(t *testing.T) {
	stg := newMemStorage()
	if err := stg.Set([]byte("claim"), []byte("from-the-future")); err != nil {
		t.Fatal(err)
	}

	machine := NewKVFastCreateFunc(func(_, _ uint64) (storage.Storage, error) { return stg, nil })(1, 1).(*StandardAdapter)
	t.Cleanup(func() { _ = machine.Close(context.Background()) })

	cmp := mustEncode(protocol.EncodeCompare([]byte("claim"), txnTargetVersion, txnResultEqual, nil, 0, 0, 0))
	op := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("claim"), []byte("replayed"), nil, 0))
	txn := mustEncode(protocol.EncodeTransaction([][]byte{cmp}, [][]byte{op}, nil))

	results := make([]sm.Result, 1)
	if err := machine.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: txn}}, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(results[0].Data) != 1 || results[0].Data[0] != 1 {
		t.Fatalf("replayed guarded create decided %v; want success — the previous run's "+
			"storage must not answer for state the log has not reached", results[0].Data)
	}
}
