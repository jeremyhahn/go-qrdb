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
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// ---------------------------------------------------------------------------
// memStorage: in-memory implementation of storage.Storage for testing
// ---------------------------------------------------------------------------

type memStorage struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func newMemStorage() *memStorage {
	return &memStorage{data: make(map[string][]byte)}
}

func (m *memStorage) Get(key []byte) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.data[string(key)]
	if !ok {
		return nil, nil
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

func (m *memStorage) Set(key, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(value))
	copy(cp, value)
	m.data[string(key)] = cp
	return nil
}

func (m *memStorage) Delete(key []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, string(key))
	return nil
}

func (m *memStorage) Batch() storage.Batch {
	return &memBatch{store: m}
}

func (m *memStorage) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Collect and sort keys within bounds.
	var keys []string
	for k := range m.data {
		if opts != nil {
			if opts.LowerBound != nil && k < string(opts.LowerBound) {
				continue
			}
			if opts.UpperBound != nil && k >= string(opts.UpperBound) {
				continue
			}
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	entries := make([]kvPair, len(keys))
	for i, k := range keys {
		v := m.data[k]
		vc := make([]byte, len(v))
		copy(vc, v)
		entries[i] = kvPair{key: []byte(k), value: vc}
	}

	return &memIterator{entries: entries, pos: -1}
}

func (m *memStorage) Snapshot() (storage.Snapshot, error) {
	return nil, errors.New("snapshot not implemented in memStorage")
}

func (m *memStorage) List(prefix []byte) ([][]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var keys [][]byte
	for k := range m.data {
		if len(prefix) == 0 || strings.HasPrefix(k, string(prefix)) {
			keys = append(keys, []byte(k))
		}
	}
	return keys, nil
}

func (m *memStorage) Exists(key []byte) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.data[string(key)]
	return ok, nil
}

func (m *memStorage) Close() error {
	return nil
}

// memBatch implements storage.Batch for memStorage.
type memBatch struct {
	store *memStorage
	ops   []func()
}

func (b *memBatch) Set(key, value []byte) error {
	k := make([]byte, len(key))
	copy(k, key)
	v := make([]byte, len(value))
	copy(v, value)
	b.ops = append(b.ops, func() {
		b.store.data[string(k)] = v
	})
	return nil
}

func (b *memBatch) Delete(key []byte) error {
	k := make([]byte, len(key))
	copy(k, key)
	b.ops = append(b.ops, func() {
		delete(b.store.data, string(k))
	})
	return nil
}

func (b *memBatch) Commit() error {
	b.store.mu.Lock()
	defer b.store.mu.Unlock()
	for _, op := range b.ops {
		op()
	}
	return nil
}

func (b *memBatch) Rollback() error {
	b.ops = nil
	return nil
}

// Get reads through the batch's pending operations, honouring storage.Batch's
// read-your-own-writes contract.
func (b *memBatch) Get(key []byte) ([]byte, error) {
	return b.overlay().Get(key)
}

func (b *memBatch) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	return b.overlay().NewIterator(opts)
}

// overlay materializes the committed state with this batch's pending
// operations applied.
func (b *memBatch) overlay() *memStorage {
	b.store.mu.RLock()
	view := &memStorage{data: make(map[string][]byte, len(b.store.data))}
	for k, v := range b.store.data {
		view.data[k] = v
	}
	b.store.mu.RUnlock()

	shadow := &memBatch{store: view, ops: b.ops}
	_ = shadow.Commit()
	return view
}

func (b *memBatch) Close() error {
	b.ops = nil
	return nil
}

// kvPair holds a single key-value pair for the in-memory iterator.
type kvPair struct {
	key   []byte
	value []byte
}

// memIterator implements storage.Iterator over a sorted slice.
type memIterator struct {
	entries []kvPair
	pos     int
	err     error
}

func (it *memIterator) SeekToFirst() bool {
	if len(it.entries) == 0 {
		return false
	}
	it.pos = 0
	return true
}

func (it *memIterator) SeekToLast() bool {
	if len(it.entries) == 0 {
		return false
	}
	it.pos = len(it.entries) - 1
	return true
}

func (it *memIterator) Seek(target []byte) bool {
	t := string(target)
	idx := sort.SearchStrings(it.sortedKeys(), t)
	if idx >= len(it.entries) {
		return false
	}
	it.pos = idx
	return true
}

func (it *memIterator) sortedKeys() []string {
	keys := make([]string, len(it.entries))
	for i, e := range it.entries {
		keys[i] = string(e.key)
	}
	return keys
}

func (it *memIterator) Next() bool {
	it.pos++
	return it.pos < len(it.entries)
}

func (it *memIterator) Prev() bool {
	it.pos--
	return it.pos >= 0
}

func (it *memIterator) Valid() bool {
	return it.pos >= 0 && it.pos < len(it.entries)
}

func (it *memIterator) Key() []byte {
	if !it.Valid() {
		return nil
	}
	return it.entries[it.pos].key
}

func (it *memIterator) Value() []byte {
	if !it.Valid() {
		return nil
	}
	return it.entries[it.pos].value
}

func (it *memIterator) Error() error {
	return it.err
}

func (it *memIterator) Close() error {
	return nil
}

// ---------------------------------------------------------------------------
// mockWatchManager: captures watch notifications for assertions
// ---------------------------------------------------------------------------

type watchEvent struct {
	key      string
	value    []byte
	revision uint64
	deleted  bool
}

type mockWatchManager struct {
	mu     sync.Mutex
	events []watchEvent
}

