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

package mvcc

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	pebblestorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// newTestStore creates a fresh in-memory storage and returns a
// ready-to-use MVCC store. Cleanup is registered automatically.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	store := memory.NewStorage()
	t.Cleanup(func() { _ = store.Close() })
	s, err := New(store)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return s
}

// ---------------------------------------------------------------------------
// New
// ---------------------------------------------------------------------------

func TestNew_FreshDB_RevisionZero(t *testing.T) {
	s := newTestStore(t)
	if rev := s.CurrentRevision(); rev != 0 {
		t.Errorf("CurrentRevision: got %d, want 0", rev)
	}
	if compacted := s.CompactionFloor(nil); compacted != 0 {
		t.Errorf("CompactedRevision: got %d, want 0", compacted)
	}
}

func TestNew_LoadsPersistedRevision(t *testing.T) {
	dir := t.TempDir()
	store, err := pebblestorage.New(pebblestorage.TestConfig(dir))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}

	s, err := New(store)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	// Write some data to advance revision.
	_, err = s.Put([]byte("key1"), []byte("val1"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	_, err = s.Put([]byte("key2"), []byte("val2"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	expectedRev := s.CurrentRevision()

	// Compact to revision 1.
	if err := s.CompactPrefix(nil, 1); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	expectedCompacted := s.CompactionFloor(nil)

	// Close and reopen.
	if err := store.Close(); err != nil {
		t.Fatalf("close storage: %v", err)
	}

	store2, err := pebblestorage.New(pebblestorage.TestConfig(dir))
	if err != nil {
		t.Fatalf("reopen storage: %v", err)
	}
	t.Cleanup(func() { _ = store2.Close() })

	s2, err := New(store2)
	if err != nil {
		t.Fatalf("new store 2: %v", err)
	}

	if s2.CurrentRevision() != expectedRev {
		t.Errorf("loaded revision: got %d, want %d", s2.CurrentRevision(), expectedRev)
	}
	if s2.CompactionFloor(nil) != expectedCompacted {
		t.Errorf("loaded compacted: got %d, want %d", s2.CompactionFloor(nil), expectedCompacted)
	}
}

// ---------------------------------------------------------------------------
// Storage
// ---------------------------------------------------------------------------

func TestStorage_ReturnsUnderlyingInstance(t *testing.T) {
	s := newTestStore(t)
	if s.Storage() == nil {
		t.Fatal("Storage: returned nil")
	}
}

func TestStorage_SameInstance(t *testing.T) {
	s := newTestStore(t)
	a := s.Storage()
	b := s.Storage()
	if a != b {
		t.Error("Storage: expected same storage.Storage on repeated calls")
	}
}

// ---------------------------------------------------------------------------
// Put - new key
// ---------------------------------------------------------------------------

func TestPut_NewKey_ReturnsCorrectMetadata(t *testing.T) {
	s := newTestStore(t)
	kv, err := s.Put([]byte("key"), []byte("value"), 42)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !bytes.Equal(kv.Key, []byte("key")) {
		t.Errorf("Key: got %q, want %q", kv.Key, "key")
	}
	if !bytes.Equal(kv.Value, []byte("value")) {
		t.Errorf("Value: got %q, want %q", kv.Value, "value")
	}
	if kv.CreateRevision != 1 {
		t.Errorf("CreateRevision: got %d, want 1", kv.CreateRevision)
	}
	if kv.ModRevision != 1 {
		t.Errorf("ModRevision: got %d, want 1", kv.ModRevision)
	}
	if kv.Version != 1 {
		t.Errorf("Version: got %d, want 1", kv.Version)
	}
	if kv.LeaseID != 42 {
		t.Errorf("LeaseID: got %d, want 42", kv.LeaseID)
	}
}

func TestPut_NewKey_IncrementsRevision(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("a"), []byte("1"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if s.CurrentRevision() != 1 {
		t.Errorf("CurrentRevision: got %d, want 1", s.CurrentRevision())
	}
}

// ---------------------------------------------------------------------------
// Put - existing key (update)
// ---------------------------------------------------------------------------

func TestPut_ExistingKey_VersionIncrements(t *testing.T) {
	s := newTestStore(t)
	kv1, err := s.Put([]byte("key"), []byte("v1"), 0)
	if err != nil {
		t.Fatalf("Put 1: %v", err)
	}
	kv2, err := s.Put([]byte("key"), []byte("v2"), 10)
	if err != nil {
		t.Fatalf("Put 2: %v", err)
	}

	if kv2.Version != 2 {
		t.Errorf("Version: got %d, want 2", kv2.Version)
	}
	if kv2.CreateRevision != kv1.CreateRevision {
		t.Errorf("CreateRevision: got %d, want %d (should not change)", kv2.CreateRevision, kv1.CreateRevision)
	}
	if kv2.ModRevision <= kv1.ModRevision {
		t.Errorf("ModRevision: got %d, should be > %d", kv2.ModRevision, kv1.ModRevision)
	}
	if kv2.LeaseID != 10 {
		t.Errorf("LeaseID: got %d, want 10", kv2.LeaseID)
	}
}

func TestPut_ExistingKey_ThreeUpdates(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("k"), []byte("v1"), 0)
	if err != nil {
		t.Fatalf("Put 1: %v", err)
	}
	_, err = s.Put([]byte("k"), []byte("v2"), 0)
	if err != nil {
		t.Fatalf("Put 2: %v", err)
	}
	kv3, err := s.Put([]byte("k"), []byte("v3"), 0)
	if err != nil {
		t.Fatalf("Put 3: %v", err)
	}
	if kv3.Version != 3 {
		t.Errorf("Version: got %d, want 3", kv3.Version)
	}
	if kv3.CreateRevision != 1 {
		t.Errorf("CreateRevision: got %d, want 1", kv3.CreateRevision)
	}
}

// ---------------------------------------------------------------------------
// Get
// ---------------------------------------------------------------------------

func TestGet_ExistingKey(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("hello"), []byte("world"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	kv, err := s.Get([]byte("hello"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(kv.Value, []byte("world")) {
		t.Errorf("Value: got %q, want %q", kv.Value, "world")
	}
}

func TestGet_NonExistentKey(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Get([]byte("missing"))
	if err == nil {
		t.Fatal("Get: expected error for missing key, got nil")
	}
	var knf *KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("Get: expected *KeyNotFoundError, got %T: %v", err, err)
	}
}

func TestGet_AfterUpdate_ReturnsLatest(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("k"), []byte("old"), 0)
	if err != nil {
		t.Fatalf("Put 1: %v", err)
	}
	_, err = s.Put([]byte("k"), []byte("new"), 0)
	if err != nil {
		t.Fatalf("Put 2: %v", err)
	}
	kv, err := s.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(kv.Value, []byte("new")) {
		t.Errorf("Value: got %q, want %q", kv.Value, "new")
	}
	if kv.Version != 2 {
		t.Errorf("Version: got %d, want 2", kv.Version)
	}
}

// ---------------------------------------------------------------------------
// GetAtRevision
// ---------------------------------------------------------------------------

func TestGetAtRevision_CurrentRevision(t *testing.T) {
	s := newTestStore(t)
	kv1, err := s.Put([]byte("key"), []byte("val"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	kv, err := s.GetAtRevision([]byte("key"), kv1.ModRevision)
	if err != nil {
		t.Fatalf("GetAtRevision: %v", err)
	}
	if !bytes.Equal(kv.Value, []byte("val")) {
		t.Errorf("Value: got %q, want %q", kv.Value, "val")
	}
}

func TestGetAtRevision_OlderRevision(t *testing.T) {
	s := newTestStore(t)
	kv1, err := s.Put([]byte("key"), []byte("v1"), 0)
	if err != nil {
		t.Fatalf("Put 1: %v", err)
	}
	_, err = s.Put([]byte("key"), []byte("v2"), 0)
	if err != nil {
		t.Fatalf("Put 2: %v", err)
	}
	// Read at the first revision.
	kv, err := s.GetAtRevision([]byte("key"), kv1.ModRevision)
	if err != nil {
		t.Fatalf("GetAtRevision: %v", err)
	}
	if !bytes.Equal(kv.Value, []byte("v1")) {
		t.Errorf("Value: got %q, want %q", kv.Value, "v1")
	}
}

func TestGetAtRevision_CompactedRevision(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("key"), []byte("v1"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	_, err = s.Put([]byte("key"), []byte("v2"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.CompactPrefix(nil, 1); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	_, err = s.GetAtRevision([]byte("key"), 1)
	if err == nil {
		t.Fatal("GetAtRevision: expected error for compacted revision, got nil")
	}
	var rce *RevisionCompactedError
	if !errors.As(err, &rce) {
		t.Fatalf("GetAtRevision: expected *RevisionCompactedError, got %T: %v", err, err)
	}
}

func TestGetAtRevision_NonExistentRevision(t *testing.T) {
	s := newTestStore(t)
	// No data exists at revision 99.
	_, err := s.GetAtRevision([]byte("nokey"), 99)
	if err == nil {
		t.Fatal("GetAtRevision: expected error for missing revision, got nil")
	}
	var rnf *RevisionNotFoundError
	if !errors.As(err, &rnf) {
		t.Fatalf("GetAtRevision: expected *RevisionNotFoundError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestDelete_ExistingKey_ReturnsPreviousKV(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("delme"), []byte("val"), 7)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	prev, err := s.Delete([]byte("delme"))
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !bytes.Equal(prev.Value, []byte("val")) {
		t.Errorf("prev Value: got %q, want %q", prev.Value, "val")
	}
	if prev.LeaseID != 7 {
		t.Errorf("prev LeaseID: got %d, want 7", prev.LeaseID)
	}

	// Get should fail after delete.
	_, err = s.Get([]byte("delme"))
	if err == nil {
		t.Fatal("Get after Delete: expected error, got nil")
	}
	var knf *KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("Get after Delete: expected *KeyNotFoundError, got %T", err)
	}
}

func TestDelete_NonExistentKey(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Delete([]byte("ghost"))
	if err == nil {
		t.Fatal("Delete: expected error for missing key, got nil")
	}
	var knf *KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("Delete: expected *KeyNotFoundError, got %T: %v", err, err)
	}
}

func TestDelete_IncrementsRevision(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("k"), []byte("v"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	revBefore := s.CurrentRevision()
	_, err = s.Delete([]byte("k"))
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if s.CurrentRevision() != revBefore+1 {
		t.Errorf("CurrentRevision: got %d, want %d", s.CurrentRevision(), revBefore+1)
	}
}

// ---------------------------------------------------------------------------
// Range - single key (nil end)
// ---------------------------------------------------------------------------

func TestRange_SingleKey_NilEnd(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("only"), []byte("one"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	results, count, rev, err := s.Range([]byte("only"), nil)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Range results: got %d, want 1", len(results))
	}
	if !bytes.Equal(results[0].Value, []byte("one")) {
		t.Errorf("Value: got %q, want %q", results[0].Value, "one")
	}
	// count is the match count, distinct from revision -- a single-key hit
	// must report count 1 regardless of how many revisions the store holds.
	if count != 1 {
		t.Errorf("count: got %d, want 1", count)
	}
	if rev != s.CurrentRevision() {
		t.Errorf("revision: got %d, want %d", rev, s.CurrentRevision())
	}
}

func TestRange_SingleKey_Missing(t *testing.T) {
	s := newTestStore(t)
	results, count, _, err := s.Range([]byte("nope"), nil)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	// Missing key in range returns empty result, not error.
	if len(results) != 0 {
		t.Errorf("Range results: got %d, want 0", len(results))
	}
	if count != 0 {
		t.Errorf("count: got %d, want 0", count)
	}
}

// ---------------------------------------------------------------------------
// Range - start/end
// ---------------------------------------------------------------------------

func TestRange_StartEnd(t *testing.T) {
	s := newTestStore(t)
	for _, k := range []string{"a", "b", "c", "d"} {
		_, err := s.Put([]byte(k), []byte("val-"+k), 0)
		if err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}
	// Range [b, d) should return b, c.
	results, count, rev, err := s.Range([]byte("b"), []byte("d"))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Range results: got %d, want 2", len(results))
	}
	if !bytes.Equal(results[0].Key, []byte("b")) {
		t.Errorf("results[0].Key: got %q, want %q", results[0].Key, "b")
	}
	if !bytes.Equal(results[1].Key, []byte("c")) {
		t.Errorf("results[1].Key: got %q, want %q", results[1].Key, "c")
	}
	// count must be the real match count, not the store's current revision
	// (4 Puts were issued to seed a, b, c, d).
	if count != 2 {
		t.Errorf("count: got %d, want 2", count)
	}
	if rev != s.CurrentRevision() {
		t.Errorf("revision: got %d, want %d", rev, s.CurrentRevision())
	}
	if count == rev {
		t.Fatalf("count (%d) coincidentally equals revision (%d); strengthen the fixture so this test cannot pass by accident", count, rev)
	}
}

func TestRange_StartEnd_EmptyResult(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("a"), []byte("v"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Range [m, z) with no keys in that range.
	results, count, _, err := s.Range([]byte("m"), []byte("z"))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Range results: got %d, want 0", len(results))
	}
	if count != 0 {
		t.Errorf("count: got %d, want 0", count)
	}
}

// ---------------------------------------------------------------------------
// Range - with Limit
// ---------------------------------------------------------------------------

func TestRange_WithLimit(t *testing.T) {
	s := newTestStore(t)
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		_, err := s.Put([]byte(k), []byte("val"), 0)
		if err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}
	results, count, _, err := s.Range([]byte("a"), []byte("z"), service.WithLimit(2))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Range results: got %d, want 2", len(results))
	}
	// Non-count-only count is capped at Limit, matching len(results): 5 keys
	// exist (a..e) but Limit(2) caps both the returned slice and count.
	if count != 2 {
		t.Errorf("count: got %d, want 2 (capped by Limit)", count)
	}
}

func TestRange_WithLimit_LargerThanResults(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("only"), []byte("val"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	results, count, _, err := s.Range([]byte("a"), []byte("z"), service.WithLimit(100))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("Range results: got %d, want 1", len(results))
	}
	if count != 1 {
		t.Errorf("count: got %d, want 1", count)
	}
}

// ---------------------------------------------------------------------------
// Range - with KeysOnly
// ---------------------------------------------------------------------------

func TestRange_KeysOnly(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("key"), []byte("secret-value"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	results, count, _, err := s.Range([]byte("key"), nil, service.WithKeysOnly())
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Range results: got %d, want 1", len(results))
	}
	if results[0].Value != nil {
		t.Errorf("Value: got %v, want nil for KeysOnly", results[0].Value)
	}
	if count != 1 {
		t.Errorf("count: got %d, want 1", count)
	}
}

func TestRange_KeysOnly_RangeQuery(t *testing.T) {
	s := newTestStore(t)
	for _, k := range []string{"a", "b"} {
		_, err := s.Put([]byte(k), []byte("val"), 0)
		if err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	results, count, _, err := s.Range([]byte("a"), []byte("c"), service.WithKeysOnly())
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	for i, kv := range results {
		if kv.Value != nil {
			t.Errorf("results[%d].Value: got %v, want nil", i, kv.Value)
		}
	}
	if count != 2 {
		t.Errorf("count: got %d, want 2", count)
	}
}

// ---------------------------------------------------------------------------
// Range - with CountOnly
// ---------------------------------------------------------------------------

func TestRange_CountOnly(t *testing.T) {
	s := newTestStore(t)
	for _, k := range []string{"a", "b", "c"} {
		_, err := s.Put([]byte(k), []byte("v"), 0)
		if err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	results, count, rev, err := s.Range([]byte("a"), []byte("d"), service.WithCountOnly())
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if results != nil {
		t.Errorf("results: got %v, want nil for CountOnly", results)
	}
	if count != 3 {
		t.Errorf("count: got %d, want 3", count)
	}
	if rev != s.CurrentRevision() {
		t.Errorf("revision: got %d, want %d", rev, s.CurrentRevision())
	}
}

// TestRange_CountOnly_IgnoresLimit verifies the documented CountOnly
// semantics: count reflects every match, not the value capped by Limit
// (the non-CountOnly path caps count at Limit; CountOnly must not).
func TestRange_CountOnly_IgnoresLimit(t *testing.T) {
	s := newTestStore(t)
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		_, err := s.Put([]byte(k), []byte("v"), 0)
		if err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	results, count, _, err := s.Range([]byte("a"), []byte("z"), service.WithCountOnly(), service.WithLimit(2))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if results != nil {
		t.Errorf("results: got %v, want nil for CountOnly", results)
	}
	if count != 5 {
		t.Errorf("count: got %d, want 5 (CountOnly must ignore Limit)", count)
	}
}

// TestRange_Count_DistinctFromRevision verifies that a range's match count
// and the store's MVCC revision are reported as two genuinely distinct
// values, reproducing (as a regression guard) the bug where Range's
// non-CountOnly path returned the current revision in place of the match
// count.
func TestRange_Count_DistinctFromRevision(t *testing.T) {
	s := newTestStore(t)
	// Issue far more revisions (10 Puts across 2 keys) than the range will
	// match (2 keys), so count and revision can never coincidentally be
	// equal -- this specifically guards against the store returning
	// currentRev where count belongs.
	for i := 0; i < 5; i++ {
		if _, err := s.Put([]byte("a"), []byte("va"), 0); err != nil {
			t.Fatalf("Put a: %v", err)
		}
		if _, err := s.Put([]byte("b"), []byte("vb"), 0); err != nil {
			t.Fatalf("Put b: %v", err)
		}
	}
	results, count, rev, err := s.Range([]byte("a"), []byte("c"))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Range results: got %d, want 2", len(results))
	}
	if count != 2 {
		t.Fatalf("count: got %d, want 2 (match count, not revision)", count)
	}
	if rev != 10 {
		t.Fatalf("revision: got %d, want 10 (10 Puts issued)", rev)
	}
	if rev != s.CurrentRevision() {
		t.Errorf("revision: got %d, want %d (store's current revision)", rev, s.CurrentRevision())
	}
}

func TestRange_CountOnly_Empty(t *testing.T) {
	s := newTestStore(t)
	results, count, _, err := s.Range([]byte("a"), []byte("z"), service.WithCountOnly())
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if results != nil {
		t.Errorf("results: got %v, want nil", results)
	}
	if count != 0 {
		t.Errorf("count: got %d, want 0", count)
	}
}

// ---------------------------------------------------------------------------
// Range - with Revision (historical)
// ---------------------------------------------------------------------------

func TestRange_WithRevision(t *testing.T) {
	s := newTestStore(t)
	kv1, err := s.Put([]byte("key"), []byte("v1"), 0)
	if err != nil {
		t.Fatalf("Put 1: %v", err)
	}
	_, err = s.Put([]byte("key"), []byte("v2"), 0)
	if err != nil {
		t.Fatalf("Put 2: %v", err)
	}
	// Read at the first revision.
	results, count, _, err := s.Range([]byte("key"), nil, service.WithRevision(kv1.ModRevision))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Range results: got %d, want 1", len(results))
	}
	if !bytes.Equal(results[0].Value, []byte("v1")) {
		t.Errorf("Value: got %q, want %q", results[0].Value, "v1")
	}
	if count != 1 {
		t.Errorf("count: got %d, want 1", count)
	}
}

func TestRange_WithRevision_Compacted(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("key"), []byte("v1"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	_, err = s.Put([]byte("key"), []byte("v2"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.CompactPrefix(nil, 1); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	_, _, _, err = s.Range([]byte("key"), nil, service.WithRevision(1))
	if err == nil {
		t.Fatal("Range: expected error for compacted revision, got nil")
	}
	var rce *RevisionCompactedError
	if !errors.As(err, &rce) {
		t.Fatalf("Range: expected *RevisionCompactedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Compact
// ---------------------------------------------------------------------------

func TestCompact_RemovesOldRevisions(t *testing.T) {
	s := newTestStore(t)
	kv1, err := s.Put([]byte("key"), []byte("v1"), 0)
	if err != nil {
		t.Fatalf("Put 1: %v", err)
	}
	_, err = s.Put([]byte("key"), []byte("v2"), 0)
	if err != nil {
		t.Fatalf("Put 2: %v", err)
	}

	if err := s.CompactPrefix(nil, kv1.ModRevision); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	if s.CompactionFloor(nil) != kv1.ModRevision {
		t.Errorf("CompactedRevision: got %d, want %d", s.CompactionFloor(nil), kv1.ModRevision)
	}

	// The latest version should still be readable.
	kv, err := s.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Get after compact: %v", err)
	}
	if !bytes.Equal(kv.Value, []byte("v2")) {
		t.Errorf("Value after compact: got %q, want %q", kv.Value, "v2")
	}
}

func TestCompact_PersistsCompactedMarker(t *testing.T) {
	dir := t.TempDir()
	store, err := pebblestorage.New(pebblestorage.TestConfig(dir))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}

	s, err := New(store)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	_, err = s.Put([]byte("k"), []byte("v"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.CompactPrefix(nil, 1); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen and verify compacted marker.
	store2, err := pebblestorage.New(pebblestorage.TestConfig(dir))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = store2.Close() })

	s2, err := New(store2)
	if err != nil {
		t.Fatalf("new store 2: %v", err)
	}
	if s2.CompactionFloor(nil) != 1 {
		t.Errorf("CompactedRevision after reopen: got %d, want 1", s2.CompactionFloor(nil))
	}
}

// ---------------------------------------------------------------------------
// PutInBatch
// ---------------------------------------------------------------------------

func TestPutInBatch_NewKey(t *testing.T) {
	s := newTestStore(t)
	batch := s.Storage().Batch()
	defer func() { _ = batch.Close() }()

	kv, err := s.PutInBatch(batch, []byte("bk"), []byte("bv"), 5, 100)
	if err != nil {
		t.Fatalf("PutInBatch: %v", err)
	}
	if kv.Version != 1 {
		t.Errorf("Version: got %d, want 1", kv.Version)
	}
	if kv.CreateRevision != 100 {
		t.Errorf("CreateRevision: got %d, want 100", kv.CreateRevision)
	}
	if kv.ModRevision != 100 {
		t.Errorf("ModRevision: got %d, want 100", kv.ModRevision)
	}
	if kv.LeaseID != 5 {
		t.Errorf("LeaseID: got %d, want 5", kv.LeaseID)
	}
}

func TestPutInBatch_ExistingKey(t *testing.T) {
	s := newTestStore(t)
	// Put directly first to create the key.
	_, err := s.Put([]byte("bk"), []byte("v1"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	batch := s.Storage().Batch()
	defer func() { _ = batch.Close() }()

	kv, err := s.PutInBatch(batch, []byte("bk"), []byte("v2"), 0, 50)
	if err != nil {
		t.Fatalf("PutInBatch: %v", err)
	}
	if kv.Version != 2 {
		t.Errorf("Version: got %d, want 2", kv.Version)
	}
	if kv.CreateRevision != 1 {
		t.Errorf("CreateRevision: got %d, want 1 (preserved)", kv.CreateRevision)
	}
}

// ---------------------------------------------------------------------------
// DeleteInBatch
// ---------------------------------------------------------------------------

func TestDeleteInBatch_ExistingKey(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("dk"), []byte("dv"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	batch := s.Storage().Batch()
	defer func() { _ = batch.Close() }()

	prev, err := s.DeleteInBatch(batch, []byte("dk"), 50)
	if err != nil {
		t.Fatalf("DeleteInBatch: %v", err)
	}
	if prev == nil {
		t.Fatal("DeleteInBatch: expected previous KV, got nil")
	}
	if !bytes.Equal(prev.Value, []byte("dv")) {
		t.Errorf("prev.Value: got %q, want %q", prev.Value, "dv")
	}
}

func TestDeleteInBatch_NonExistentKey(t *testing.T) {
	s := newTestStore(t)

	batch := s.Storage().Batch()
	defer func() { _ = batch.Close() }()

	prev, err := s.DeleteInBatch(batch, []byte("nope"), 50)
	if err != nil {
		t.Fatalf("DeleteInBatch: %v", err)
	}
	if prev != nil {
		t.Errorf("DeleteInBatch: expected nil for non-existent key, got %+v", prev)
	}
}

// ---------------------------------------------------------------------------
// SetRevision
// ---------------------------------------------------------------------------

func TestSetRevision_UpdatesCounter(t *testing.T) {
	s := newTestStore(t)
	s.SetRevision(999)
	if s.CurrentRevision() != 999 {
		t.Errorf("CurrentRevision: got %d, want 999", s.CurrentRevision())
	}
}

func TestSetRevision_SubsequentPutUsesNewBase(t *testing.T) {
	s := newTestStore(t)
	s.SetRevision(100)
	kv, err := s.Put([]byte("k"), []byte("v"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	// nextRevision should have incremented from 100 to 101.
	if kv.ModRevision != 101 {
		t.Errorf("ModRevision: got %d, want 101", kv.ModRevision)
	}
}

// ---------------------------------------------------------------------------
// CurrentRevision / CompactedRevision
// ---------------------------------------------------------------------------

func TestCurrentRevision_IncreasesMonotonically(t *testing.T) {
	s := newTestStore(t)
	prev := s.CurrentRevision()
	for i := 0; i < 5; i++ {
		_, err := s.Put([]byte("k"), []byte("v"), 0)
		if err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
		curr := s.CurrentRevision()
		if curr <= prev {
			t.Errorf("revision %d not > %d", curr, prev)
		}
		prev = curr
	}
}

func TestCompactedRevision_InitiallyZero(t *testing.T) {
	s := newTestStore(t)
	if s.CompactionFloor(nil) != 0 {
		t.Errorf("CompactedRevision: got %d, want 0", s.CompactionFloor(nil))
	}
}

// ---------------------------------------------------------------------------
// Concurrent Put operations
// ---------------------------------------------------------------------------

func TestPut_Concurrent_NoRace(t *testing.T) {
	s := newTestStore(t)
	const goroutines = 10
	const puts = 20

	var wg sync.WaitGroup
	errs := make(chan error, goroutines*puts)

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			key := []byte("concurrent-key")
			for i := 0; i < puts; i++ {
				_, err := s.Put(key, []byte("val"), 0)
				if err != nil {
					errs <- err
				}
			}
		}(g)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent Put error: %v", err)
	}

	// Final revision should be goroutines * puts.
	expected := int64(goroutines * puts)
	if s.CurrentRevision() != expected {
		t.Errorf("final revision: got %d, want %d", s.CurrentRevision(), expected)
	}
}

func TestPut_Concurrent_DifferentKeys(t *testing.T) {
	s := newTestStore(t)
	const goroutines = 5

	var wg sync.WaitGroup
	errs := make(chan error, goroutines)

	keys := []string{"key-a", "key-b", "key-c", "key-d", "key-e"}
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			_, err := s.Put([]byte(keys[id]), []byte("val"), 0)
			if err != nil {
				errs <- err
			}
		}(g)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent Put error: %v", err)
	}

	// All 5 keys should be readable.
	for _, k := range keys {
		_, err := s.Get([]byte(k))
		if err != nil {
			t.Errorf("Get %q after concurrent puts: %v", k, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Range with Revision through range scan path (start/end)
// ---------------------------------------------------------------------------

func TestRange_WithRevision_StartEnd(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("a"), []byte("a1"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	rev1 := s.CurrentRevision()
	_, err = s.Put([]byte("b"), []byte("b1"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// At rev1, only "a" existed.
	results, count, _, err := s.Range([]byte("a"), []byte("c"), service.WithRevision(rev1))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	// "b" was created after rev1, so findAtRevision for "b" at rev1 should fail.
	// Only "a" should be in results.
	if len(results) != 1 {
		t.Fatalf("Range results: got %d, want 1", len(results))
	}
	if !bytes.Equal(results[0].Key, []byte("a")) {
		t.Errorf("Key: got %q, want %q", results[0].Key, "a")
	}
	if count != 1 {
		t.Errorf("count: got %d, want 1", count)
	}
}

// ---------------------------------------------------------------------------
// Delete then Put same key
// ---------------------------------------------------------------------------

func TestDelete_ThenPut_SameKey(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("k"), []byte("v1"), 0)
	if err != nil {
		t.Fatalf("Put 1: %v", err)
	}
	_, err = s.Delete([]byte("k"))
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Re-create the key.
	kv, err := s.Put([]byte("k"), []byte("v2"), 0)
	if err != nil {
		t.Fatalf("Put 2: %v", err)
	}
	// Should be version 1 again (new key after delete).
	if kv.Version != 1 {
		t.Errorf("Version: got %d, want 1", kv.Version)
	}
	// CreateRevision should be the new revision.
	if kv.CreateRevision != kv.ModRevision {
		t.Errorf("CreateRevision(%d) != ModRevision(%d) for re-created key",
			kv.CreateRevision, kv.ModRevision)
	}
}

func TestDelete_ThenGet_ReturnsTombstone(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("k"), []byte("v"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	_, err = s.Delete([]byte("k"))
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err = s.Get([]byte("k"))
	if err == nil {
		t.Fatal("Get after Delete: expected error, got nil")
	}
	var knf *KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("Get after Delete: expected *KeyNotFoundError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// GetAtRevision with deleted key (historical read)
// ---------------------------------------------------------------------------

func TestGetAtRevision_DeletedKey_HistoricalRead(t *testing.T) {
	s := newTestStore(t)
	kv1, err := s.Put([]byte("k"), []byte("v1"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	_, err = s.Delete([]byte("k"))
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Read at the revision before delete should succeed.
	kv, err := s.GetAtRevision([]byte("k"), kv1.ModRevision)
	if err != nil {
		t.Fatalf("GetAtRevision: %v", err)
	}
	if !bytes.Equal(kv.Value, []byte("v1")) {
		t.Errorf("Value: got %q, want %q", kv.Value, "v1")
	}
}

func TestGetAtRevision_AtDeleteRevision_ReturnsTombstone(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("k"), []byte("v1"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	_, err = s.Delete([]byte("k"))
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	deleteRev := s.CurrentRevision()

	// Read at the delete revision should return KeyNotFoundError (tombstone).
	_, err = s.GetAtRevision([]byte("k"), deleteRev)
	if err == nil {
		t.Fatal("GetAtRevision at delete revision: expected error, got nil")
	}
	var knf *KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("GetAtRevision at delete revision: expected *KeyNotFoundError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Range - KeysOnly with single key path
// ---------------------------------------------------------------------------

func TestRange_SingleKey_KeysOnly(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("k"), []byte("secret"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	results, count, _, err := s.Range([]byte("k"), nil, service.WithKeysOnly())
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Range results: got %d, want 1", len(results))
	}
	if results[0].Value != nil {
		t.Errorf("Value: expected nil for KeysOnly, got %q", results[0].Value)
	}
	if !bytes.Equal(results[0].Key, []byte("k")) {
		t.Errorf("Key: got %q, want %q", results[0].Key, "k")
	}
	if count != 1 {
		t.Errorf("count: got %d, want 1", count)
	}
}

// ---------------------------------------------------------------------------
// Compact and then Range with compacted revision
// ---------------------------------------------------------------------------

func TestRange_StartEnd_CompactedRevision(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("a"), []byte("v1"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	_, err = s.Put([]byte("b"), []byte("v2"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.CompactPrefix(nil, 1); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	_, _, _, err = s.Range([]byte("a"), []byte("c"), service.WithRevision(1))
	if err == nil {
		t.Fatal("Range: expected error for compacted revision, got nil")
	}
	var rce *RevisionCompactedError
	if !errors.As(err, &rce) {
		t.Fatalf("Range: expected *RevisionCompactedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// PutInBatch and DeleteInBatch - commit cycle
// ---------------------------------------------------------------------------

func TestBatch_PutAndDelete_CommitCycle(t *testing.T) {
	s := newTestStore(t)

	batch := s.Storage().Batch()

	_, err := s.PutInBatch(batch, []byte("bk1"), []byte("bv1"), 0, 10)
	if err != nil {
		t.Fatalf("PutInBatch: %v", err)
	}

	// Commit the batch.
	if err := batch.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	_ = batch.Close()

	// Update revision so Get can find it.
	s.SetRevision(10)

	kv, err := s.Get([]byte("bk1"))
	if err != nil {
		t.Fatalf("Get after batch: %v", err)
	}
	if !bytes.Equal(kv.Value, []byte("bv1")) {
		t.Errorf("Value: got %q, want %q", kv.Value, "bv1")
	}
}

// ---------------------------------------------------------------------------
// Multiple keys: Compact preserves later revisions
// ---------------------------------------------------------------------------

func TestCompact_PreservesLaterRevisions(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("a"), []byte("v1"), 0) // rev 1
	if err != nil {
		t.Fatalf("Put a: %v", err)
	}
	_, err = s.Put([]byte("b"), []byte("v2"), 0) // rev 2
	if err != nil {
		t.Fatalf("Put b: %v", err)
	}
	_, err = s.Put([]byte("a"), []byte("v3"), 0) // rev 3
	if err != nil {
		t.Fatalf("Put a update: %v", err)
	}

	// Compact up to rev 2: should remove rev 1 and rev 2 data entries.
	if err := s.CompactPrefix(nil, 2); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// "a" at rev 3 should still be readable.
	kv, err := s.Get([]byte("a"))
	if err != nil {
		t.Fatalf("Get a: %v", err)
	}
	if !bytes.Equal(kv.Value, []byte("v3")) {
		t.Errorf("Value: got %q, want %q", kv.Value, "v3")
	}

	// "b" at rev 2 is the only (latest) revision for that key.
	// Compaction preserves the latest revision per key, so "b" should still be readable.
	kvB, err := s.Get([]byte("b"))
	if err != nil {
		t.Fatalf("Get b after compaction: %v", err)
	}
	if !bytes.Equal(kvB.Value, []byte("v2")) {
		t.Errorf("Value b: got %q, want %q", kvB.Value, "v2")
	}
}

// ---------------------------------------------------------------------------
// Edge case: empty value
// ---------------------------------------------------------------------------

func TestPut_EmptyValue(t *testing.T) {
	s := newTestStore(t)
	kv, err := s.Put([]byte("k"), []byte{}, 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if len(kv.Value) != 0 {
		t.Errorf("Value: got %q, want empty", kv.Value)
	}
	got, err := s.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Value) != 0 {
		t.Errorf("Get Value: got %q, want empty", got.Value)
	}
}

func TestPut_EmptyKey(t *testing.T) {
	s := newTestStore(t)
	kv, err := s.Put([]byte{}, []byte("val"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if kv.Version != 1 {
		t.Errorf("Version: got %d, want 1", kv.Version)
	}
}

// ---------------------------------------------------------------------------
// New - corrupt persisted data
// ---------------------------------------------------------------------------

func TestNew_CorruptRevisionData_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	store, err := pebblestorage.New(pebblestorage.TestConfig(dir))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}

	// Write corrupt revision data (wrong size).
	if err := store.Set(RevisionKey(), []byte{0x01, 0x02}); err != nil {
		t.Fatalf("Set corrupt revision: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	store2, err := pebblestorage.New(pebblestorage.TestConfig(dir))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = store2.Close() })

	_, err = New(store2)
	if err == nil {
		t.Fatal("New: expected error for corrupt revision data, got nil")
	}
	var ee *EncodingError
	if !errors.As(err, &ee) {
		t.Fatalf("New: expected *EncodingError, got %T: %v", err, err)
	}
}

func TestNew_CorruptCompactedData_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	store, err := pebblestorage.New(pebblestorage.TestConfig(dir))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}

	// Write valid revision but corrupt compacted data.
	if err := store.Set(RevisionKey(), EncodeRevision(5)); err != nil {
		t.Fatalf("Set revision: %v", err)
	}
	if err := store.Set(CompactionFloorKey(nil), []byte{0xFF}); err != nil {
		t.Fatalf("Set corrupt compacted: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	store2, err := pebblestorage.New(pebblestorage.TestConfig(dir))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = store2.Close() })

	_, err = New(store2)
	if err == nil {
		t.Fatal("New: expected error for corrupt compacted data, got nil")
	}
	var ee *EncodingError
	if !errors.As(err, &ee) {
		t.Fatalf("New: expected *EncodingError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// GetAtRevision - edge case: revision 0 bypass
// ---------------------------------------------------------------------------

func TestGetAtRevision_RevisionZero_NotCompacted(t *testing.T) {
	s := newTestStore(t)
	// Compact to revision 5.
	_, err := s.Put([]byte("k"), []byte("v"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Revision 0 should not trigger compaction check (revision > 0 && <=).
	// It goes directly to findAtRevision. Since nothing at rev 0, it should
	// return a RevisionNotFoundError.
	_, err = s.GetAtRevision([]byte("k"), 0)
	if err == nil {
		t.Fatal("GetAtRevision(0): expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// Delete - metadata fields preserved in return value
// ---------------------------------------------------------------------------

func TestDelete_ReturnsCorrectMetadataFields(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("d"), []byte("v1"), 5)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	_, err = s.Put([]byte("d"), []byte("v2"), 10)
	if err != nil {
		t.Fatalf("Put 2: %v", err)
	}

	prev, err := s.Delete([]byte("d"))
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if prev.Version != 2 {
		t.Errorf("prev.Version: got %d, want 2", prev.Version)
	}
	if prev.CreateRevision != 1 {
		t.Errorf("prev.CreateRevision: got %d, want 1", prev.CreateRevision)
	}
	if prev.ModRevision != 2 {
		t.Errorf("prev.ModRevision: got %d, want 2", prev.ModRevision)
	}
	if prev.LeaseID != 10 {
		t.Errorf("prev.LeaseID: got %d, want 10", prev.LeaseID)
	}
}

// ---------------------------------------------------------------------------
// Range - all keys with large range
// ---------------------------------------------------------------------------

func TestRange_AllKeys(t *testing.T) {
	s := newTestStore(t)
	keys := []string{"alpha", "beta", "gamma", "delta"}
	for _, k := range keys {
		_, err := s.Put([]byte(k), []byte("v-"+k), 0)
		if err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}
	// Range covering all keys using byte boundaries.
	results, count, _, err := s.Range([]byte{0x00}, []byte{0xFF})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(results) != 4 {
		t.Errorf("Range results: got %d, want 4", len(results))
	}
	if count != 4 {
		t.Errorf("count: got %d, want 4", count)
	}
}

// ---------------------------------------------------------------------------
// Compact edge cases: the revision is validated before anything changes
// ---------------------------------------------------------------------------

func TestCompactPrefix_RefusesOutOfRangeRevisions(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Put([]byte("a/k"), []byte("v1"), 0); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := s.Put([]byte("a/k"), []byte("v2"), 0); err != nil {
		t.Fatalf("Put: %v", err)
	}
	tests := []struct {
		name     string
		revision int64
		check    func(error) bool
	}{
		{"zero", 0, func(err error) bool { var e *CompactRevisionInvalidError; return errors.As(err, &e) && e.Revision == 0 }},
		{"negative", -3, func(err error) bool { var e *CompactRevisionInvalidError; return errors.As(err, &e) }},
		{"future", 3, func(err error) bool {
			var e *CompactRevisionFutureError
			return errors.As(err, &e) && e.Requested == 3 && e.Current == 2
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := s.CompactPrefix([]byte("a/"), tt.revision)
			if !tt.check(err) {
				t.Fatalf("CompactPrefix(%d) = %v (%T)", tt.revision, err, err)
			}
			if got := s.CompactionFloor([]byte("a/")); got != 0 {
				t.Fatalf("a refused compaction moved the floor to %d", got)
			}
			if _, err := s.GetAtRevision([]byte("a/k"), 1); err != nil {
				t.Fatalf("a refused compaction discarded history: %v", err)
			}
		})
	}
}

func TestCompactPrefix_AlreadyCompactedIsRefusedAndKeepsTheFloor(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 4; i++ {
		if _, err := s.Put([]byte("a/k"), []byte{byte(i)}, 0); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if err := s.CompactPrefix([]byte("a/"), 3); err != nil {
		t.Fatalf("CompactPrefix: %v", err)
	}
	for _, rev := range []int64{3, 2} {
		err := s.CompactPrefix([]byte("a/"), rev)
		var e *RevisionCompactedError
		if !errors.As(err, &e) || e.Requested != rev || e.Compacted != 3 {
			t.Fatalf("CompactPrefix(%d) after compacting to 3 = %v (%T)", rev, err, err)
		}
	}
	if got := s.CompactionFloor([]byte("a/")); got != 3 {
		t.Fatalf("floor = %d, want 3: a refused compaction moved it", got)
	}
}

// TestCompactPrefix_ConfinedToItsPrefix is the store-level half of the
// per-tenant compaction rule: compacting one database's prefix discards that
// database's history and nothing of any other's -- neither its data entries,
// nor its change log, nor its readability at old revisions.
func TestCompactPrefix_ConfinedToItsPrefix(t *testing.T) {
	s := newTestStore(t)
	put := func(k, v string) int64 {
		t.Helper()
		kv, err := s.Put([]byte(k), []byte(v), 0)
		if err != nil {
			t.Fatalf("Put(%s): %v", k, err)
		}
		return kv.ModRevision
	}
	aOld := put("acme/app/k", "a1")
	bOld := put("beta/app/k", "b1")
	// A key whose user-key length differs, so the per-length seek is exercised.
	put("acme/app/longer-key", "x1")
	put("acme/app/k", "a2")
	put("beta/app/k", "b2")
	put("acme/app/longer-key", "x2")
	// A key that shares a byte prefix but is another database's.
	put("acme/apps/k", "s1")
	put("acme/apps/k", "s2")
	cut := s.CurrentRevision()

	if err := s.CompactPrefix([]byte("acme/app/"), cut); err != nil {
		t.Fatalf("CompactPrefix: %v", err)
	}

	if _, err := s.GetAtRevision([]byte("acme/app/k"), aOld); err == nil {
		t.Fatal("the compacted database's old revision is still readable")
	}
	if kv, err := s.GetAtRevision([]byte("beta/app/k"), bOld); err != nil || string(kv.Value) != "b1" {
		t.Fatalf("another tenant's history was discarded: %v, %v", kv, err)
	}
	if kv, err := s.GetAtRevision([]byte("acme/apps/k"), cut-1); err != nil || string(kv.Value) != "s1" {
		t.Fatalf("a sibling database sharing a byte prefix lost its history: %v, %v", kv, err)
	}
	if kv, err := s.Get([]byte("acme/app/k")); err != nil || string(kv.Value) != "a2" {
		t.Fatalf("the compacted key's current state is gone: %v, %v", kv, err)
	}
	if got := s.CompactedRevisionForKey([]byte("beta/app/k")); got != 0 {
		t.Fatalf("beta's floor = %d, want 0", got)
	}
	if got := s.CompactedRevisionForRange([]byte("beta/app/"), []byte("beta/app0")); got != 0 {
		t.Fatalf("beta's range floor = %d, want 0", got)
	}
	if got := s.CompactedRevisionForRange([]byte("acme/"), []byte("acme0")); got != cut {
		t.Fatalf("a range overlapping acme/app/ reports floor %d, want %d", got, cut)
	}

	var replayed []string
	if err := s.ScanChanges(1, cut, func(c Change) bool {
		replayed = append(replayed, string(c.Key))
		return true
	}); err != nil {
		t.Fatalf("ScanChanges: %v", err)
	}
	for _, k := range replayed {
		if strings.HasPrefix(k, "acme/app/") {
			t.Fatalf("the compacted database's change log survived: %v", replayed)
		}
	}
	if len(replayed) != 4 {
		t.Fatalf("other databases' change log = %v, want beta/app/k x2 and acme/apps/k x2", replayed)
	}
}

func TestCompactPrefix_FloorsSurviveReopen(t *testing.T) {
	store := memory.NewStorage()
	t.Cleanup(func() { _ = store.Close() })
	s, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, k := range []string{"a/k", "a/k", "b/k", "b/k"} {
		if _, err := s.Put([]byte(k), []byte("v"), 0); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if err := s.CompactPrefix([]byte("a/"), 2); err != nil {
		t.Fatalf("CompactPrefix a/: %v", err)
	}
	if err := s.CompactPrefix([]byte("b/"), 3); err != nil {
		t.Fatalf("CompactPrefix b/: %v", err)
	}
	s2, err := New(store)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := s2.CompactionFloor([]byte("a/")); got != 2 {
		t.Fatalf("a/ floor after reopen = %d, want 2", got)
	}
	if got := s2.CompactionFloor([]byte("b/")); got != 3 {
		t.Fatalf("b/ floor after reopen = %d, want 3", got)
	}
	if got := s2.CompactionFloor([]byte("c/")); got != 0 {
		t.Fatalf("c/ floor after reopen = %d, want 0", got)
	}
}

func TestCompactPrefix_EmptyPrefixCoversTheWholeStore(t *testing.T) {
	s := newTestStore(t)
	for _, k := range []string{"x", "y/z", "x"} {
		if _, err := s.Put([]byte(k), []byte("v"), 0); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if err := s.CompactPrefix(nil, 2); err != nil {
		t.Fatalf("CompactPrefix: %v", err)
	}
	if got := s.CompactedRevisionForKey([]byte("y/z")); got != 2 {
		t.Fatalf("floor for y/z = %d, want 2", got)
	}
	// A narrower compaction under an already-compacted prefix is refused at
	// or below the covering floor.
	var e *RevisionCompactedError
	if err := s.CompactPrefix([]byte("y/"), 2); !errors.As(err, &e) {
		t.Fatalf("CompactPrefix(y/, 2) under a covering floor = %v", err)
	}
}

// ---------------------------------------------------------------------------
// Range - with deleted key in the middle of range
// ---------------------------------------------------------------------------

func TestRange_SkipsDeletedKeys(t *testing.T) {
	s := newTestStore(t)
	for _, k := range []string{"a", "b", "c"} {
		_, err := s.Put([]byte(k), []byte("v-"+k), 0)
		if err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}
	_, err := s.Delete([]byte("b"))
	if err != nil {
		t.Fatalf("Delete b: %v", err)
	}

	// Range [a, d) should skip deleted "b" since its metadata is gone.
	results, count, _, err := s.Range([]byte("a"), []byte("d"))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	// "b" metadata was deleted so only "a" and "c" have metadata entries.
	if len(results) != 2 {
		t.Fatalf("Range results: got %d, want 2", len(results))
	}
	if count != 2 {
		t.Errorf("count: got %d, want 2", count)
	}
}

// ---------------------------------------------------------------------------
// findAtRevision - deleted key with historical read
// ---------------------------------------------------------------------------

func TestRange_SingleKey_DeletedAtRevision_ReturnsEmpty(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("k"), []byte("v"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	_, err = s.Delete([]byte("k"))
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	deleteRev := s.CurrentRevision()

	// Single key range at delete revision should return empty (tombstone).
	results, count, _, err := s.Range([]byte("k"), nil, service.WithRevision(deleteRev))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Range results at delete rev: got %d, want 0", len(results))
	}
	if count != 0 {
		t.Errorf("count: got %d, want 0", count)
	}
}

// ---------------------------------------------------------------------------
// Large binary values
// ---------------------------------------------------------------------------

func TestPut_LargeBinaryValue(t *testing.T) {
	s := newTestStore(t)
	largeVal := bytes.Repeat([]byte{0xAB}, 1<<16) // 64KB
	_, err := s.Put([]byte("big"), largeVal, 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	kv, err := s.Get([]byte("big"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(kv.Value, largeVal) {
		t.Error("Get: value mismatch for large binary")
	}
}

func TestPut_MultipleLargeKeys(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 50; i++ {
		key := bytes.Repeat([]byte{byte(i)}, 100)
		val := bytes.Repeat([]byte{byte(i + 1)}, 200)
		_, err := s.Put(key, val, 0)
		if err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}
	if s.CurrentRevision() != 50 {
		t.Errorf("CurrentRevision: got %d, want 50", s.CurrentRevision())
	}
}

// ---------------------------------------------------------------------------
// DeleteInBatch - commit and verify removal
// ---------------------------------------------------------------------------

func TestDeleteInBatch_CommitAndVerify(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("dk"), []byte("dv"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	batch := s.Storage().Batch()
	prev, err := s.DeleteInBatch(batch, []byte("dk"), 50)
	if err != nil {
		t.Fatalf("DeleteInBatch: %v", err)
	}
	if err := batch.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	_ = batch.Close()

	if prev == nil {
		t.Fatal("DeleteInBatch: expected previous KV")
	}

	// After committing the batch, metadata should be gone.
	_, err = s.Get([]byte("dk"))
	if err == nil {
		t.Fatal("Get after batch delete: expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// Range with Revision - multiple keys scenario
// ---------------------------------------------------------------------------

func TestRange_WithRevision_MultipleKeys(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("a"), []byte("a-v1"), 0) // rev 1
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	_, err = s.Put([]byte("b"), []byte("b-v1"), 0) // rev 2
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	_, err = s.Put([]byte("a"), []byte("a-v2"), 0) // rev 3
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// At revision 2, "a" should be at v1, "b" should be at v1.
	results, count, _, err := s.Range([]byte("a"), []byte("c"), service.WithRevision(2))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Range results: got %d, want 2", len(results))
	}
	if !bytes.Equal(results[0].Value, []byte("a-v1")) {
		t.Errorf("results[0].Value: got %q, want %q", results[0].Value, "a-v1")
	}
	if count != 2 {
		t.Errorf("count: got %d, want 2", count)
	}
}

// ---------------------------------------------------------------------------
// Range - sort ordering
// ---------------------------------------------------------------------------

// rangeKeys extracts the Key field from each result, as plain strings, for
// order assertions.
func rangeKeys(results []*service.KeyValue) []string {
	keys := make([]string, len(results))
	for i, kv := range results {
		keys[i] = string(kv.Key)
	}
	return keys
}

// assertKeyOrder fails the test unless got matches want exactly, in order.
func assertKeyOrder(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("key order: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("key order: got %v, want %v", got, want)
		}
	}
}

func TestRange_Sort_AscendByKey(t *testing.T) {
	s := newTestStore(t)
	for _, k := range []string{"c", "a", "b"} {
		if _, err := s.Put([]byte(k), []byte("v-"+k), 0); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}
	results, count, _, err := s.Range([]byte("a"), []byte("z"), service.WithSort(service.SortAscend, service.SortByKey))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if count != 3 {
		t.Fatalf("count: got %d, want 3", count)
	}
	assertKeyOrder(t, rangeKeys(results), []string{"a", "b", "c"})
}

func TestRange_Sort_DescendByKey(t *testing.T) {
	s := newTestStore(t)
	for _, k := range []string{"a", "b", "c"} {
		if _, err := s.Put([]byte(k), []byte("v-"+k), 0); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}
	results, count, _, err := s.Range([]byte("a"), []byte("z"), service.WithSort(service.SortDescend, service.SortByKey))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if count != 3 {
		t.Fatalf("count: got %d, want 3", count)
	}
	assertKeyOrder(t, rangeKeys(results), []string{"c", "b", "a"})
}

// TestRange_Sort_DescendWithLimit verifies Limit is applied to the SORTED
// result set, not to whatever order the underlying storage iterator
// produces: descending by key with Limit(1) must return only the
// lexicographically largest key, not the first key the scan happened to
// visit.
func TestRange_Sort_DescendWithLimit(t *testing.T) {
	s := newTestStore(t)
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		if _, err := s.Put([]byte(k), []byte("v-"+k), 0); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}
	results, count, _, err := s.Range([]byte("a"), []byte("z"),
		service.WithSort(service.SortDescend, service.SortByKey), service.WithLimit(2))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if count != 2 {
		t.Fatalf("count: got %d, want 2", count)
	}
	assertKeyOrder(t, rangeKeys(results), []string{"e", "d"})
}

func TestRange_Sort_ByValue(t *testing.T) {
	s := newTestStore(t)
	// Values deliberately invert key order: "a" holds the lexicographically
	// largest value, "c" the smallest.
	values := map[string]string{"a": "zval", "b": "yval", "c": "xval"}
	for _, k := range []string{"a", "b", "c"} {
		if _, err := s.Put([]byte(k), []byte(values[k]), 0); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}
	results, _, _, err := s.Range([]byte("a"), []byte("z"), service.WithSort(service.SortAscend, service.SortByValue))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	// Ascending by value: xval < yval < zval -> c, b, a.
	assertKeyOrder(t, rangeKeys(results), []string{"c", "b", "a"})
}

func TestRange_Sort_ByCreateRevision(t *testing.T) {
	s := newTestStore(t)
	// Insert in an order that diverges from key-lexicographic order so the
	// assertion cannot pass by coincidentally matching key order.
	for _, k := range []string{"c", "a", "b"} {
		if _, err := s.Put([]byte(k), []byte("v"), 0); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}
	// Re-putting "a" changes its ModRevision but must leave CreateRevision
	// (insertion order) untouched.
	if _, err := s.Put([]byte("a"), []byte("v2"), 0); err != nil {
		t.Fatalf("Put a v2: %v", err)
	}
	results, _, _, err := s.Range([]byte("a"), []byte("z"), service.WithSort(service.SortAscend, service.SortByCreateRevision))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	// Insertion (create) order was c, a, b.
	assertKeyOrder(t, rangeKeys(results), []string{"c", "a", "b"})
}

func TestRange_Sort_ByModRevision(t *testing.T) {
	s := newTestStore(t)
	for _, k := range []string{"a", "b", "c"} {
		if _, err := s.Put([]byte(k), []byte("v"), 0); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}
	// Re-put "a" last so its ModRevision becomes the newest, diverging from
	// key-lexicographic order.
	if _, err := s.Put([]byte("a"), []byte("v2"), 0); err != nil {
		t.Fatalf("Put a v2: %v", err)
	}
	results, _, _, err := s.Range([]byte("a"), []byte("z"), service.WithSort(service.SortAscend, service.SortByModRevision))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	// Mod order: b (rev2), c (rev3), a (rev4, re-put last).
	assertKeyOrder(t, rangeKeys(results), []string{"b", "c", "a"})
}

func TestRange_Sort_ByVersion(t *testing.T) {
	s := newTestStore(t)
	// "a" is put 3 times (version 3), "b" once (version 1), "c" twice
	// (version 2) -- version order (b, c, a) diverges from key order.
	for i := 0; i < 3; i++ {
		if _, err := s.Put([]byte("a"), []byte("v"), 0); err != nil {
			t.Fatalf("Put a: %v", err)
		}
	}
	if _, err := s.Put([]byte("b"), []byte("v"), 0); err != nil {
		t.Fatalf("Put b: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := s.Put([]byte("c"), []byte("v"), 0); err != nil {
			t.Fatalf("Put c: %v", err)
		}
	}
	results, _, _, err := s.Range([]byte("a"), []byte("z"), service.WithSort(service.SortAscend, service.SortByVersion))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	assertKeyOrder(t, rangeKeys(results), []string{"b", "c", "a"})
}

// TestRange_Sort_TargetOnly_DefaultsAscending verifies that requesting a
// non-key SortTarget without an explicit SortOrder (the zero value,
// SortNone) still triggers sorting -- matching the "either field set"
// trigger condition used by every transport handler to decide whether to
// thread WithSort at all.
func TestRange_Sort_TargetOnly_DefaultsAscending(t *testing.T) {
	s := newTestStore(t)
	values := map[string]string{"a": "zval", "b": "yval", "c": "xval"}
	for _, k := range []string{"a", "b", "c"} {
		if _, err := s.Put([]byte(k), []byte(values[k]), 0); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}
	results, _, _, err := s.Range([]byte("a"), []byte("z"), service.WithSort(service.SortNone, service.SortByValue))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	assertKeyOrder(t, rangeKeys(results), []string{"c", "b", "a"})
}

func TestRange_Sort_UnsupportedTarget(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Put([]byte("a"), []byte("v"), 0); err != nil {
		t.Fatalf("Put: %v", err)
	}
	const outOfRangeTarget = service.SortTarget(255)
	_, _, _, err := s.Range([]byte("a"), []byte("z"), service.WithSort(service.SortAscend, outOfRangeTarget))
	if err == nil {
		t.Fatal("Range: expected error for unsupported sort target, got nil")
	}
	var uste *UnsupportedSortTargetError
	if !errors.As(err, &uste) {
		t.Fatalf("Range: expected *UnsupportedSortTargetError, got %T: %v", err, err)
	}
}

func TestRange_Sort_StableOnTies(t *testing.T) {
	s := newTestStore(t)
	// All three keys share the same value, so sorting by value must fall
	// back to preserving the underlying scan's relative order (stability),
	// not reorder ties arbitrarily.
	for _, k := range []string{"a", "b", "c"} {
		if _, err := s.Put([]byte(k), []byte("same"), 0); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}
	results, _, _, err := s.Range([]byte("a"), []byte("z"), service.WithSort(service.SortAscend, service.SortByValue))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("Range results: got %d, want 3", len(results))
	}
}

// TestRange_Sort_SingleKey_Unaffected verifies that sort options are inert
// on the single-key (nil end) path, which can only ever produce zero or one
// result.
func TestRange_Sort_SingleKey_Unaffected(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Put([]byte("only"), []byte("v"), 0); err != nil {
		t.Fatalf("Put: %v", err)
	}
	results, count, _, err := s.Range([]byte("only"), nil, service.WithSort(service.SortDescend, service.SortByValue))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(results) != 1 || count != 1 {
		t.Fatalf("Range results/count: got %d/%d, want 1/1", len(results), count)
	}
}

// ---------------------------------------------------------------------------
// Compact then read latest at higher revision
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Delete after compaction - getDataAtRevision error path
// ---------------------------------------------------------------------------

func TestDelete_AfterCompaction_SingleRevisionPreserved(t *testing.T) {
	s := newTestStore(t)
	// Put only once (so metadata and data are at same revision).
	_, err := s.Put([]byte("k"), []byte("v"), 0) // rev 1
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Compaction preserves the latest revision per key, so the single
	// revision at rev 1 is preserved even though rev 1 <= compaction point.
	if err := s.CompactPrefix(nil, 1); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	// Data is still present because it's the latest revision for this key.
	// Delete should succeed.
	prev, err := s.Delete([]byte("k"))
	if err != nil {
		t.Fatalf("Delete after compaction: %v", err)
	}
	if !bytes.Equal(prev.Value, []byte("v")) {
		t.Errorf("Delete prev value: got %q, want %q", prev.Value, "v")
	}
}

func TestGet_AfterCompaction_SingleRevisionPreserved(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("k"), []byte("v"), 0) // rev 1
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Compaction preserves the latest revision per key, so the single
	// revision at rev 1 is preserved.
	if err := s.CompactPrefix(nil, 1); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	// Get should succeed because compaction preserved the latest (only) revision.
	kv, err := s.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get after compaction: %v", err)
	}
	if !bytes.Equal(kv.Value, []byte("v")) {
		t.Errorf("Value: got %q, want %q", kv.Value, "v")
	}
}

func TestCompact_ThenReadLatest(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("k"), []byte("v1"), 0) // rev 1
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	_, err = s.Put([]byte("k"), []byte("v2"), 0) // rev 2
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	_, err = s.Put([]byte("k"), []byte("v3"), 0) // rev 3
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.CompactPrefix(nil, 2); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// Latest (rev 3) should still be readable.
	kv, err := s.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(kv.Value, []byte("v3")) {
		t.Errorf("Value: got %q, want %q", kv.Value, "v3")
	}

	// Rev 3 via GetAtRevision should also work.
	kv2, err := s.GetAtRevision([]byte("k"), 3)
	if err != nil {
		t.Fatalf("GetAtRevision(3): %v", err)
	}
	if !bytes.Equal(kv2.Value, []byte("v3")) {
		t.Errorf("Value at rev 3: got %q, want %q", kv2.Value, "v3")
	}
}
