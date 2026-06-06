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

package engine

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	qkv "github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// ---------------------------------------------------------------------------
// UpdateIndexes
// ---------------------------------------------------------------------------

func TestUpdateIndexes_JSONValue(t *testing.T) {
	idx := index.NewMemoryIndex("by_name", false)
	indexes := map[string]index.Index{"by_name": idx}
	indexFields := map[string]string{"by_name": "name"}

	key := []byte("user:1")
	value := []byte(`{"name":"alice","age":30}`)

	if err := UpdateIndexes(indexes, indexFields, key, value, nil); err != nil {
		t.Fatalf("UpdateIndexes: %v", err)
	}

	ids, err := idx.Get([]byte("alice"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected 1 ID, got %d", len(ids))
	}
}

func TestUpdateIndexes_NonJSONValue(t *testing.T) {
	idx := index.NewMemoryIndex("by_name", false)
	indexes := map[string]index.Index{"by_name": idx}
	indexFields := map[string]string{"by_name": "name"}

	key := []byte("user:1")
	value := []byte("not-json")

	// Should not panic or index anything.
	if err := UpdateIndexes(indexes, indexFields, key, value, nil); err != nil {
		t.Fatalf("UpdateIndexes: %v", err)
	}

	ids, err := idx.Get([]byte("not-json"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected 0 IDs for non-JSON value, got %d", len(ids))
	}
}

func TestUpdateIndexes_PreviousValueCleanup(t *testing.T) {
	idx := index.NewMemoryIndex("by_name", false)
	indexes := map[string]index.Index{"by_name": idx}
	indexFields := map[string]string{"by_name": "name"}

	key := []byte("user:1")

	// First insert.
	oldValue := []byte(`{"name":"alice"}`)
	if err := UpdateIndexes(indexes, indexFields, key, oldValue, nil); err != nil {
		t.Fatalf("UpdateIndexes (first insert): %v", err)
	}

	// Update with previous value.
	newValue := []byte(`{"name":"bob"}`)
	prevKv := &service.KeyValue{Key: key, Value: oldValue}
	if err := UpdateIndexes(indexes, indexFields, key, newValue, prevKv); err != nil {
		t.Fatalf("UpdateIndexes (update): %v", err)
	}

	// Old value should be gone.
	oldIDs, err := idx.Get([]byte("alice"))
	if err != nil {
		t.Fatalf("Get(alice): %v", err)
	}
	if len(oldIDs) != 0 {
		t.Errorf("expected old index entry removed, got %d IDs", len(oldIDs))
	}

	// New value should be present.
	newIDs, err := idx.Get([]byte("bob"))
	if err != nil {
		t.Fatalf("Get(bob): %v", err)
	}
	if len(newIDs) != 1 {
		t.Errorf("expected 1 ID for new value, got %d", len(newIDs))
	}
}

func TestUpdateIndexes_MissingField(t *testing.T) {
	idx := index.NewMemoryIndex("by_email", false)
	indexes := map[string]index.Index{"by_email": idx}
	indexFields := map[string]string{"by_email": "email"}

	key := []byte("user:1")
	value := []byte(`{"name":"alice"}`)

	if err := UpdateIndexes(indexes, indexFields, key, value, nil); err != nil {
		t.Fatalf("UpdateIndexes: %v", err)
	}

	// No "email" field in the JSON, so nothing should be indexed.
	all, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("expected 0 IDs when field is missing, got %d", len(all))
	}
}

func TestUpdateIndexes_EmptyIndexesMap(t *testing.T) {
	// Should return immediately without error.
	if err := UpdateIndexes(nil, nil, []byte("key"), []byte(`{"a":"b"}`), nil); err != nil {
		t.Fatalf("UpdateIndexes(nil): %v", err)
	}
	if err := UpdateIndexes(map[string]index.Index{}, map[string]string{}, []byte("key"), []byte(`{"a":"b"}`), nil); err != nil {
		t.Fatalf("UpdateIndexes(empty): %v", err)
	}
}

// TestUpdateIndexes_UniqueViolationReturnsError verifies that inserting a
// second entity with the same indexed field value on a unique index returns
// a UniqueViolationError instead of silently succeeding.
func TestUpdateIndexes_UniqueViolationReturnsError(t *testing.T) {
	idx := index.NewMemoryIndex("by_email", true) // unique=true
	indexes := map[string]index.Index{"by_email": idx}
	indexFields := map[string]string{"by_email": "email"}

	// First insert: should succeed.
	if err := UpdateIndexes(indexes, indexFields,
		[]byte("user:1"), []byte(`{"email":"alice@example.com"}`), nil); err != nil {
		t.Fatalf("first insert: unexpected error: %v", err)
	}

	// Second insert with same email on a different key: must return an error.
	err := UpdateIndexes(indexes, indexFields,
		[]byte("user:2"), []byte(`{"email":"alice@example.com"}`), nil)
	if err == nil {
		t.Fatal("expected unique constraint error, got nil")
	}

	var uv *index.UniqueViolationError
	if !errors.As(err, &uv) {
		t.Errorf("expected *index.UniqueViolationError, got %T: %v", err, err)
	}
}

// TestUpdateIndexes_UniqueViolationDoesNotWriteBatch verifies that when a
// unique constraint violation occurs, the caller's batch is left untouched
// (the document write is aborted before PutInBatch is called). This is
// validated indirectly through the MVCC engine in mvcc_engine_test.go; here
// we confirm the UpdateIndexes function itself returns the error cleanly.
func TestUpdateIndexes_SameKeyUpdateNoViolation(t *testing.T) {
	idx := index.NewMemoryIndex("by_email", true) // unique=true
	indexes := map[string]index.Index{"by_email": idx}
	indexFields := map[string]string{"by_email": "email"}

	key := []byte("user:1")
	oldVal := []byte(`{"email":"alice@example.com"}`)

	if err := UpdateIndexes(indexes, indexFields, key, oldVal, nil); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	// Updating the same key to a new email should not be a violation.
	newVal := []byte(`{"email":"alice2@example.com"}`)
	prevKv := &service.KeyValue{Key: key, Value: oldVal}
	if err := UpdateIndexes(indexes, indexFields, key, newVal, prevKv); err != nil {
		t.Fatalf("update same key: unexpected error: %v", err)
	}

	// Old email must no longer be indexed.
	old, err := idx.Get([]byte("alice@example.com"))
	if err != nil {
		t.Fatalf("Get(old): %v", err)
	}
	if len(old) != 0 {
		t.Errorf("old email still indexed: %d entries", len(old))
	}

	// New email must be indexed.
	updated, err := idx.Get([]byte("alice2@example.com"))
	if err != nil {
		t.Fatalf("Get(new): %v", err)
	}
	if len(updated) != 1 {
		t.Errorf("new email not indexed: %d entries", len(updated))
	}
}

// ---------------------------------------------------------------------------
// RemoveIndexes
// ---------------------------------------------------------------------------

func TestRemoveIndexes_JSONValue(t *testing.T) {
	idx := index.NewMemoryIndex("by_name", false)
	indexes := map[string]index.Index{"by_name": idx}
	indexFields := map[string]string{"by_name": "name"}

	key := []byte("user:1")
	value := []byte(`{"name":"alice"}`)

	// First add the index entry.
	if err := UpdateIndexes(indexes, indexFields, key, value, nil); err != nil {
		t.Fatalf("UpdateIndexes: %v", err)
	}
	ids, _ := idx.Get([]byte("alice"))
	if len(ids) != 1 {
		t.Fatalf("setup: expected 1 ID, got %d", len(ids))
	}

	// Now remove it.
	prevKv := &service.KeyValue{Key: key, Value: value}
	if err := RemoveIndexes(indexes, indexFields, key, prevKv); err != nil {
		t.Fatalf("RemoveIndexes: %v", err)
	}

	ids, err := idx.Get([]byte("alice"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected 0 IDs after remove, got %d", len(ids))
	}
}

func TestRemoveIndexes_NilPrevKv(t *testing.T) {
	idx := index.NewMemoryIndex("by_name", false)
	indexes := map[string]index.Index{"by_name": idx}
	indexFields := map[string]string{"by_name": "name"}

	// Should not panic when prevKv is nil.
	if err := RemoveIndexes(indexes, indexFields, []byte("key"), nil); err != nil {
		t.Fatalf("RemoveIndexes(nil prevKv): %v", err)
	}

	all, _ := idx.GetAll()
	if len(all) != 0 {
		t.Errorf("expected 0 IDs, got %d", len(all))
	}
}

func TestRemoveIndexes_EmptyIndexes(t *testing.T) {
	prevKv := &service.KeyValue{Key: []byte("key"), Value: []byte(`{"name":"alice"}`)}
	// Should return immediately without error.
	if err := RemoveIndexes(nil, nil, []byte("key"), prevKv); err != nil {
		t.Fatalf("RemoveIndexes(nil): %v", err)
	}
	if err := RemoveIndexes(map[string]index.Index{}, map[string]string{}, []byte("key"), prevKv); err != nil {
		t.Fatalf("RemoveIndexes(empty): %v", err)
	}
}

func TestRemoveIndexes_NonJSONPrevValue(t *testing.T) {
	idx := index.NewMemoryIndex("by_name", false)
	indexes := map[string]index.Index{"by_name": idx}
	indexFields := map[string]string{"by_name": "name"}

	prevKv := &service.KeyValue{Key: []byte("key"), Value: []byte("not-json")}
	// Should not panic for non-JSON previous value.
	if err := RemoveIndexes(indexes, indexFields, []byte("key"), prevKv); err != nil {
		t.Fatalf("RemoveIndexes(non-JSON): %v", err)
	}
}

// ---------------------------------------------------------------------------
// BackfillIndex
// ---------------------------------------------------------------------------

func TestBackfillIndex_MatchingEntries(t *testing.T) {
	idx := index.NewMemoryIndex("by_city", false)

	scanner := func() ([]*service.KeyValue, error) {
		return []*service.KeyValue{
			{Key: []byte("user:1"), Value: []byte(`{"city":"nyc"}`)},
			{Key: []byte("user:2"), Value: []byte(`{"city":"sf"}`)},
			{Key: []byte("user:3"), Value: []byte(`{"city":"nyc"}`)},
		}, nil
	}

	BackfillIndex(scanner, idx, "city")

	nycIDs, err := idx.Get([]byte("nyc"))
	if err != nil {
		t.Fatalf("Get(nyc): %v", err)
	}
	if len(nycIDs) != 2 {
		t.Errorf("expected 2 IDs for nyc, got %d", len(nycIDs))
	}

	sfIDs, err := idx.Get([]byte("sf"))
	if err != nil {
		t.Fatalf("Get(sf): %v", err)
	}
	if len(sfIDs) != 1 {
		t.Errorf("expected 1 ID for sf, got %d", len(sfIDs))
	}
}

func TestBackfillIndex_NonMatchingEntries(t *testing.T) {
	idx := index.NewMemoryIndex("by_city", false)

	scanner := func() ([]*service.KeyValue, error) {
		return []*service.KeyValue{
			{Key: []byte("user:1"), Value: []byte(`{"name":"alice"}`)},
			{Key: []byte("user:2"), Value: []byte("not-json")},
			{Key: []byte("user:3"), Value: nil},
		}, nil
	}

	BackfillIndex(scanner, idx, "city")

	all, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("expected 0 IDs for non-matching entries, got %d", len(all))
	}
}

func TestBackfillIndex_ScannerError(t *testing.T) {
	idx := index.NewMemoryIndex("by_city", false)

	scanner := func() ([]*service.KeyValue, error) {
		return nil, errors.New("scan failed")
	}

	// Should not panic on scanner error.
	BackfillIndex(scanner, idx, "city")

	all, _ := idx.GetAll()
	if len(all) != 0 {
		t.Errorf("expected 0 IDs after scanner error, got %d", len(all))
	}
}

func TestBackfillIndex_EmptyValues(t *testing.T) {
	idx := index.NewMemoryIndex("by_city", false)

	scanner := func() ([]*service.KeyValue, error) {
		return []*service.KeyValue{
			{Key: []byte("k1"), Value: []byte{}},
			{Key: []byte("k2"), Value: nil},
		}, nil
	}

	BackfillIndex(scanner, idx, "city")

	all, _ := idx.GetAll()
	if len(all) != 0 {
		t.Errorf("expected 0 IDs for empty values, got %d", len(all))
	}
}

// ---------------------------------------------------------------------------
// UnquoteJSON
// ---------------------------------------------------------------------------

func TestUnquoteJSON_QuotedString(t *testing.T) {
	raw := json.RawMessage(`"hello world"`)
	got := UnquoteJSON(raw)
	if string(got) != "hello world" {
		t.Errorf("UnquoteJSON(%q) = %q, want %q", raw, got, "hello world")
	}
}

func TestUnquoteJSON_Number(t *testing.T) {
	raw := json.RawMessage(`42`)
	got := UnquoteJSON(raw)
	if string(got) != "42" {
		t.Errorf("UnquoteJSON(%q) = %q, want %q", raw, got, "42")
	}
}

func TestUnquoteJSON_Boolean(t *testing.T) {
	raw := json.RawMessage(`true`)
	got := UnquoteJSON(raw)
	if string(got) != "true" {
		t.Errorf("UnquoteJSON(%q) = %q, want %q", raw, got, "true")
	}
}

func TestUnquoteJSON_Null(t *testing.T) {
	raw := json.RawMessage(`null`)
	got := UnquoteJSON(raw)
	// json.Unmarshal of null into a string succeeds with the zero value "",
	// so UnquoteJSON returns "" for null.
	if string(got) != "" {
		t.Errorf("UnquoteJSON(%q) = %q, want %q", raw, got, "")
	}
}

func TestUnquoteJSON_FloatNumber(t *testing.T) {
	raw := json.RawMessage(`3.14`)
	got := UnquoteJSON(raw)
	if string(got) != "3.14" {
		t.Errorf("UnquoteJSON(%q) = %q, want %q", raw, got, "3.14")
	}
}

func TestUnquoteJSON_EmptyString(t *testing.T) {
	raw := json.RawMessage(`""`)
	got := UnquoteJSON(raw)
	if string(got) != "" {
		t.Errorf("UnquoteJSON(%q) = %q, want %q", raw, got, "")
	}
}

// ---------------------------------------------------------------------------
// UpdateIndexesBatch
// ---------------------------------------------------------------------------

func TestUpdateIndexesBatch_JSONValue(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := indexTestLogger(t)
	idx, err := index.NewStoragePebbleIndex(stg, index.IndexConfig{
		Name:          "by_name",
		Type:          index.IndexTypeNormal,
		CaseSensitive: true,
	}, log)
	if err != nil {
		t.Fatalf("NewStoragePebbleIndex: %v", err)
	}

	indexes := map[string]index.Index{"by_name": idx}
	indexFields := map[string]string{"by_name": "name"}

	batch := stg.Batch()
	bErr := UpdateIndexesBatch(batch, indexes, indexFields,
		[]byte("user:1"), []byte(`{"name":"alice"}`), nil)
	if bErr != nil {
		t.Fatalf("UpdateIndexesBatch: %v", bErr)
	}
	if cErr := batch.Commit(); cErr != nil {
		t.Fatalf("Commit: %v", cErr)
	}

	ids, err := idx.Get([]byte("alice"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected 1 ID, got %d", len(ids))
	}
	if string(ids[0]) != "user:1" {
		t.Errorf("expected key %q, got %q", "user:1", string(ids[0]))
	}
}

func TestUpdateIndexesBatch_NonJSON(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := indexTestLogger(t)
	idx, err := index.NewStoragePebbleIndex(stg, index.IndexConfig{
		Name:          "by_name",
		Type:          index.IndexTypeNormal,
		CaseSensitive: true,
	}, log)
	if err != nil {
		t.Fatalf("NewStoragePebbleIndex: %v", err)
	}

	indexes := map[string]index.Index{"by_name": idx}
	indexFields := map[string]string{"by_name": "name"}

	batch := stg.Batch()
	bErr := UpdateIndexesBatch(batch, indexes, indexFields,
		[]byte("user:1"), []byte("not-json"), nil)
	if bErr != nil {
		t.Fatalf("UpdateIndexesBatch: %v", bErr)
	}
	if cErr := batch.Commit(); cErr != nil {
		t.Fatalf("Commit: %v", cErr)
	}

	// Non-JSON value should result in nothing indexed.
	ids, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected 0 IDs for non-JSON value, got %d", len(ids))
	}
}

func TestUpdateIndexesBatch_WithPreviousValue(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := indexTestLogger(t)
	idx, err := index.NewStoragePebbleIndex(stg, index.IndexConfig{
		Name:          "by_name",
		Type:          index.IndexTypeNormal,
		CaseSensitive: true,
	}, log)
	if err != nil {
		t.Fatalf("NewStoragePebbleIndex: %v", err)
	}

	indexes := map[string]index.Index{"by_name": idx}
	indexFields := map[string]string{"by_name": "name"}

	// First insert.
	batch1 := stg.Batch()
	bErr := UpdateIndexesBatch(batch1, indexes, indexFields,
		[]byte("user:1"), []byte(`{"name":"alice"}`), nil)
	if bErr != nil {
		t.Fatalf("UpdateIndexesBatch (first): %v", bErr)
	}
	if cErr := batch1.Commit(); cErr != nil {
		t.Fatalf("Commit (first): %v", cErr)
	}

	// Update with previous value -- old entry should be removed, new one added.
	prevKv := &qkv.KeyValue{Key: []byte("user:1"), Value: []byte(`{"name":"alice"}`)}
	batch2 := stg.Batch()
	bErr = UpdateIndexesBatch(batch2, indexes, indexFields,
		[]byte("user:1"), []byte(`{"name":"bob"}`), prevKv)
	if bErr != nil {
		t.Fatalf("UpdateIndexesBatch (update): %v", bErr)
	}
	if cErr := batch2.Commit(); cErr != nil {
		t.Fatalf("Commit (update): %v", cErr)
	}

	// Old value should be gone.
	oldIDs, err := idx.Get([]byte("alice"))
	if err != nil {
		t.Fatalf("Get(alice): %v", err)
	}
	if len(oldIDs) != 0 {
		t.Errorf("expected old index entry removed, got %d IDs", len(oldIDs))
	}

	// New value should be present.
	newIDs, err := idx.Get([]byte("bob"))
	if err != nil {
		t.Fatalf("Get(bob): %v", err)
	}
	if len(newIDs) != 1 {
		t.Errorf("expected 1 ID for new value, got %d", len(newIDs))
	}
}

// ---------------------------------------------------------------------------
// RemoveIndexesBatch
// ---------------------------------------------------------------------------

func TestRemoveIndexesBatch_RemovesEntry(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := indexTestLogger(t)
	idx, err := index.NewStoragePebbleIndex(stg, index.IndexConfig{
		Name:          "by_name",
		Type:          index.IndexTypeNormal,
		CaseSensitive: true,
	}, log)
	if err != nil {
		t.Fatalf("NewStoragePebbleIndex: %v", err)
	}

	indexes := map[string]index.Index{"by_name": idx}
	indexFields := map[string]string{"by_name": "name"}

	// Add an entry via batch.
	batch1 := stg.Batch()
	bErr := UpdateIndexesBatch(batch1, indexes, indexFields,
		[]byte("user:1"), []byte(`{"name":"alice"}`), nil)
	if bErr != nil {
		t.Fatalf("UpdateIndexesBatch: %v", bErr)
	}
	if cErr := batch1.Commit(); cErr != nil {
		t.Fatalf("Commit: %v", cErr)
	}

	// Verify entry exists.
	ids, err := idx.Get([]byte("alice"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("setup: expected 1 ID, got %d", len(ids))
	}

	// Remove via RemoveIndexesBatch.
	prevKv := &qkv.KeyValue{Key: []byte("user:1"), Value: []byte(`{"name":"alice"}`)}
	batch2 := stg.Batch()
	rErr := RemoveIndexesBatch(batch2, indexes, indexFields, []byte("user:1"), prevKv)
	if rErr != nil {
		t.Fatalf("RemoveIndexesBatch: %v", rErr)
	}
	if cErr := batch2.Commit(); cErr != nil {
		t.Fatalf("Commit: %v", cErr)
	}

	// Verify entry is gone.
	ids, err = idx.Get([]byte("alice"))
	if err != nil {
		t.Fatalf("Get after remove: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected 0 IDs after remove, got %d", len(ids))
	}
}

// ---------------------------------------------------------------------------
// BackfillIndexWithPrefix
// ---------------------------------------------------------------------------

func TestBackfillIndexWithPrefix(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	// Pre-populate storage with keyed data under a prefix.
	if err := stg.Set([]byte("users/1"), []byte(`{"name":"alice"}`)); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := stg.Set([]byte("users/2"), []byte(`{"name":"bob"}`)); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := stg.Set([]byte("users/3"), []byte(`{"name":"carol"}`)); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// Entry under a different prefix -- should not be indexed.
	if err := stg.Set([]byte("products/1"), []byte(`{"name":"widget"}`)); err != nil {
		t.Fatalf("Set: %v", err)
	}

	log := indexTestLogger(t)
	idx, err := index.NewStoragePebbleIndex(stg, index.IndexConfig{
		Name:          "users.Name",
		Type:          index.IndexTypeNormal,
		CaseSensitive: true,
	}, log)
	if err != nil {
		t.Fatalf("NewStoragePebbleIndex: %v", err)
	}

	// Backfill from the "users/" prefix.
	if bErr := BackfillIndexWithPrefix(stg, idx, "name", []byte("users/")); bErr != nil {
		t.Fatalf("BackfillIndexWithPrefix: %v", bErr)
	}

	// Verify all three user entries are indexed.
	for _, name := range []string{"alice", "bob", "carol"} {
		ids, gErr := idx.Get([]byte(name))
		if gErr != nil {
			t.Fatalf("Get(%s): %v", name, gErr)
		}
		if len(ids) != 1 {
			t.Errorf("expected 1 ID for %s, got %d", name, len(ids))
		}
	}

	// Verify product is NOT indexed.
	ids, err := idx.Get([]byte("widget"))
	if err != nil {
		t.Fatalf("Get(widget): %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected 0 IDs for widget (different prefix), got %d", len(ids))
	}
}

func TestBackfillIndexWithPrefix_NonJSON(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	// Pre-populate with non-JSON data.
	if err := stg.Set([]byte("users/1"), []byte("not-json")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := stg.Set([]byte("users/2"), []byte("")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	log := indexTestLogger(t)
	idx, err := index.NewStoragePebbleIndex(stg, index.IndexConfig{
		Name:          "users.Name",
		Type:          index.IndexTypeNormal,
		CaseSensitive: true,
	}, log)
	if err != nil {
		t.Fatalf("NewStoragePebbleIndex: %v", err)
	}

	if bErr := BackfillIndexWithPrefix(stg, idx, "name", []byte("users/")); bErr != nil {
		t.Fatalf("BackfillIndexWithPrefix: %v", bErr)
	}

	// Nothing should be indexed.
	all, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("expected 0 IDs for non-JSON data, got %d", len(all))
	}
}

// ---------------------------------------------------------------------------
// UpdateIndexesBatch: Empty indexes
// ---------------------------------------------------------------------------

func TestUpdateIndexesBatch_EmptyIndexes(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	batch := stg.Batch()
	defer func() { _ = batch.Close() }()

	// Nil indexes map.
	if err := UpdateIndexesBatch(batch, nil, nil, []byte("k"), []byte(`{"a":"b"}`), nil); err != nil {
		t.Fatalf("UpdateIndexesBatch(nil): %v", err)
	}

	// Empty indexes map.
	if err := UpdateIndexesBatch(batch, map[string]index.Index{}, map[string]string{}, []byte("k"), []byte(`{"a":"b"}`), nil); err != nil {
		t.Fatalf("UpdateIndexesBatch(empty): %v", err)
	}
}

// ---------------------------------------------------------------------------
// UpdateIndexesBatch: Non-batchable index (MemoryIndex)
// ---------------------------------------------------------------------------

func TestUpdateIndexesBatch_NonBatchableIndex(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	// MemoryIndex does NOT implement BatchableIndex.
	memIdx := index.NewMemoryIndex("by_name", false)
	indexes := map[string]index.Index{"by_name": memIdx}
	indexFields := map[string]string{"by_name": "name"}

	batch := stg.Batch()
	defer func() { _ = batch.Close() }()

	// Should skip the non-batchable index gracefully, no error.
	err := UpdateIndexesBatch(batch, indexes, indexFields,
		[]byte("user:1"), []byte(`{"name":"alice"}`), nil)
	if err != nil {
		t.Fatalf("UpdateIndexesBatch with MemoryIndex: %v", err)
	}
	if cErr := batch.Commit(); cErr != nil {
		t.Fatalf("Commit: %v", cErr)
	}

	// MemoryIndex should NOT have been updated (batch path skips it).
	ids, err := memIdx.Get([]byte("alice"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected 0 IDs (non-batchable skipped), got %d", len(ids))
	}
}

// ---------------------------------------------------------------------------
// RemoveIndexesBatch: nil prevKv
// ---------------------------------------------------------------------------

func TestRemoveIndexesBatch_NilPrevKv(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := indexTestLogger(t)
	idx, err := index.NewStoragePebbleIndex(stg, index.IndexConfig{
		Name:          "by_name",
		Type:          index.IndexTypeNormal,
		CaseSensitive: true,
	}, log)
	if err != nil {
		t.Fatalf("NewStoragePebbleIndex: %v", err)
	}

	indexes := map[string]index.Index{"by_name": idx}
	indexFields := map[string]string{"by_name": "name"}

	batch := stg.Batch()
	defer func() { _ = batch.Close() }()

	// nil prevKv -- should return nil immediately.
	rErr := RemoveIndexesBatch(batch, indexes, indexFields, []byte("user:1"), nil)
	if rErr != nil {
		t.Fatalf("RemoveIndexesBatch(nil prevKv): %v", rErr)
	}
}

// ---------------------------------------------------------------------------
// RemoveIndexesBatch: Empty prev value
// ---------------------------------------------------------------------------

func TestRemoveIndexesBatch_EmptyPrevValue(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := indexTestLogger(t)
	idx, err := index.NewStoragePebbleIndex(stg, index.IndexConfig{
		Name:          "by_name",
		Type:          index.IndexTypeNormal,
		CaseSensitive: true,
	}, log)
	if err != nil {
		t.Fatalf("NewStoragePebbleIndex: %v", err)
	}

	indexes := map[string]index.Index{"by_name": idx}
	indexFields := map[string]string{"by_name": "name"}

	batch := stg.Batch()
	defer func() { _ = batch.Close() }()

	// prevKv with empty Value -- should return nil immediately.
	prevKv := &qkv.KeyValue{Key: []byte("user:1"), Value: []byte{}}
	rErr := RemoveIndexesBatch(batch, indexes, indexFields, []byte("user:1"), prevKv)
	if rErr != nil {
		t.Fatalf("RemoveIndexesBatch(empty value): %v", rErr)
	}
}

// ---------------------------------------------------------------------------
// RemoveIndexesBatch: Non-JSON prev value
// ---------------------------------------------------------------------------

func TestRemoveIndexesBatch_NonJSONPrevValue(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := indexTestLogger(t)
	idx, err := index.NewStoragePebbleIndex(stg, index.IndexConfig{
		Name:          "by_name",
		Type:          index.IndexTypeNormal,
		CaseSensitive: true,
	}, log)
	if err != nil {
		t.Fatalf("NewStoragePebbleIndex: %v", err)
	}

	indexes := map[string]index.Index{"by_name": idx}
	indexFields := map[string]string{"by_name": "name"}

	batch := stg.Batch()
	defer func() { _ = batch.Close() }()

	// prevKv with non-JSON Value -- should return nil (JSON parse fails gracefully).
	prevKv := &qkv.KeyValue{Key: []byte("user:1"), Value: []byte("not-json-at-all")}
	rErr := RemoveIndexesBatch(batch, indexes, indexFields, []byte("user:1"), prevKv)
	if rErr != nil {
		t.Fatalf("RemoveIndexesBatch(non-JSON): %v", rErr)
	}
}

// ---------------------------------------------------------------------------
// RemoveIndexesBatch: Non-batchable index (MemoryIndex)
// ---------------------------------------------------------------------------

func TestRemoveIndexesBatch_NonBatchableIndex(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	// MemoryIndex does NOT implement BatchableIndex.
	memIdx := index.NewMemoryIndex("by_name", false)
	// Pre-populate the index so there's something to "remove".
	_ = memIdx.Put([]byte("user:1"), []byte("alice"))

	indexes := map[string]index.Index{"by_name": memIdx}
	indexFields := map[string]string{"by_name": "name"}

	batch := stg.Batch()
	defer func() { _ = batch.Close() }()

	prevKv := &qkv.KeyValue{Key: []byte("user:1"), Value: []byte(`{"name":"alice"}`)}
	rErr := RemoveIndexesBatch(batch, indexes, indexFields, []byte("user:1"), prevKv)
	if rErr != nil {
		t.Fatalf("RemoveIndexesBatch with MemoryIndex: %v", rErr)
	}

	// MemoryIndex entry should NOT have been removed (batch path skips non-batchable).
	ids, err := memIdx.Get([]byte("alice"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(ids) != 1 {
		t.Errorf("expected 1 ID (non-batchable skipped), got %d", len(ids))
	}
}

// ---------------------------------------------------------------------------
// BackfillIndexWithPrefix: Empty storage
// ---------------------------------------------------------------------------

func TestBackfillIndexWithPrefix_EmptyStorage(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := indexTestLogger(t)
	idx, err := index.NewStoragePebbleIndex(stg, index.IndexConfig{
		Name:          "by_name",
		Type:          index.IndexTypeNormal,
		CaseSensitive: true,
	}, log)
	if err != nil {
		t.Fatalf("NewStoragePebbleIndex: %v", err)
	}

	// No data in storage -- should return nil with no entries indexed.
	if bErr := BackfillIndexWithPrefix(stg, idx, "name", []byte("users/")); bErr != nil {
		t.Fatalf("BackfillIndexWithPrefix: %v", bErr)
	}

	all, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("expected 0 IDs for empty storage, got %d", len(all))
	}
}

// ---------------------------------------------------------------------------
// BackfillIndexWithPrefix: Non-JSON values
// ---------------------------------------------------------------------------

func TestBackfillIndexWithPrefix_NonJSONValues(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	// Put non-JSON data.
	if err := stg.Set([]byte("users/1"), []byte("not-json")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := stg.Set([]byte("users/2"), []byte("also not json")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := stg.Set([]byte("users/3"), []byte("")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	log := indexTestLogger(t)
	idx, err := index.NewStoragePebbleIndex(stg, index.IndexConfig{
		Name:          "users.Name",
		Type:          index.IndexTypeNormal,
		CaseSensitive: true,
	}, log)
	if err != nil {
		t.Fatalf("NewStoragePebbleIndex: %v", err)
	}

	if bErr := BackfillIndexWithPrefix(stg, idx, "name", []byte("users/")); bErr != nil {
		t.Fatalf("BackfillIndexWithPrefix: %v", bErr)
	}

	// Non-JSON values should be skipped.
	all, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("expected 0 IDs for non-JSON data, got %d", len(all))
	}
}

// ---------------------------------------------------------------------------
// EntityIndex: Field definition
// ---------------------------------------------------------------------------

func TestEntityIndex_FieldDefinition(t *testing.T) {
	tests := []struct {
		name string
		idx  EntityIndex
	}{
		{
			name: "basic non-unique",
			idx: EntityIndex{
				FieldName:     "Name",
				JSONFieldName: "name",
				Unique:        false,
				Sparse:        false,
				CaseSensitive: true,
			},
		},
		{
			name: "unique index",
			idx: EntityIndex{
				FieldName:     "Email",
				JSONFieldName: "email",
				Unique:        true,
				Sparse:        false,
				CaseSensitive: true,
			},
		},
		{
			name: "sparse case-insensitive",
			idx: EntityIndex{
				FieldName:     "Tag",
				JSONFieldName: "tag",
				Unique:        false,
				Sparse:        true,
				CaseSensitive: false,
			},
		},
		{
			name: "all flags set",
			idx: EntityIndex{
				FieldName:     "Code",
				JSONFieldName: "code",
				Unique:        true,
				Sparse:        true,
				CaseSensitive: true,
			},
		},
		{
			name: "zero value",
			idx:  EntityIndex{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Verify all fields are accessible and hold the expected values.
			idx := tt.idx
			if idx.FieldName != tt.idx.FieldName {
				t.Errorf("FieldName: got %q, want %q", idx.FieldName, tt.idx.FieldName)
			}
			if idx.JSONFieldName != tt.idx.JSONFieldName {
				t.Errorf("JSONFieldName: got %q, want %q", idx.JSONFieldName, tt.idx.JSONFieldName)
			}
			if idx.Unique != tt.idx.Unique {
				t.Errorf("Unique: got %v, want %v", idx.Unique, tt.idx.Unique)
			}
			if idx.Sparse != tt.idx.Sparse {
				t.Errorf("Sparse: got %v, want %v", idx.Sparse, tt.idx.Sparse)
			}
			if idx.CaseSensitive != tt.idx.CaseSensitive {
				t.Errorf("CaseSensitive: got %v, want %v", idx.CaseSensitive, tt.idx.CaseSensitive)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// UpdateCompoundIndexesBatch
// ---------------------------------------------------------------------------

func TestUpdateCompoundIndexesBatch_EmptyMap_NoOp(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	batch := stg.Batch()
	defer func() { _ = batch.Close() }()

	err := UpdateCompoundIndexesBatch(batch, nil, nil, []byte("k"), []byte(`{"a":"1"}`), nil)
	if err != nil {
		t.Fatalf("expected nil error for empty compound indexes, got %v", err)
	}
}

func TestUpdateCompoundIndexesBatch_NonJSONValue_Skipped(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := indexTestLogger(t)
	idx, err := index.NewCompoundIndex(stg, index.IndexConfig{
		Name:       "entities.A_B",
		FieldNames: []string{"A", "B"},
	}, log)
	if err != nil {
		t.Fatalf("NewCompoundIndex: %v", err)
	}

	batch := stg.Batch()
	defer func() { _ = batch.Close() }()

	compoundIndexes := map[string]*index.CompoundIndex{"entities.A_B": idx}
	compoundFields := map[string][]string{"entities.A_B": {"a", "b"}}

	// Non-JSON value: should be silently skipped, not error.
	err = UpdateCompoundIndexesBatch(batch, compoundIndexes, compoundFields, []byte("entities/1"), []byte("not-json"), nil)
	if err != nil {
		t.Fatalf("expected nil error for non-JSON value, got %v", err)
	}
}

func TestUpdateCompoundIndexesBatch_HappyPath(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := indexTestLogger(t)
	idx, err := index.NewCompoundIndex(stg, index.IndexConfig{
		Name:       "docs.Author_Year",
		FieldNames: []string{"Author", "Year"},
	}, log)
	if err != nil {
		t.Fatalf("NewCompoundIndex: %v", err)
	}

	batch := stg.Batch()
	compoundIndexes := map[string]*index.CompoundIndex{"docs.Author_Year": idx}
	compoundFields := map[string][]string{"docs.Author_Year": {"author", "year"}}
	key := []byte("docs/1")
	value := []byte(`{"author":"alice","year":"2024"}`)

	if err := UpdateCompoundIndexesBatch(batch, compoundIndexes, compoundFields, key, value, nil); err != nil {
		t.Fatalf("UpdateCompoundIndexesBatch: %v", err)
	}
	if err := batch.Commit(); err != nil {
		t.Fatalf("batch.Commit: %v", err)
	}

	results, err := idx.GetCompound([][]byte{[]byte("alice"), []byte("2024")})
	if err != nil {
		t.Fatalf("GetCompound: %v", err)
	}
	if len(results) != 1 || string(results[0]) != "docs/1" {
		t.Errorf("expected [docs/1], got %v", results)
	}
}

func TestUpdateCompoundIndexesBatch_UpdateRemovesOldEntry(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := indexTestLogger(t)
	idx, err := index.NewCompoundIndex(stg, index.IndexConfig{
		Name:       "items.Cat_Tag",
		FieldNames: []string{"Cat", "Tag"},
	}, log)
	if err != nil {
		t.Fatalf("NewCompoundIndex: %v", err)
	}

	compoundIndexes := map[string]*index.CompoundIndex{"items.Cat_Tag": idx}
	compoundFields := map[string][]string{"items.Cat_Tag": {"cat", "tag"}}
	key := []byte("items/1")
	oldValue := []byte(`{"cat":"electronics","tag":"phone"}`)
	newValue := []byte(`{"cat":"electronics","tag":"laptop"}`)

	// Insert old entry.
	b1 := stg.Batch()
	if err := UpdateCompoundIndexesBatch(b1, compoundIndexes, compoundFields, key, oldValue, nil); err != nil {
		t.Fatalf("first UpdateCompoundIndexesBatch: %v", err)
	}
	_ = b1.Commit()

	// Update — provide prevKv so old entry is removed.
	prevKv := &qkv.KeyValue{Key: key, Value: oldValue}
	b2 := stg.Batch()
	if err := UpdateCompoundIndexesBatch(b2, compoundIndexes, compoundFields, key, newValue, prevKv); err != nil {
		t.Fatalf("second UpdateCompoundIndexesBatch: %v", err)
	}
	_ = b2.Commit()

	// Old entry should be gone.
	old, err := idx.GetCompound([][]byte{[]byte("electronics"), []byte("phone")})
	if err != nil {
		t.Fatalf("GetCompound (old): %v", err)
	}
	if len(old) != 0 {
		t.Errorf("expected old entry removed, but got %v", old)
	}

	// New entry should be present.
	newRes, err := idx.GetCompound([][]byte{[]byte("electronics"), []byte("laptop")})
	if err != nil {
		t.Fatalf("GetCompound (new): %v", err)
	}
	if len(newRes) != 1 || string(newRes[0]) != "items/1" {
		t.Errorf("expected [items/1], got %v", newRes)
	}
}

// ---------------------------------------------------------------------------
// RemoveCompoundIndexesBatch
// ---------------------------------------------------------------------------

func TestRemoveCompoundIndexesBatch_EmptyMap_NoOp(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	batch := stg.Batch()
	defer func() { _ = batch.Close() }()

	err := RemoveCompoundIndexesBatch(batch, nil, nil, []byte("k"), nil)
	if err != nil {
		t.Fatalf("expected nil for empty map, got %v", err)
	}
}

func TestRemoveCompoundIndexesBatch_NilPrevKv_NoOp(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := indexTestLogger(t)
	idx, err := index.NewCompoundIndex(stg, index.IndexConfig{
		Name:       "things.X_Y",
		FieldNames: []string{"X", "Y"},
	}, log)
	if err != nil {
		t.Fatalf("NewCompoundIndex: %v", err)
	}

	batch := stg.Batch()
	defer func() { _ = batch.Close() }()

	compoundIndexes := map[string]*index.CompoundIndex{"things.X_Y": idx}
	compoundFields := map[string][]string{"things.X_Y": {"x", "y"}}

	// prevKv is nil — should be a no-op.
	err = RemoveCompoundIndexesBatch(batch, compoundIndexes, compoundFields, []byte("things/1"), nil)
	if err != nil {
		t.Fatalf("expected nil for nil prevKv, got %v", err)
	}
}

func TestRemoveCompoundIndexesBatch_HappyPath(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := indexTestLogger(t)
	idx, err := index.NewCompoundIndex(stg, index.IndexConfig{
		Name:       "nodes.Zone_Role",
		FieldNames: []string{"Zone", "Role"},
	}, log)
	if err != nil {
		t.Fatalf("NewCompoundIndex: %v", err)
	}

	key := []byte("nodes/1")
	value := []byte(`{"zone":"us-east-1","role":"leader"}`)
	compoundIndexes := map[string]*index.CompoundIndex{"nodes.Zone_Role": idx}
	compoundFields := map[string][]string{"nodes.Zone_Role": {"zone", "role"}}

	// Insert entry.
	b1 := stg.Batch()
	if err := UpdateCompoundIndexesBatch(b1, compoundIndexes, compoundFields, key, value, nil); err != nil {
		t.Fatalf("UpdateCompoundIndexesBatch: %v", err)
	}
	_ = b1.Commit()

	// Remove entry.
	prevKv := &qkv.KeyValue{Key: key, Value: value}
	b2 := stg.Batch()
	if err := RemoveCompoundIndexesBatch(b2, compoundIndexes, compoundFields, key, prevKv); err != nil {
		t.Fatalf("RemoveCompoundIndexesBatch: %v", err)
	}
	_ = b2.Commit()

	results, err := idx.GetCompound([][]byte{[]byte("us-east-1"), []byte("leader")})
	if err != nil {
		t.Fatalf("GetCompound: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected no results after removal, got %v", results)
	}
}

// ---------------------------------------------------------------------------
// BackfillCompoundIndexWithPrefix
// ---------------------------------------------------------------------------

func TestBackfillCompoundIndexWithPrefix_PopulatesIndex(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	// Write data directly to storage (bypassing index).
	for i, body := range []string{
		`{"author":"alice","year":"2023"}`,
		`{"author":"bob","year":"2024"}`,
	} {
		if err := stg.Set([]byte("books/"+string(rune('1'+i))), []byte(body)); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	log := indexTestLogger(t)
	idx, err := index.NewCompoundIndex(stg, index.IndexConfig{
		Name:       "books.Author_Year",
		FieldNames: []string{"Author", "Year"},
	}, log)
	if err != nil {
		t.Fatalf("NewCompoundIndex: %v", err)
	}

	if err := BackfillCompoundIndexWithPrefix(stg, idx, []string{"author", "year"}, []byte("books/")); err != nil {
		t.Fatalf("BackfillCompoundIndexWithPrefix: %v", err)
	}

	res, err := idx.GetCompound([][]byte{[]byte("alice"), []byte("2023")})
	if err != nil {
		t.Fatalf("GetCompound alice/2023: %v", err)
	}
	if len(res) != 1 {
		t.Errorf("expected 1 key for alice/2023, got %d", len(res))
	}
}

func TestBackfillCompoundIndexWithPrefix_EmptyValueSkipped(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	// One entry with empty value; should be silently skipped.
	if err := stg.Set([]byte("parts/1"), []byte{}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	log := indexTestLogger(t)
	idx, err := index.NewCompoundIndex(stg, index.IndexConfig{
		Name:       "parts.X_Y",
		FieldNames: []string{"X", "Y"},
	}, log)
	if err != nil {
		t.Fatalf("NewCompoundIndex: %v", err)
	}

	if err := BackfillCompoundIndexWithPrefix(stg, idx, []string{"x", "y"}, []byte("parts/")); err != nil {
		t.Fatalf("BackfillCompoundIndexWithPrefix with empty value: %v", err)
	}
}

func TestBackfillCompoundIndexWithPrefix_NonJSONSkipped(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	// One non-JSON entry; should not fail, and index should remain empty.
	if err := stg.Set([]byte("items/1"), []byte("not-json")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	log := indexTestLogger(t)
	idx, err := index.NewCompoundIndex(stg, index.IndexConfig{
		Name:       "items.A_B",
		FieldNames: []string{"A", "B"},
	}, log)
	if err != nil {
		t.Fatalf("NewCompoundIndex: %v", err)
	}

	if err := BackfillCompoundIndexWithPrefix(stg, idx, []string{"a", "b"}, []byte("items/")); err != nil {
		t.Fatalf("BackfillCompoundIndexWithPrefix: %v", err)
	}

	res, err := idx.GetCompound([][]byte{[]byte("x"), []byte("y")})
	if err != nil {
		t.Fatalf("GetCompound: %v", err)
	}
	if len(res) != 0 {
		t.Errorf("expected empty index for non-JSON data, got %v", res)
	}
}

// ---------------------------------------------------------------------------
// extractCompoundFieldValues
// ---------------------------------------------------------------------------

func TestExtractCompoundFieldValues_AllPresent(t *testing.T) {
	t.Parallel()
	data := map[string]json.RawMessage{
		"name": json.RawMessage(`"alice"`),
		"age":  json.RawMessage(`30`),
	}
	result := extractCompoundFieldValues(data, []string{"name", "age"})
	if result == nil {
		t.Fatal("expected non-nil result for all-present fields")
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 values, got %d", len(result))
	}
	if string(result[0]) != "alice" {
		t.Errorf("result[0] = %q, want %q", result[0], "alice")
	}
	if string(result[1]) != "30" {
		t.Errorf("result[1] = %q, want %q", result[1], "30")
	}
}

func TestExtractCompoundFieldValues_MissingField_ReturnsNil(t *testing.T) {
	t.Parallel()
	data := map[string]json.RawMessage{
		"name": json.RawMessage(`"alice"`),
	}
	// "age" is not in the map — sparse behavior: return nil.
	result := extractCompoundFieldValues(data, []string{"name", "age"})
	if result != nil {
		t.Errorf("expected nil for missing field, got %v", result)
	}
}

func TestExtractCompoundFieldValues_EmptyFieldNames_EmptySlice(t *testing.T) {
	t.Parallel()
	data := map[string]json.RawMessage{
		"x": json.RawMessage(`"y"`),
	}
	result := extractCompoundFieldValues(data, []string{})
	// Zero-length fieldNames → zero-length values slice, not nil.
	if result == nil {
		t.Fatal("expected non-nil empty slice for zero field names")
	}
	if len(result) != 0 {
		t.Errorf("expected empty slice, got %v", result)
	}
}

// TestRemoveCompoundIndexesBatch_NonJSONPrevKv verifies that a non-JSON
// prevKv.Value is silently skipped (unmarshal fails → return nil, not error).
func TestRemoveCompoundIndexesBatch_NonJSONPrevKv(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := indexTestLogger(t)
	idx, err := index.NewCompoundIndex(stg, index.IndexConfig{
		Name:       "x.A_B",
		FieldNames: []string{"A", "B"},
	}, log)
	if err != nil {
		t.Fatalf("NewCompoundIndex: %v", err)
	}

	batch := stg.Batch()
	defer func() { _ = batch.Close() }()

	compoundIndexes := map[string]*index.CompoundIndex{"x.A_B": idx}
	compoundFields := map[string][]string{"x.A_B": {"a", "b"}}
	key := []byte("x/1")
	prevKv := &qkv.KeyValue{Key: key, Value: []byte("not-json")}

	// Non-JSON prevKv.Value: JSON unmarshal fails → function returns nil silently.
	err = RemoveCompoundIndexesBatch(batch, compoundIndexes, compoundFields, key, prevKv)
	if err != nil {
		t.Fatalf("expected nil for non-JSON prevKv.Value, got %v", err)
	}
}

// TestRemoveCompoundIndexesBatch_MissingFieldsKey verifies that when the
// compound index exists but no field names are registered for it, the index
// loop skips the entry without error.
func TestRemoveCompoundIndexesBatch_MissingFieldsKey(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := indexTestLogger(t)
	idx, err := index.NewCompoundIndex(stg, index.IndexConfig{
		Name:       "y.C_D",
		FieldNames: []string{"C", "D"},
	}, log)
	if err != nil {
		t.Fatalf("NewCompoundIndex: %v", err)
	}

	batch := stg.Batch()
	defer func() { _ = batch.Close() }()

	compoundIndexes := map[string]*index.CompoundIndex{"y.C_D": idx}
	// Intentionally empty compoundFields: lookup for "y.C_D" will return !ok.
	compoundFields := map[string][]string{}
	key := []byte("y/1")
	prevKv := &qkv.KeyValue{Key: key, Value: []byte(`{"c":"v1","d":"v2"}`)}

	err = RemoveCompoundIndexesBatch(batch, compoundIndexes, compoundFields, key, prevKv)
	if err != nil {
		t.Fatalf("expected nil when field names missing, got %v", err)
	}
}

// TestUpdateCompoundIndexesBatch_MissingFieldsKey verifies that when the
// compound index exists but no field names are registered for it, the index
// loop skips the entry without error.
func TestUpdateCompoundIndexesBatch_MissingFieldsKey(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := indexTestLogger(t)
	idx, err := index.NewCompoundIndex(stg, index.IndexConfig{
		Name:       "z.E_F",
		FieldNames: []string{"E", "F"},
	}, log)
	if err != nil {
		t.Fatalf("NewCompoundIndex: %v", err)
	}

	batch := stg.Batch()
	defer func() { _ = batch.Close() }()

	compoundIndexes := map[string]*index.CompoundIndex{"z.E_F": idx}
	// Empty compoundFields: lookup for "z.E_F" will return !ok.
	compoundFields := map[string][]string{}
	key := []byte("z/1")
	value := []byte(`{"e":"v1","f":"v2"}`)

	err = UpdateCompoundIndexesBatch(batch, compoundIndexes, compoundFields, key, value, nil)
	if err != nil {
		t.Fatalf("expected nil when field names missing, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test helper
// ---------------------------------------------------------------------------

func indexTestLogger(t *testing.T) *logger.Logger {
	t.Helper()
	log, err := logger.New(logger.LogConfig{
		Level:  "error",
		Format: "json",
		Output: "stderr",
	})
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}
	return log
}