func (m *mockWatchManager) Notify(key string, value []byte, revision uint64, deleted bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, watchEvent{
		key:      key,
		value:    value,
		revision: revision,
		deleted:  deleted,
	})
}

func (m *mockWatchManager) getEvents() []watchEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]watchEvent, len(m.events))
	copy(cp, m.events)
	return cp
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newTestFSM(opts ...Option) (*FastStateMachine, *memStorage) {
	store := newMemStorage()
	fsm := NewFastStateMachine(1, 1, store, opts...)
	return fsm, store
}

// applyPut builds a KV Put entry and applies it via Update.
func applyPut(t *testing.T, fsm *FastStateMachine, key, value string) sm.Result {
	t.Helper()
	cmd, err := kv.EncodePut([]byte(key), []byte(value))
	if err != nil {
		t.Fatalf("EncodePut failed: %v", err)
	}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := fsm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update(put) failed: %v", err)
	}
	return results[0]
}

// applyDelete builds a KV Delete entry and applies it via Update.
func applyDelete(t *testing.T, fsm *FastStateMachine, key string) sm.Result {
	t.Helper()
	cmd, err := kv.EncodeDelete([]byte(key))
	if err != nil {
		t.Fatalf("EncodeDelete failed: %v", err)
	}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := fsm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update(delete) failed: %v", err)
	}
	return results[0]
}

// lookupKey performs a point lookup and returns the result.
func lookupKey(t *testing.T, fsm *FastStateMachine, key string) []byte {
	t.Helper()
	result, err := fsm.Lookup(context.Background(), []byte(key))
	if err != nil {
		t.Fatalf("Lookup failed: %v", err)
	}
	if result == nil {
		return nil
	}
	val, ok := result.([]byte)
	if !ok {
		t.Fatalf("Lookup returned unexpected type: %T", result)
	}
	return val
}

// waitForAsync waits for all async persistence goroutines to complete.
func waitForAsync(fsm *FastStateMachine) {
	fsm.wg.Wait()
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestFastStateMachine_Put_Success(t *testing.T) {
	fsm, store := newTestFSM()

	result := applyPut(t, fsm, "hello", "world")
	if result.Value == 0 {
		t.Fatal("expected non-zero revision")
	}

	// Verify via Lookup (cache read).
	val := lookupKey(t, fsm, "hello")
	if string(val) != "world" {
		t.Fatalf("expected 'world', got %q", val)
	}

	// Wait for async persist and verify storage.
	waitForAsync(fsm)
	stored, err := store.Get([]byte("hello"))
	if err != nil {
		t.Fatalf("storage.Get failed: %v", err)
	}
	if string(stored) != "world" {
		t.Fatalf("storage: expected 'world', got %q", stored)
	}
}

func TestFastStateMachine_Put_EmptyValue(t *testing.T) {
	fsm, store := newTestFSM()

	result := applyPut(t, fsm, "empty-key", "")
	if result.Value == 0 {
		t.Fatal("expected non-zero revision")
	}

	val := lookupKey(t, fsm, "empty-key")
	if val == nil {
		t.Fatal("expected non-nil empty slice for empty value")
	}
	if len(val) != 0 {
		t.Fatalf("expected empty value, got %q", val)
	}

	waitForAsync(fsm)
	stored, err := store.Get([]byte("empty-key"))
	if err != nil {
		t.Fatalf("storage.Get failed: %v", err)
	}
	if stored == nil {
		t.Fatal("storage: expected non-nil empty slice")
	}
	if len(stored) != 0 {
		t.Fatalf("storage: expected empty value, got %q", stored)
	}
}

func TestFastStateMachine_Get_Found(t *testing.T) {
	fsm, _ := newTestFSM()

	applyPut(t, fsm, "key1", "value1")
	applyPut(t, fsm, "key2", "value2")

	val := lookupKey(t, fsm, "key1")
	if string(val) != "value1" {
		t.Fatalf("expected 'value1', got %q", val)
	}

	val = lookupKey(t, fsm, "key2")
	if string(val) != "value2" {
		t.Fatalf("expected 'value2', got %q", val)
	}
}

func TestFastStateMachine_Get_NotFound(t *testing.T) {
	fsm, _ := newTestFSM()

	val := lookupKey(t, fsm, "nonexistent")
	if val != nil {
		t.Fatalf("expected nil for missing key, got %q", val)
	}
}

func TestFastStateMachine_Delete_Success(t *testing.T) {
	fsm, store := newTestFSM()

	applyPut(t, fsm, "to-delete", "value")

	// Wait for async put to persist before issuing delete so that
	// the storage operations are properly ordered.
	waitForAsync(fsm)

	val := lookupKey(t, fsm, "to-delete")
	if string(val) != "value" {
		t.Fatalf("expected 'value' before delete, got %q", val)
	}

	result := applyDelete(t, fsm, "to-delete")
	if result.Value == 0 {
		t.Fatal("expected non-zero revision from delete")
	}

	// Lookup should return nil (tombstone).
	val = lookupKey(t, fsm, "to-delete")
	if val != nil {
		t.Fatalf("expected nil after delete, got %q", val)
	}

	// After async cleanup, storage should also be empty.
	waitForAsync(fsm)
	stored, err := store.Get([]byte("to-delete"))
	if err != nil {
		t.Fatalf("storage.Get failed: %v", err)
	}
	if stored != nil {
		t.Fatalf("storage: expected nil after delete, got %q", stored)
	}
}

func TestFastStateMachine_Delete_NotFound(t *testing.T) {
	fsm, _ := newTestFSM()

	// Deleting a key that does not exist should succeed without error.
	result := applyDelete(t, fsm, "ghost")
	if result.Value == 0 {
		t.Fatal("expected non-zero revision")
	}

	val := lookupKey(t, fsm, "ghost")
	if val != nil {
		t.Fatalf("expected nil for deleted non-existent key, got %q", val)
	}
}

func TestFastStateMachine_Batch_MixedOps(t *testing.T) {
	fsm, _ := newTestFSM()

	// Pre-populate a key that the batch will delete.
	applyPut(t, fsm, "batch-del", "to-be-removed")

	// Build batch: 2 puts + 1 delete.
	ops := [][]byte{
		mustEncode(protocol.EncodeOp(kv.OpPut, []byte("batch-a"), []byte("val-a"), nil, 0)),
		mustEncode(protocol.EncodeOp(kv.OpPut, []byte("batch-b"), []byte("val-b"), nil, 0)),
		mustEncode(protocol.EncodeOp(kv.OpDelete, []byte("batch-del"), nil, nil, 0)),
	}
	cmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Index: 10, Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := fsm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update(batch) failed: %v", err)
	}

	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision from batch")
	}

	// Verify puts.
	if v := lookupKey(t, fsm, "batch-a"); string(v) != "val-a" {
		t.Fatalf("batch put a: expected 'val-a', got %q", v)
	}
	if v := lookupKey(t, fsm, "batch-b"); string(v) != "val-b" {
		t.Fatalf("batch put b: expected 'val-b', got %q", v)
	}

	// Verify delete.
	if v := lookupKey(t, fsm, "batch-del"); v != nil {
		t.Fatalf("batch delete: expected nil, got %q", v)
	}
}

