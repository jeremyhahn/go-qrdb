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

package storage

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// Test double: a minimal committed-state Reader
// ---------------------------------------------------------------------------

// mapReader is a committed key-value view used as the base of a BufferedBatch.
type mapReader struct {
	mu      sync.Mutex
	data    map[string]string
	iterErr error
}

func newMapReader(pairs map[string]string) *mapReader {
	data := make(map[string]string, len(pairs))
	for k, v := range pairs {
		data[k] = v
	}
	return &mapReader{data: data}
}

func (m *mapReader) Get(key []byte) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[string(key)]
	if !ok {
		return nil, nil
	}
	return []byte(v), nil
}

func (m *mapReader) NewIterator(opts *IteratorOptions) Iterator {
	if m.iterErr != nil {
		return &sliceIterator{err: m.iterErr}
	}
	if opts == nil {
		opts = &IteratorOptions{}
	}

	m.mu.Lock()
	keys := make([]string, 0, len(m.data))
	for k := range m.data {
		if !withinBounds([]byte(k), opts) {
			continue
		}
		keys = append(keys, k)
	}
	m.mu.Unlock()
	sort.Strings(keys)

	entries := make([]sliceEntry, len(keys))
	for i, k := range keys {
		entries[i] = sliceEntry{key: []byte(k)}
		if !opts.OnlyKeys {
			entries[i].value = []byte(m.data[k])
		}
	}
	return &sliceIterator{entries: entries}
}

// apply commits ops into the map, mirroring a backend's ApplyFunc.
func (m *mapReader) apply(ops []BufferedOp) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, op := range ops {
		if op.Range {
			for k := range m.data {
				if inRange([]byte(k), op.Key, op.RangeEnd) {
					delete(m.data, k)
				}
			}
			continue
		}
		if op.Deleted {
			delete(m.data, string(op.Key))
			continue
		}
		m.data[string(op.Key)] = string(op.Value)
	}
	return nil
}

// collect drains an iterator forward into "key=value" strings.
func collect(t *testing.T, it Iterator) []string {
	t.Helper()
	defer func() { _ = it.Close() }()

	var out []string
	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		out = append(out, string(it.Key())+"="+string(it.Value()))
	}
	if err := it.Error(); err != nil {
		t.Fatalf("iterator error: %v", err)
	}
	return out
}

// ---------------------------------------------------------------------------
// Read-your-own-writes
// ---------------------------------------------------------------------------

