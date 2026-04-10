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