func TestFastStateMachine_PrefixScan_CacheOverlay(t *testing.T) {
	fsm, store := newTestFSM()

	// Put some keys directly into storage (simulating prior persist).
	_ = store.Set([]byte("ns/a"), []byte("from-storage-a"))
	_ = store.Set([]byte("ns/b"), []byte("from-storage-b"))
	_ = store.Set([]byte("other/x"), []byte("should-not-appear"))

	// Override one key in cache (cache wins).
	applyPut(t, fsm, "ns/b", "from-cache-b")
	// Add a new key only in cache.
	applyPut(t, fsm, "ns/c", "from-cache-c")
	waitForAsync(fsm)

	result, err := fsm.Lookup(context.Background(), &PrefixQuery{Prefix: []byte("ns/")})
	if err != nil {
		t.Fatalf("prefix lookup failed: %v", err)
	}
	m, ok := result.(map[string][]byte)
	if !ok {
		t.Fatalf("expected map[string][]byte, got %T", result)
	}

	if len(m) != 3 {
		t.Fatalf("expected 3 entries, got %d: %v", len(m), keysOf(m))
	}

	// ns/a comes from storage only.
	if string(m["ns/a"]) != "from-storage-a" {
		t.Fatalf("ns/a: expected 'from-storage-a', got %q", m["ns/a"])
	}
	// ns/b should be overridden by cache.
	if string(m["ns/b"]) != "from-cache-b" {
		t.Fatalf("ns/b: expected 'from-cache-b', got %q", m["ns/b"])
	}
	// ns/c from cache only.
	if string(m["ns/c"]) != "from-cache-c" {
		t.Fatalf("ns/c: expected 'from-cache-c', got %q", m["ns/c"])
	}
}

func TestFastStateMachine_PrefixScan_Tombstone(t *testing.T) {
	fsm, store := newTestFSM()

	// Put a key in storage, then delete it through the state machine
	// so it becomes a tombstone in cache.
	_ = store.Set([]byte("pfx/alive"), []byte("yes"))
	_ = store.Set([]byte("pfx/dead"), []byte("to-be-killed"))

	applyDelete(t, fsm, "pfx/dead")

	result, err := fsm.Lookup(context.Background(), &PrefixQuery{Prefix: []byte("pfx/")})
	if err != nil {
		t.Fatalf("prefix lookup failed: %v", err)
	}
	m := result.(map[string][]byte)

	if _, found := m["pfx/dead"]; found {
		t.Fatal("tombstoned key pfx/dead should not appear in prefix scan")
	}
	if string(m["pfx/alive"]) != "yes" {
		t.Fatalf("pfx/alive: expected 'yes', got %q", m["pfx/alive"])
	}
}

func TestFastStateMachine_Snapshot_SaveRecover(t *testing.T) {
	fsm1, _ := newTestFSM()

	// Populate some data.
	applyPut(t, fsm1, "snap/key1", "val1")
	applyPut(t, fsm1, "snap/key2", "val2")
	applyPut(t, fsm1, "snap/key3", "val3")

	// Save snapshot.
	var buf bytes.Buffer
	stopper := make(chan struct{})
	if err := fsm1.SaveSnapshot(context.Background(), &buf, stopper); err != nil {
		t.Fatalf("SaveSnapshot failed: %v", err)
	}

	// Create a new FSM and recover.
	fsm2, _ := newTestFSM()
	if err := fsm2.RecoverFromSnapshot(context.Background(), &buf, stopper); err != nil {
		t.Fatalf("RecoverFromSnapshot failed: %v", err)
	}

	// Verify all keys are present.
	for _, tc := range []struct{ key, val string }{
		{"snap/key1", "val1"},
		{"snap/key2", "val2"},
		{"snap/key3", "val3"},
	} {
		got := lookupKey(t, fsm2, tc.key)
		if string(got) != tc.val {
			t.Fatalf("%s: expected %q, got %q", tc.key, tc.val, got)
		}
	}
}