// TestBufferedBatch_GetSeesOwnWrites is the contract that makes an apply loop
// deterministic: a write buffered in the batch must be visible to a read taken
// from the same batch before it commits.
func TestBufferedBatch_GetSeesOwnWrites(t *testing.T) {
	base := newMapReader(map[string]string{"committed": "old"})
	b := NewBufferedBatch(base, base.apply)

	if err := b.Set([]byte("pending"), []byte("new")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := b.Set([]byte("committed"), []byte("updated")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	for _, tc := range []struct {
		key  string
		want string
	}{
		{"pending", "new"},
		{"committed", "updated"},
	} {
		got, err := b.Get([]byte(tc.key))
		if err != nil {
			t.Fatalf("Get(%q): %v", tc.key, err)
		}
		if string(got) != tc.want {
			t.Errorf("Get(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

// TestBufferedBatch_GetFallsThroughToBase verifies an untouched key still reads
// the committed value.
func TestBufferedBatch_GetFallsThroughToBase(t *testing.T) {
	base := newMapReader(map[string]string{"a": "1", "b": "2"})
	b := NewBufferedBatch(base, base.apply)

	if err := b.Set([]byte("a"), []byte("shadow")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := b.Get([]byte("b"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "2" {
		t.Errorf("Get(b) = %q, want %q", got, "2")
	}
}

// TestBufferedBatch_GetHidesPendingDelete requires that a buffered delete makes
// the key read as absent even though it is still committed.
func TestBufferedBatch_GetHidesPendingDelete(t *testing.T) {
	base := newMapReader(map[string]string{"gone": "still-there"})
	b := NewBufferedBatch(base, base.apply)

	if err := b.Delete([]byte("gone")); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, err := b.Get([]byte("gone"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Errorf("Get after pending delete = %q, want nil", got)
	}
}

// TestBufferedBatch_GetLastWriteWins requires the most recent buffered write to
// a key to be the one a read observes.
func TestBufferedBatch_GetLastWriteWins(t *testing.T) {
	base := newMapReader(nil)
	b := NewBufferedBatch(base, base.apply)

	for _, v := range []string{"v1", "v2", "v3"} {
		if err := b.Set([]byte("k"), []byte(v)); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	got, err := b.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "v3" {
		t.Errorf("Get = %q, want v3", got)
	}
}

// TestBufferedBatch_GetAfterDeleteThenSet requires a set following a delete to
// win, matching the order the operations will be applied in.
func TestBufferedBatch_GetAfterDeleteThenSet(t *testing.T) {
	base := newMapReader(map[string]string{"k": "committed"})
	b := NewBufferedBatch(base, base.apply)

	if err := b.Delete([]byte("k")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := b.Set([]byte("k"), []byte("reborn")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := b.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "reborn" {
		t.Errorf("Get = %q, want reborn", got)
	}
}

// TestBufferedBatch_GetPreservesEmptyValue requires a zero-length value to stay
// distinguishable from a missing key.
func TestBufferedBatch_GetPreservesEmptyValue(t *testing.T) {
	base := newMapReader(nil)
	b := NewBufferedBatch(base, base.apply)

	if err := b.Set([]byte("empty"), []byte{}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := b.Get([]byte("empty"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("empty value read back as a missing key")
	}
	if len(got) != 0 {
		t.Errorf("Get = %q, want empty", got)
	}
}

// TestBufferedBatch_GetDoesNotAliasCaller verifies the batch copies the values
// it is handed and the values it returns.
func TestBufferedBatch_GetDoesNotAliasCaller(t *testing.T) {
	base := newMapReader(nil)
	b := NewBufferedBatch(base, base.apply)

	key := []byte("k")
	value := []byte("original")
	if err := b.Set(key, value); err != nil {
		t.Fatalf("Set: %v", err)
	}
	value[0] = 'X'
	key[0] = 'X'

	got, err := b.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "original" {
		t.Fatalf("batch aliased the caller's slices: got %q", got)
	}

	got[0] = 'Y'
	again, err := b.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(again) != "original" {
		t.Fatalf("Get returned an aliased slice: got %q", again)
	}
}

// ---------------------------------------------------------------------------
// Overlay iteration
// ---------------------------------------------------------------------------

// TestBufferedBatch_IteratorMergesPendingWrites requires a scan to see the
// batch's pending writes interleaved in key order with the committed ones.
func TestBufferedBatch_IteratorMergesPendingWrites(t *testing.T) {
	base := newMapReader(map[string]string{"a": "1", "c": "3", "e": "5"})
	b := NewBufferedBatch(base, base.apply)

	if err := b.Set([]byte("b"), []byte("2")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := b.Set([]byte("d"), []byte("4")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got := collect(t, b.NewIterator(nil))
	want := []string{"a=1", "b=2", "c=3", "d=4", "e=5"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("iteration = %v, want %v", got, want)
	}
}

// TestBufferedBatch_IteratorShadowsAndHides requires a pending write to replace
// the committed value and a pending delete to remove the key from the scan.
func TestBufferedBatch_IteratorShadowsAndHides(t *testing.T) {
	base := newMapReader(map[string]string{"a": "old", "b": "keep", "c": "doomed"})
	b := NewBufferedBatch(base, base.apply)

	if err := b.Set([]byte("a"), []byte("new")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := b.Delete([]byte("c")); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got := collect(t, b.NewIterator(nil))
	want := []string{"a=new", "b=keep"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("iteration = %v, want %v", got, want)
	}
}

// TestBufferedBatch_IteratorHonoursBounds requires pending writes outside the
// requested range to be excluded, exactly as committed keys are.
func TestBufferedBatch_IteratorHonoursBounds(t *testing.T) {
	base := newMapReader(map[string]string{"a": "1", "m": "13", "z": "26"})
	b := NewBufferedBatch(base, base.apply)

	for _, k := range []string{"b", "n", "y"} {
		if err := b.Set([]byte(k), []byte("pending")); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	got := collect(t, b.NewIterator(&IteratorOptions{
		LowerBound: []byte("b"),
		UpperBound: []byte("y"),
	}))
	want := []string{"b=pending", "m=13", "n=pending"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("bounded iteration = %v, want %v", got, want)
	}
}

// TestBufferedBatch_IteratorOnlyKeys requires values to be omitted from both
// sides of the merge when the caller asks for keys only.
func TestBufferedBatch_IteratorOnlyKeys(t *testing.T) {
	base := newMapReader(map[string]string{"a": "1"})
	b := NewBufferedBatch(base, base.apply)

	if err := b.Set([]byte("b"), []byte("2")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	it := b.NewIterator(&IteratorOptions{OnlyKeys: true})
	defer func() { _ = it.Close() }()

	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		if len(it.Value()) != 0 {
			t.Errorf("OnlyKeys iteration returned value %q for key %q", it.Value(), it.Key())
		}
	}
}

// TestBufferedBatch_IteratorReverse requires the merged result to be delivered
// in descending order when reverse iteration is requested.
func TestBufferedBatch_IteratorReverse(t *testing.T) {
	base := newMapReader(map[string]string{"a": "1", "c": "3"})
	b := NewBufferedBatch(base, base.apply)

	if err := b.Set([]byte("b"), []byte("2")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got := collect(t, b.NewIterator(&IteratorOptions{Reverse: true}))
	want := []string{"c=3", "b=2", "a=1"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("reverse iteration = %v, want %v", got, want)
	}
}

// TestBufferedBatch_IteratorPropagatesBaseError requires a failure in the base
// scan to surface through Error rather than read as an empty range, which would
// silently drop committed data from a merged view.
func TestBufferedBatch_IteratorPropagatesBaseError(t *testing.T) {
	sentinel := errors.New("base scan failed")
	base := newMapReader(nil)
	base.iterErr = sentinel

	b := NewBufferedBatch(base, base.apply)
	if err := b.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	it := b.NewIterator(nil)
	defer func() { _ = it.Close() }()

	if it.SeekToFirst() {
		t.Error("iterator positioned despite a base scan failure")
	}
	if !errors.Is(it.Error(), sentinel) {
		t.Errorf("Error() = %v, want %v", it.Error(), sentinel)
	}
}

// TestBufferedBatch_IteratorSeekAndPrev exercises positioning in both
// directions over a merged range.
func TestBufferedBatch_IteratorSeekAndPrev(t *testing.T) {
	base := newMapReader(map[string]string{"a": "1", "c": "3"})
	b := NewBufferedBatch(base, base.apply)
	if err := b.Set([]byte("b"), []byte("2")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	it := b.NewIterator(nil)
	defer func() { _ = it.Close() }()

	if !it.Seek([]byte("b")) || string(it.Key()) != "b" {
		t.Fatalf("Seek(b) positioned at %q", it.Key())
	}
	if !it.Prev() || string(it.Key()) != "a" {
		t.Fatalf("Prev positioned at %q", it.Key())
	}
	if !it.SeekToLast() || string(it.Key()) != "c" {
		t.Fatalf("SeekToLast positioned at %q", it.Key())
	}
	if it.Next() {
		t.Errorf("Next past the last entry returned true at %q", it.Key())
	}
	if it.Seek([]byte("zz")) {
		t.Error("Seek past the end returned true")
	}
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// TestBufferedBatch_CommitApplies verifies the buffered operations reach the
// backend in order.
func TestBufferedBatch_CommitApplies(t *testing.T) {
	base := newMapReader(map[string]string{"drop": "x"})
	b := NewBufferedBatch(base, base.apply)

	if err := b.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := b.Delete([]byte("drop")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if b.Len() != 2 {
		t.Fatalf("Len = %d, want 2", b.Len())
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	got, _ := base.Get([]byte("k"))
	if string(got) != "v" {
		t.Errorf("committed value = %q, want v", got)
	}
	if gone, _ := base.Get([]byte("drop")); gone != nil {
		t.Errorf("deleted key survived commit: %q", gone)
	}
}

// TestBufferedBatch_CommitWrapsApplyError verifies a backend failure is
// reported as a BatchError rather than swallowed.
func TestBufferedBatch_CommitWrapsApplyError(t *testing.T) {
	sentinel := errors.New("backend down")
	base := newMapReader(nil)
	b := NewBufferedBatch(base, func([]BufferedOp) error { return sentinel })

	if err := b.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	err := b.Commit()
	if err == nil {
		t.Fatal("Commit should report the backend failure")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("Commit error = %v, want to wrap %v", err, sentinel)
	}
	var be *BatchError
	if !errors.As(err, &be) {
		t.Errorf("Commit error is %T, want *BatchError", err)
	}
}

// TestBufferedBatch_RollbackDiscards verifies rolled-back operations never
// reach the backend.
func TestBufferedBatch_RollbackDiscards(t *testing.T) {
	base := newMapReader(nil)
	b := NewBufferedBatch(base, base.apply)

	if err := b.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := b.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got, _ := base.Get([]byte("k")); got != nil {
		t.Errorf("rolled-back write reached the backend: %q", got)
	}
}

// TestBufferedBatch_CloseIsIdempotent verifies repeated Close calls succeed and
// that an unclosed batch discards its operations.
func TestBufferedBatch_CloseIsIdempotent(t *testing.T) {
	base := newMapReader(nil)
	b := NewBufferedBatch(base, base.apply)

	if err := b.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got, _ := base.Get([]byte("k")); got != nil {
		t.Errorf("implicit rollback did not discard the write: %q", got)
	}
}

// TestBufferedBatch_OperationsAfterCloseFail requires every method to report a
// closed batch rather than operate on it.
func TestBufferedBatch_OperationsAfterCloseFail(t *testing.T) {
	base := newMapReader(nil)
	b := NewBufferedBatch(base, base.apply)
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	for name, op := range map[string]func() error{
		"set":      func() error { return b.Set([]byte("k"), []byte("v")) },
		"delete":   func() error { return b.Delete([]byte("k")) },
		"get":      func() error { _, err := b.Get([]byte("k")); return err },
		"commit":   b.Commit,
		"rollback": b.Rollback,
	} {
		if err := op(); err == nil {
			t.Errorf("%s on a closed batch returned nil error", name)
		}
	}

	it := b.NewIterator(nil)
	defer func() { _ = it.Close() }()
	if it.SeekToFirst() {
		t.Error("iterator on a closed batch positioned")
	}
	if it.Error() == nil {
		t.Error("iterator on a closed batch reported no error")
	}
}

// TestBufferedBatch_EmptyKeyRejected requires the empty key to be refused on
// both mutating methods.
func TestBufferedBatch_EmptyKeyRejected(t *testing.T) {
	base := newMapReader(nil)
	b := NewBufferedBatch(base, base.apply)

	if err := b.Set(nil, []byte("v")); !errors.Is(err, ErrEmptyKey) {
		t.Errorf("Set(nil) = %v, want ErrEmptyKey", err)
	}
	if err := b.Delete(nil); !errors.Is(err, ErrEmptyKey) {
		t.Errorf("Delete(nil) = %v, want ErrEmptyKey", err)
	}
}

// TestBufferedBatch_ConcurrentUse exercises the batch from several goroutines
// so the race detector can check its internal synchronization.
func TestBufferedBatch_ConcurrentUse(t *testing.T) {
	base := newMapReader(map[string]string{"seed": "0"})
	b := NewBufferedBatch(base, base.apply)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := []byte(fmt.Sprintf("k%02d", i))
			for j := 0; j < 32; j++ {
				if err := b.Set(key, []byte(fmt.Sprintf("v%d", j))); err != nil {
					t.Errorf("Set: %v", err)
					return
				}
				if _, err := b.Get(key); err != nil {
					t.Errorf("Get: %v", err)
					return
				}
				_ = b.NewIterator(nil).Close()
			}
		}(i)
	}
	wg.Wait()

	if err := b.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	for i := 0; i < 8; i++ {
		got, err := base.Get([]byte(fmt.Sprintf("k%02d", i)))
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !bytes.Equal(got, []byte("v31")) {
			t.Errorf("key k%02d = %q, want v31", i, got)
		}
	}
}

// ---------------------------------------------------------------------------
// sliceIterator
// ---------------------------------------------------------------------------

// TestSliceIterator_UnpositionedMovesAreNoops requires Next and Prev to refuse
// to move an iterator that was never positioned.
func TestSliceIterator_UnpositionedMovesAreNoops(t *testing.T) {
	it := &sliceIterator{entries: []sliceEntry{{key: []byte("a")}}}

	if it.Next() {
		t.Error("Next on an unpositioned iterator returned true")
	}
	if it.Prev() {
		t.Error("Prev on an unpositioned iterator returned true")
	}
	if it.Valid() {
		t.Error("an unpositioned iterator reported Valid")
	}
	if it.Key() != nil || it.Value() != nil {
		t.Error("an unpositioned iterator returned a key or value")
	}
	if err := it.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestSliceIterator_EmptyRange requires every seek to fail on an empty range.
func TestSliceIterator_EmptyRange(t *testing.T) {
	it := &sliceIterator{}

	if it.SeekToFirst() || it.SeekToLast() || it.Seek([]byte("a")) {
		t.Error("a seek succeeded on an empty range")
	}
}

// TestSliceIterator_ErrorBlocksAllMovement requires a construction failure to
// keep the iterator permanently invalid.
func TestSliceIterator_ErrorBlocksAllMovement(t *testing.T) {
	sentinel := errors.New("broken")
	it := &sliceIterator{entries: []sliceEntry{{key: []byte("a")}}, err: sentinel}

	if it.SeekToFirst() || it.SeekToLast() || it.Seek([]byte("a")) {
		t.Error("a seek succeeded on a failed iterator")
	}
	if it.Next() || it.Prev() {
		t.Error("a move succeeded on a failed iterator")
	}
	if !errors.Is(it.Error(), sentinel) {
		t.Errorf("Error() = %v, want %v", it.Error(), sentinel)
	}
}

// ---------------------------------------------------------------------------
// withinBounds
// ---------------------------------------------------------------------------

// TestWithinBounds covers every combination of open and closed bounds.
func TestWithinBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
		opts IteratorOptions
		want bool
	}{
		{"unbounded", "m", IteratorOptions{}, true},
		{"at lower bound", "b", IteratorOptions{LowerBound: []byte("b")}, true},
		{"below lower bound", "a", IteratorOptions{LowerBound: []byte("b")}, false},
		{"at upper bound", "y", IteratorOptions{UpperBound: []byte("y")}, false},
		{"below upper bound", "x", IteratorOptions{UpperBound: []byte("y")}, true},
		{"inside both", "m", IteratorOptions{LowerBound: []byte("b"), UpperBound: []byte("y")}, true},
		{"outside both", "z", IteratorOptions{LowerBound: []byte("b"), UpperBound: []byte("y")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			if got := withinBounds([]byte(tc.key), &opts); got != tc.want {
				t.Errorf("withinBounds(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}
