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
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// newTestStore opens a fresh PebbleDB in a temp directory and returns a
// ready-to-use MVCC store. Cleanup is registered automatically.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	db, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := New(db)
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
	if compacted := s.CompactedRevision(); compacted != 0 {
		t.Errorf("CompactedRevision: got %d, want 0", compacted)
	}
}

func TestNew_LoadsPersistedRevision(t *testing.T) {
	dir := t.TempDir()
	db, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}

	s, err := New(db)
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
	if err := s.Compact(1); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	expectedCompacted := s.CompactedRevision()

	// Close and reopen.
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	db2, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		t.Fatalf("reopen pebble: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })

	s2, err := New(db2)
	if err != nil {
		t.Fatalf("new store 2: %v", err)
	}

	if s2.CurrentRevision() != expectedRev {
		t.Errorf("loaded revision: got %d, want %d", s2.CurrentRevision(), expectedRev)
	}
	if s2.CompactedRevision() != expectedCompacted {
		t.Errorf("loaded compacted: got %d, want %d", s2.CompactedRevision(), expectedCompacted)
	}
}

// ---------------------------------------------------------------------------
// DB
// ---------------------------------------------------------------------------

func TestDB_ReturnsUnderlyingInstance(t *testing.T) {
	s := newTestStore(t)
	if s.DB() == nil {
		t.Fatal("DB: returned nil")
	}
}