func TestFastStateMachine_Snapshot_Empty(t *testing.T) {
	fsm1, _ := newTestFSM()

	// Save snapshot with no data.
	var buf bytes.Buffer
	stopper := make(chan struct{})
	if err := fsm1.SaveSnapshot(context.Background(), &buf, stopper); err != nil {
		t.Fatalf("SaveSnapshot failed: %v", err)
	}

	// Recover into a fresh FSM.
	fsm2, _ := newTestFSM()

	// Pre-populate to verify recovery clears state.
	applyPut(t, fsm2, "stale", "data")

	if err := fsm2.RecoverFromSnapshot(context.Background(), &buf, stopper); err != nil {
		t.Fatalf("RecoverFromSnapshot failed: %v", err)
	}

	// The stale key should be gone.
	val := lookupKey(t, fsm2, "stale")
	if val != nil {
		t.Fatalf("expected nil after empty snapshot recover, got %q", val)
	}
}

func TestFastStateMachine_Update_UnknownOp(t *testing.T) {
	fsm, _ := newTestFSM()

	cmd := []byte{0xFF, 0x01, 0x02} // unknown op 0xFF
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for unknown op")
	}

	var opErr *InvalidOpcodeError
	if !errors.As(err, &opErr) {
		t.Fatalf("expected InvalidOpcodeError, got %T: %v", err, err)
	}
	if opErr.Op != 0xFF {
		t.Fatalf("expected op 0xFF, got 0x%02X", opErr.Op)
	}
}

func TestFastStateMachine_WatchNotification(t *testing.T) {
	wm := &mockWatchManager{}
	fsm, _ := newTestFSM(WithWatchManager(wm))

	applyPut(t, fsm, "watched", "value1")
	applyDelete(t, fsm, "watched")

	events := wm.getEvents()
	if len(events) != 2 {
		t.Fatalf("expected 2 watch events, got %d", len(events))
	}

	// First event: put.
	if events[0].key != "watched" || events[0].deleted {
		t.Fatalf("event 0: expected put on 'watched', got key=%q deleted=%v", events[0].key, events[0].deleted)
	}
	if string(events[0].value) != "value1" {
		t.Fatalf("event 0: expected value 'value1', got %q", events[0].value)
	}

	// Second event: delete.
	if events[1].key != "watched" || !events[1].deleted {
		t.Fatalf("event 1: expected delete on 'watched', got key=%q deleted=%v", events[1].key, events[1].deleted)
	}
	if events[1].value != nil {
		t.Fatalf("event 1: expected nil value for delete, got %q", events[1].value)
	}
}

func TestFastStateMachine_Close(t *testing.T) {
	fsm, _ := newTestFSM()

	// Put some data to trigger async writes.
	applyPut(t, fsm, "close-test", "data")

	// Close should wait for async goroutines and return without error.
	if err := fsm.Close(context.Background()); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

func TestFastStateMachine_ConcurrentPuts(t *testing.T) {
	fsm, _ := newTestFSM()

	const goroutines = 64
	const opsPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	var errCount atomic.Int64

	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				key := "concurrent/" + string(rune('A'+id)) + "/" + string(rune('0'+i%10))
				cmd, err := kv.EncodePut([]byte(key), []byte("val"))
				if err != nil {
					errCount.Add(1)
					continue
				}
				entries := []sm.Entry{{Index: uint64(id*opsPerGoroutine + i), Cmd: cmd}}
				results := make([]sm.Result, 1)
				if err := fsm.Update(context.Background(), entries, results); err != nil {
					errCount.Add(1)
				}
			}
		}(g)
	}

	wg.Wait()

	if n := errCount.Load(); n != 0 {
		t.Fatalf("concurrent puts produced %d errors", n)
	}

	// Close to wait for all async writes.
	if err := fsm.Close(context.Background()); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

func TestFastStateMachine_Update_EmptyEntry(t *testing.T) {
	fsm, _ := newTestFSM()

	// An entry with empty Cmd should produce a zero Result without error.
	entries := []sm.Entry{{Index: 1, Cmd: nil}}
	results := make([]sm.Result, 1)

	if err := fsm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update with empty cmd failed: %v", err)
	}
	if results[0].Value != 0 {
		t.Fatalf("expected zero result for empty cmd, got %d", results[0].Value)
	}
}

func TestFastStateMachine_Put_InvalidPayload(t *testing.T) {
	fsm, _ := newTestFSM()

	// Op = OpPut (1) but payload is too short (missing keyLen).
	cmd := []byte{kv.OpPut}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for truncated put payload")
	}

	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestFastStateMachine_Delete_InvalidPayload(t *testing.T) {
	fsm, _ := newTestFSM()

	// Op = OpDelete (3) but payload is too short.
	cmd := []byte{kv.OpDelete}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for truncated delete payload")
	}

	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestFastStateMachine_Batch_InvalidPayload(t *testing.T) {
	fsm, _ := newTestFSM()

	// Batch op with truncated payload.
	cmd := []byte{protocol.OpBatch, 0x01} // only 1 byte of payload (need at least 2)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for truncated batch payload")
	}

	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestFastStateMachine_Lookup_UnsupportedQueryType(t *testing.T) {
	fsm, _ := newTestFSM()

	_, err := fsm.Lookup(context.Background(), 42)
	if err == nil {
		t.Fatal("expected error for unsupported query type")
	}

	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
	if !strings.Contains(incErr.Reason, "unsupported query type") {
		t.Fatalf("unexpected error reason: %s", incErr.Reason)
	}
}

func TestFastStateMachine_HandlersNotConfigured(t *testing.T) {
	fsm, _ := newTestFSM()

	// Handlers for domain stores that are not configured should encode
	// ServiceNotConfiguredError in result.Data (Raft-safe: no error returned).
	notConfiguredOps := []struct {
		op      byte
		service string
	}{
		{protocol.OpMVCCPut, "mvcc"},
		{protocol.OpMVCCDelete, "mvcc"},
		{protocol.OpMVCCRange, "mvcc"},
		{protocol.OpMVCCCompact, "mvcc"},
		{protocol.OpTransaction, "mvcc"},
		{protocol.OpTSWrite, "timeseries"},
		{protocol.OpTSWriteBatch, "timeseries"},
		{protocol.OpTSWritePoint, "timeseries"},
		{protocol.OpTSDeleteSeries, "timeseries"},
		{protocol.OpTSSetRetention, "timeseries"},
	}

	dummyPayload := make([]byte, 32)

	for _, tc := range notConfiguredOps {
		cmd := make([]byte, 1+len(dummyPayload))
		cmd[0] = tc.op
		copy(cmd[1:], dummyPayload)

		entries := []sm.Entry{{Index: 1, Cmd: cmd}}
		results := make([]sm.Result, 1)

		err := fsm.Update(context.Background(), entries, results)
		if err != nil {
			t.Fatalf("op %d: expected nil error (SNC encoded in result.Data), got %v", tc.op, err)
		}
		if len(results[0].Data) == 0 {
			t.Fatalf("op %d: expected ServiceNotConfiguredError encoded in result.Data", tc.op)
		}
		if !bytes.Contains(results[0].Data, []byte(tc.service)) {
			t.Fatalf("op %d: expected Data to mention %q, got %q", tc.op, tc.service, results[0].Data)
		}
	}
}

func TestFastStateMachine_Snapshot_Cancelled(t *testing.T) {
	fsm, _ := newTestFSM()

	// Populate enough data that the stopper might be checked.
	for i := 0; i < 10; i++ {
		applyPut(t, fsm, "cancel/"+string(rune('A'+i)), "data")
	}

	// Close the stopper immediately.
	stopper := make(chan struct{})
	close(stopper)

	var buf bytes.Buffer
	err := fsm.SaveSnapshot(context.Background(), &buf, stopper)
	// May or may not error depending on timing. If it does, should be SnapshotError.
	if err != nil {
		var snapErr *SnapshotError
		if !errors.As(err, &snapErr) {
			t.Fatalf("expected SnapshotError, got %T: %v", err, err)
		}
	}
}

func TestFastStateMachine_RecoverSnapshot_Cancelled(t *testing.T) {
	fsm1, _ := newTestFSM()

	// Save a snapshot with data.
	for i := 0; i < 10; i++ {
		applyPut(t, fsm1, "recov/"+string(rune('A'+i)), "data")
	}

	var buf bytes.Buffer
	openStopper := make(chan struct{})
	if err := fsm1.SaveSnapshot(context.Background(), &buf, openStopper); err != nil {
		t.Fatalf("SaveSnapshot failed: %v", err)
	}

	// Recover with stopper already closed.
	fsm2, _ := newTestFSM()
	closedStopper := make(chan struct{})
	close(closedStopper)

	err := fsm2.RecoverFromSnapshot(context.Background(), &buf, closedStopper)
	// May or may not see cancellation depending on timing.
	if err != nil {
		var snapErr *SnapshotError
		if !errors.As(err, &snapErr) {
			t.Fatalf("expected SnapshotError, got %T: %v", err, err)
		}
	}
}

func TestFastStateMachine_MultipleEntries(t *testing.T) {
	fsm, _ := newTestFSM()

	// Apply multiple entries in a single Update call.
	cmdA, err := kv.EncodePut([]byte("multi/a"), []byte("va"))
	if err != nil {
		t.Fatalf("EncodePut failed: %v", err)
	}
	cmdB, err := kv.EncodePut([]byte("multi/b"), []byte("vb"))
	if err != nil {
		t.Fatalf("EncodePut failed: %v", err)
	}
	cmdDel, err := kv.EncodeDelete([]byte("multi/a"))
	if err != nil {
		t.Fatalf("EncodeDelete failed: %v", err)
	}
	entries := []sm.Entry{
		{Index: 1, Cmd: cmdA},
		{Index: 2, Cmd: cmdB},
		{Index: 3, Cmd: cmdDel},
	}
	results := make([]sm.Result, 3)

	if err := fsm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// All three should have increasing revisions.
	if results[0].Value >= results[1].Value {
		t.Fatalf("expected increasing revisions: %d >= %d", results[0].Value, results[1].Value)
	}
	if results[1].Value >= results[2].Value {
		t.Fatalf("expected increasing revisions: %d >= %d", results[1].Value, results[2].Value)
	}

	// multi/a should be tombstoned.
	val := lookupKey(t, fsm, "multi/a")
	if val != nil {
		t.Fatalf("multi/a should be nil after delete, got %q", val)
	}

	// multi/b should exist.
	val = lookupKey(t, fsm, "multi/b")
	if string(val) != "vb" {
		t.Fatalf("multi/b: expected 'vb', got %q", val)
	}
}