func TestDB_SameInstance(t *testing.T) {
	s := newTestStore(t)
	a := s.DB()
	b := s.DB()
	if a != b {
		t.Error("DB: expected same pebble.DB pointer on repeated calls")
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
	if err := s.Compact(1); err != nil {
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
	results, rev, err := s.Range([]byte("only"), nil)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Range results: got %d, want 1", len(results))
	}
	if !bytes.Equal(results[0].Value, []byte("one")) {
		t.Errorf("Value: got %q, want %q", results[0].Value, "one")
	}
	if rev != s.CurrentRevision() {
		t.Errorf("revision: got %d, want %d", rev, s.CurrentRevision())
	}
}

func TestRange_SingleKey_Missing(t *testing.T) {
	s := newTestStore(t)
	results, _, err := s.Range([]byte("nope"), nil)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	// Missing key in range returns empty result, not error.
	if len(results) != 0 {
		t.Errorf("Range results: got %d, want 0", len(results))
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
	results, _, err := s.Range([]byte("b"), []byte("d"))
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
}

func TestRange_StartEnd_EmptyResult(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("a"), []byte("v"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Range [m, z) with no keys in that range.
	results, _, err := s.Range([]byte("m"), []byte("z"))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Range results: got %d, want 0", len(results))
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
	results, _, err := s.Range([]byte("a"), []byte("z"), service.WithLimit(2))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Range results: got %d, want 2", len(results))
	}
}

func TestRange_WithLimit_LargerThanResults(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("only"), []byte("val"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	results, _, err := s.Range([]byte("a"), []byte("z"), service.WithLimit(100))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("Range results: got %d, want 1", len(results))
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
	results, _, err := s.Range([]byte("key"), nil, service.WithKeysOnly())
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Range results: got %d, want 1", len(results))
	}
	if results[0].Value != nil {
		t.Errorf("Value: got %v, want nil for KeysOnly", results[0].Value)
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
	results, _, err := s.Range([]byte("a"), []byte("c"), service.WithKeysOnly())
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	for i, kv := range results {
		if kv.Value != nil {
			t.Errorf("results[%d].Value: got %v, want nil", i, kv.Value)
		}
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
	results, count, err := s.Range([]byte("a"), []byte("d"), service.WithCountOnly())
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if results != nil {
		t.Errorf("results: got %v, want nil for CountOnly", results)
	}
	if count != 3 {
		t.Errorf("count: got %d, want 3", count)
	}
}

func TestRange_CountOnly_Empty(t *testing.T) {
	s := newTestStore(t)
	results, count, err := s.Range([]byte("a"), []byte("z"), service.WithCountOnly())
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
	results, _, err := s.Range([]byte("key"), nil, service.WithRevision(kv1.ModRevision))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Range results: got %d, want 1", len(results))
	}
	if !bytes.Equal(results[0].Value, []byte("v1")) {
		t.Errorf("Value: got %q, want %q", results[0].Value, "v1")
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
	if err := s.Compact(1); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	_, _, err = s.Range([]byte("key"), nil, service.WithRevision(1))
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

	if err := s.Compact(kv1.ModRevision); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	if s.CompactedRevision() != kv1.ModRevision {
		t.Errorf("CompactedRevision: got %d, want %d", s.CompactedRevision(), kv1.ModRevision)
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
	db, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}

	s, err := New(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	_, err = s.Put([]byte("k"), []byte("v"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Compact(1); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen and verify compacted marker.
	db2, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })

	s2, err := New(db2)
	if err != nil {
		t.Fatalf("new store 2: %v", err)
	}
	if s2.CompactedRevision() != 1 {
		t.Errorf("CompactedRevision after reopen: got %d, want 1", s2.CompactedRevision())
	}
}

// ---------------------------------------------------------------------------
// PutInBatch
// ---------------------------------------------------------------------------

func TestPutInBatch_NewKey(t *testing.T) {
	s := newTestStore(t)
	batch := s.DB().NewBatch()
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

	batch := s.DB().NewBatch()
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

	batch := s.DB().NewBatch()
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

	batch := s.DB().NewBatch()
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
	if s.CompactedRevision() != 0 {
		t.Errorf("CompactedRevision: got %d, want 0", s.CompactedRevision())
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
	results, _, err := s.Range([]byte("a"), []byte("c"), service.WithRevision(rev1))
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
	results, _, err := s.Range([]byte("k"), nil, service.WithKeysOnly())
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
	if err := s.Compact(1); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	_, _, err = s.Range([]byte("a"), []byte("c"), service.WithRevision(1))
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

	batch := s.DB().NewBatch()

	_, err := s.PutInBatch(batch, []byte("bk1"), []byte("bv1"), 0, 10)
	if err != nil {
		t.Fatalf("PutInBatch: %v", err)
	}

	// Commit the batch.
	if err := batch.Commit(pebble.Sync); err != nil {
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
	if err := s.Compact(2); err != nil {
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

	// "b" at rev 2 was compacted. Metadata still exists, but data is gone.
	// Get should fail because getDataAtRevision for rev 2 won't find the data.
	_, err = s.Get([]byte("b"))
	if err == nil {
		t.Fatal("Get b after compaction: expected error, got nil")
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
	db, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}

	// Write corrupt revision data (wrong size).
	if err := db.Set(revisionKey(), []byte{0x01, 0x02}, pebble.Sync); err != nil {
		t.Fatalf("Set corrupt revision: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db2, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })

	_, err = New(db2)
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
	db, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}

	// Write valid revision but corrupt compacted data.
	if err := db.Set(revisionKey(), encodeRevision(5), pebble.Sync); err != nil {
		t.Fatalf("Set revision: %v", err)
	}
	if err := db.Set(compactedKey(), []byte{0xFF}, pebble.Sync); err != nil {
		t.Fatalf("Set corrupt compacted: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db2, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })

	_, err = New(db2)
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
	results, _, err := s.Range([]byte{0x00}, []byte{0xFF})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(results) != 4 {
		t.Errorf("Range results: got %d, want 4", len(results))
	}
}

// ---------------------------------------------------------------------------
// Compact edge case: compact revision 0
// ---------------------------------------------------------------------------

func TestCompact_RevisionZero(t *testing.T) {
	s := newTestStore(t)
	// No data at all; compact at 0 should succeed.
	if err := s.Compact(0); err != nil {
		t.Fatalf("Compact(0): %v", err)
	}
	if s.CompactedRevision() != 0 {
		t.Errorf("CompactedRevision: got %d, want 0", s.CompactedRevision())
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
	results, _, err := s.Range([]byte("a"), []byte("d"))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	// "b" metadata was deleted so only "a" and "c" have metadata entries.
	if len(results) != 2 {
		t.Fatalf("Range results: got %d, want 2", len(results))
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
	results, _, err := s.Range([]byte("k"), nil, service.WithRevision(deleteRev))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Range results at delete rev: got %d, want 0", len(results))
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

	batch := s.DB().NewBatch()
	prev, err := s.DeleteInBatch(batch, []byte("dk"), 50)
	if err != nil {
		t.Fatalf("DeleteInBatch: %v", err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
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
	results, _, err := s.Range([]byte("a"), []byte("c"), service.WithRevision(2))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Range results: got %d, want 2", len(results))
	}
	if !bytes.Equal(results[0].Value, []byte("a-v1")) {
		t.Errorf("results[0].Value: got %q, want %q", results[0].Value, "a-v1")
	}
}

// ---------------------------------------------------------------------------
// Compact then read latest at higher revision
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Delete after compaction - getDataAtRevision error path
// ---------------------------------------------------------------------------

func TestDelete_AfterCompaction_DataMissing(t *testing.T) {
	s := newTestStore(t)
	// Put only once (so metadata and data are at same revision).
	_, err := s.Put([]byte("k"), []byte("v"), 0) // rev 1
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Compact removes the data entry at rev 1.
	if err := s.Compact(1); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	// Metadata still exists but data is gone. Delete should hit the
	// getDataAtRevision error path.
	_, err = s.Delete([]byte("k"))
	if err == nil {
		t.Fatal("Delete after compaction of data: expected error, got nil")
	}
}

func TestGet_AfterCompaction_DataMissing(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Put([]byte("k"), []byte("v"), 0) // rev 1
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Compact removes data at rev 1 but metadata remains.
	if err := s.Compact(1); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	// Get should fail because data at rev 1 was compacted.
	_, err = s.Get([]byte("k"))
	if err == nil {
		t.Fatal("Get after compaction of data: expected error, got nil")
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
	if err := s.Compact(2); err != nil {
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