func TestFastStateMachine_Batch_UnknownOp(t *testing.T) {
	fsm, _ := newTestFSM()

	// Build a batch with an unknown op type inside.
	ops := [][]byte{
		mustEncode(protocol.EncodeOp(0xFE, []byte("bad"), []byte("val"), nil, 0)),
	}
	cmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for unknown op in batch")
	}

	var opErr *InvalidOpcodeError
	if !errors.As(err, &opErr) {
		t.Fatalf("expected InvalidOpcodeError, got %T: %v", err, err)
	}
}

func TestFastStateMachine_WatchNotification_Batch(t *testing.T) {
	wm := &mockWatchManager{}
	fsm, _ := newTestFSM(WithWatchManager(wm))

	ops := [][]byte{
		mustEncode(protocol.EncodeOp(kv.OpPut, []byte("bw/a"), []byte("va"), nil, 0)),
		mustEncode(protocol.EncodeOp(kv.OpDelete, []byte("bw/b"), nil, nil, 0)),
	}
	cmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := fsm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update(batch) failed: %v", err)
	}

	events := wm.getEvents()
	if len(events) != 2 {
		t.Fatalf("expected 2 watch events from batch, got %d", len(events))
	}

	if events[0].key != "bw/a" || events[0].deleted {
		t.Fatalf("event 0: expected put on 'bw/a', got %+v", events[0])
	}
	if events[1].key != "bw/b" || !events[1].deleted {
		t.Fatalf("event 1: expected delete on 'bw/b', got %+v", events[1])
	}
}

func TestFastStateMachine_PrefixScan_Empty(t *testing.T) {
	fsm, _ := newTestFSM()

	result, err := fsm.Lookup(context.Background(), &PrefixQuery{Prefix: []byte("noexist/")})
	if err != nil {
		t.Fatalf("prefix lookup failed: %v", err)
	}
	m := result.(map[string][]byte)
	if len(m) != 0 {
		t.Fatalf("expected empty map, got %d entries", len(m))
	}
}

func TestFastStateMachine_Put_KeyOverwrite(t *testing.T) {
	fsm, _ := newTestFSM()

	applyPut(t, fsm, "overwrite", "original")
	applyPut(t, fsm, "overwrite", "updated")

	val := lookupKey(t, fsm, "overwrite")
	if string(val) != "updated" {
		t.Fatalf("expected 'updated', got %q", val)
	}
}

// ---------------------------------------------------------------------------
// Error type coverage tests
// ---------------------------------------------------------------------------

func TestInvalidOpcodeError_Error(t *testing.T) {
	err := &InvalidOpcodeError{Op: 0xAB}
	msg := err.Error()
	if !strings.Contains(msg, "171") { // 0xAB = 171
		t.Fatalf("expected error message to contain op value 171, got %q", msg)
	}
	if !strings.Contains(msg, "statemachine") {
		t.Fatalf("expected error message to contain 'statemachine', got %q", msg)
	}
}

func TestIncompleteDataError_Error(t *testing.T) {
	err := &IncompleteDataError{Reason: "missing key"}
	msg := err.Error()
	if !strings.Contains(msg, "missing key") {
		t.Fatalf("expected error message to contain reason, got %q", msg)
	}
	if !strings.Contains(msg, "incomplete data") {
		t.Fatalf("expected error message to contain 'incomplete data', got %q", msg)
	}
}

func TestSnapshotError_Error_And_Unwrap(t *testing.T) {
	inner := errors.New("disk full")
	err := &SnapshotError{Op: "save", Inner: inner}
	msg := err.Error()
	if !strings.Contains(msg, "save") {
		t.Fatalf("expected error message to contain op 'save', got %q", msg)
	}
	if !strings.Contains(msg, "disk full") {
		t.Fatalf("expected error message to contain inner error, got %q", msg)
	}
	if err.Unwrap() != inner {
		t.Fatal("Unwrap should return the inner error")
	}
}

func TestStorageError_Error_And_Unwrap(t *testing.T) {
	inner := errors.New("io timeout")
	err := &StorageError{Op: "get", Inner: inner}
	msg := err.Error()
	if !strings.Contains(msg, "get") {
		t.Fatalf("expected error message to contain op 'get', got %q", msg)
	}
	if !strings.Contains(msg, "io timeout") {
		t.Fatalf("expected error message to contain inner error, got %q", msg)
	}
	if err.Unwrap() != inner {
		t.Fatal("Unwrap should return the inner error")
	}
}

// ---------------------------------------------------------------------------
// Additional coverage tests
// ---------------------------------------------------------------------------

func TestFastStateMachine_WithLogger(t *testing.T) {
	logger := slog.Default()
	fsm, _ := newTestFSM(WithLogger(logger))
	if fsm.logger != logger {
		t.Fatal("WithLogger did not set the logger")
	}
}

func TestFastStateMachine_Lookup_StorageFallback(t *testing.T) {
	fsm, store := newTestFSM()

	// Write directly to storage (bypassing cache).
	if err := store.Set([]byte("storage-only"), []byte("found-it")); err != nil {
		t.Fatalf("store.Set failed: %v", err)
	}

	// Lookup should find it via storage fallback.
	val := lookupKey(t, fsm, "storage-only")
	if string(val) != "found-it" {
		t.Fatalf("expected 'found-it', got %q", val)
	}
}

func TestFastStateMachine_Lookup_StorageError(t *testing.T) {
	store := &errStorage{getErr: errors.New("storage down")}
	fsm := NewFastStateMachine(1, 1, store)

	// Lookup for a key not in cache will hit storage and get an error.
	_, err := fsm.Lookup(context.Background(), []byte("anything"))
	if err == nil {
		t.Fatal("expected error from storage fallback")
	}

	var storageErr *StorageError
	if !errors.As(err, &storageErr) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
}

func TestFastStateMachine_PrefixScan_StorageIterError(t *testing.T) {
	store := &errStorage{iterErr: errors.New("corruption")}
	fsm := NewFastStateMachine(1, 1, store)

	_, err := fsm.Lookup(context.Background(), &PrefixQuery{Prefix: []byte("pfx/")})
	if err == nil {
		t.Fatal("expected error from storage iterator")
	}

	var storageErr *StorageError
	if !errors.As(err, &storageErr) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
}

func TestFastStateMachine_PrefixUpperBound_AllFF(t *testing.T) {
	// A prefix of all 0xFF bytes should return nil (unbounded).
	prefix := []byte{0xFF, 0xFF, 0xFF}
	result := prefixUpperBound(prefix)
	if result != nil {
		t.Fatalf("expected nil for all-0xFF prefix, got %v", result)
	}
}

func TestFastStateMachine_PrefixUpperBound_Normal(t *testing.T) {
	prefix := []byte("abc")
	result := prefixUpperBound(prefix)
	expected := []byte("abd")
	if !bytes.Equal(result, expected) {
		t.Fatalf("expected %q, got %q", expected, result)
	}
}

func TestFastStateMachine_PrefixUpperBound_Empty(t *testing.T) {
	result := prefixUpperBound(nil)
	if result != nil {
		t.Fatalf("expected nil for empty prefix, got %v", result)
	}
}

func TestFastStateMachine_PrefixUpperBound_TrailingFF(t *testing.T) {
	// "ab\xFF" should roll over to "ac".
	prefix := []byte{0x61, 0x62, 0xFF}
	result := prefixUpperBound(prefix)
	expected := []byte{0x61, 0x63}
	if !bytes.Equal(result, expected) {
		t.Fatalf("expected %v, got %v", expected, result)
	}
}

func TestFastStateMachine_Put_KeyLengthExceedsPayload(t *testing.T) {
	fsm, _ := newTestFSM()

	// Craft a put with keyLen larger than remaining payload.
	cmd := make([]byte, 4) // op(1) + keyLen(2) + 1 byte but keyLen says 100
	cmd[0] = kv.OpPut
	binary.LittleEndian.PutUint16(cmd[1:3], 100)
	cmd[3] = 0x01

	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for key length exceeding payload")
	}

	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestFastStateMachine_Delete_KeyLengthExceedsPayload(t *testing.T) {
	fsm, _ := newTestFSM()

	// Craft a delete with keyLen larger than remaining payload.
	cmd := make([]byte, 4) // op(1) + keyLen(2) + 1 byte but keyLen says 100
	cmd[0] = kv.OpDelete
	binary.LittleEndian.PutUint16(cmd[1:3], 100)
	cmd[3] = 0x01

	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for key length exceeding payload")
	}

	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestFastStateMachine_Snapshot_RecoverLargeDataset(t *testing.T) {
	fsm1, _ := newTestFSM()

	// Populate with more data than shard count to exercise distribution.
	const count = 500
	for i := 0; i < count; i++ {
		key := "large/" + string(rune('A'+i/26%26)) + string(rune('a'+i%26))
		applyPut(t, fsm1, key, "v"+key)
	}

	var buf bytes.Buffer
	stopper := make(chan struct{})
	if err := fsm1.SaveSnapshot(context.Background(), &buf, stopper); err != nil {
		t.Fatalf("SaveSnapshot failed: %v", err)
	}

	fsm2, _ := newTestFSM()
	if err := fsm2.RecoverFromSnapshot(context.Background(), &buf, stopper); err != nil {
		t.Fatalf("RecoverFromSnapshot failed: %v", err)
	}

	// Spot-check a few entries.
	for _, i := range []int{0, 100, 250, 499} {
		key := "large/" + string(rune('A'+i/26%26)) + string(rune('a'+i%26))
		val := lookupKey(t, fsm2, key)
		expected := "v" + key
		if string(val) != expected {
			t.Fatalf("key %s: expected %q, got %q", key, expected, val)
		}
	}
}

// ---------------------------------------------------------------------------
// errStorage: storage that returns errors for coverage testing
// ---------------------------------------------------------------------------

type errStorage struct {
	getErr  error
	iterErr error
}

func (e *errStorage) Get(_ []byte) ([]byte, error) {
	return nil, e.getErr
}

func (e *errStorage) Set(_, _ []byte) error {
	return nil
}

func (e *errStorage) Delete(_ []byte) error {
	return nil
}

func (e *errStorage) Batch() storage.Batch {
	return &memBatch{store: newMemStorage()}
}

func (e *errStorage) NewIterator(_ *storage.IteratorOptions) storage.Iterator {
	return &errIterator{err: e.iterErr}
}

func (e *errStorage) Snapshot() (storage.Snapshot, error) {
	return nil, errors.New("not implemented")
}

func (e *errStorage) List(_ []byte) ([][]byte, error) {
	return nil, e.getErr
}

func (e *errStorage) Exists(_ []byte) (bool, error) {
	return false, e.getErr
}

func (e *errStorage) Close() error {
	return nil
}

type errIterator struct {
	err error
}

func (it *errIterator) SeekToFirst() bool  { return false }
func (it *errIterator) SeekToLast() bool   { return false }
func (it *errIterator) Seek(_ []byte) bool { return false }
func (it *errIterator) Next() bool         { return false }
func (it *errIterator) Prev() bool         { return false }
func (it *errIterator) Valid() bool        { return false }
func (it *errIterator) Key() []byte        { return nil }
func (it *errIterator) Value() []byte      { return nil }
func (it *errIterator) Error() error       { return it.err }
func (it *errIterator) Close() error       { return nil }

// keysOf returns the sorted keys of a map for diagnostic output.
func keysOf(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---------------------------------------------------------------------------
// Tests: ListQuery
// ---------------------------------------------------------------------------

func TestFastStateMachine_ListQuery_AllKeys(t *testing.T) {
	fsm, _ := newTestFSM()

	applyPut(t, fsm, "b", "val-b")
	applyPut(t, fsm, "a", "val-a")
	applyPut(t, fsm, "c", "val-c")
	waitForAsync(fsm)

	result, err := fsm.Lookup(context.Background(), kv.ListQuery{})
	if err != nil {
		t.Fatalf("Lookup(ListQuery{}) failed: %v", err)
	}

	keys, ok := result.([]string)
	if !ok {
		t.Fatalf("expected []string, got %T", result)
	}

	expected := []string{"a", "b", "c"}
	if len(keys) != len(expected) {
		t.Fatalf("expected %d keys, got %d: %v", len(expected), len(keys), keys)
	}
	for i, k := range keys {
		if k != expected[i] {
			t.Fatalf("keys[%d]: expected %q, got %q", i, expected[i], k)
		}
	}
}

func TestFastStateMachine_ListQuery_WithPrefix(t *testing.T) {
	fsm, _ := newTestFSM()

	applyPut(t, fsm, "app:config", "v1")
	applyPut(t, fsm, "app:name", "v2")
	applyPut(t, fsm, "db:host", "v3")
	applyPut(t, fsm, "app:version", "v4")
	waitForAsync(fsm)

	result, err := fsm.Lookup(context.Background(), kv.ListQuery{Prefix: "app:"})
	if err != nil {
		t.Fatalf("Lookup(ListQuery{Prefix: 'app:'}) failed: %v", err)
	}

	keys := result.([]string)
	expected := []string{"app:config", "app:name", "app:version"}
	if len(keys) != len(expected) {
		t.Fatalf("expected %d keys, got %d: %v", len(expected), len(keys), keys)
	}
	for i, k := range keys {
		if k != expected[i] {
			t.Fatalf("keys[%d]: expected %q, got %q", i, expected[i], k)
		}
	}
}

func TestFastStateMachine_ListQuery_NoResults(t *testing.T) {
	fsm, _ := newTestFSM()

	applyPut(t, fsm, "exists", "val")
	waitForAsync(fsm)

	result, err := fsm.Lookup(context.Background(), kv.ListQuery{Prefix: "nonexistent:"})
	if err != nil {
		t.Fatalf("Lookup(ListQuery) failed: %v", err)
	}

	keys := result.([]string)
	if len(keys) != 0 {
		t.Fatalf("expected empty slice, got %v", keys)
	}
}

func TestFastStateMachine_ListQuery_EmptyStore(t *testing.T) {
	fsm, _ := newTestFSM()

	result, err := fsm.Lookup(context.Background(), kv.ListQuery{})
	if err != nil {
		t.Fatalf("Lookup(ListQuery{}) failed: %v", err)
	}

	keys := result.([]string)
	if len(keys) != 0 {
		t.Fatalf("expected empty slice, got %v", keys)
	}
}

func TestFastStateMachine_ListQuery_ExcludesDeletedKeys(t *testing.T) {
	fsm, _ := newTestFSM()

	applyPut(t, fsm, "keep", "val")
	applyPut(t, fsm, "remove", "val")
	applyDelete(t, fsm, "remove")
	waitForAsync(fsm)

	result, err := fsm.Lookup(context.Background(), kv.ListQuery{})
	if err != nil {
		t.Fatalf("Lookup(ListQuery{}) failed: %v", err)
	}

	keys := result.([]string)
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d: %v", len(keys), keys)
	}
	if keys[0] != "keep" {
		t.Fatalf("expected 'keep', got %q", keys[0])
	}
}

func TestFastStateMachine_ListQuery_SortedOrder(t *testing.T) {
	fsm, _ := newTestFSM()

	// Insert in reverse order.
	applyPut(t, fsm, "z", "1")
	applyPut(t, fsm, "m", "2")
	applyPut(t, fsm, "a", "3")
	waitForAsync(fsm)

	result, err := fsm.Lookup(context.Background(), kv.ListQuery{})
	if err != nil {
		t.Fatalf("Lookup(ListQuery{}) failed: %v", err)
	}

	keys := result.([]string)
	if !sort.StringsAreSorted(keys) {
		t.Fatalf("expected sorted keys, got %v", keys)
	}
}

func TestFastStateMachine_ListQuery_MergesStorageAndCache(t *testing.T) {
	fsm, store := newTestFSM()

	// Write directly to storage (simulating pre-existing data).
	_ = store.Set([]byte("storage-only"), []byte("val"))

	// Write through the state machine (lands in cache + async storage).
	applyPut(t, fsm, "cache-and-storage", "val")
	waitForAsync(fsm)

	result, err := fsm.Lookup(context.Background(), kv.ListQuery{})
	if err != nil {
		t.Fatalf("Lookup(ListQuery{}) failed: %v", err)
	}

	keys := result.([]string)
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d: %v", len(keys), keys)
	}
	if keys[0] != "cache-and-storage" || keys[1] != "storage-only" {
		t.Fatalf("unexpected keys: %v", keys)
	}
}
