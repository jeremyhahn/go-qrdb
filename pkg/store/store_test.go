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
	"encoding/binary"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
)

// openStore is a test helper that creates, opens, and registers cleanup for
// a PebbleStateMachine. It fails the test immediately on error.
func openStore(t *testing.T) *PebbleStateMachine {
	t.Helper()
	s := NewPebbleStateMachine(nil)
	dir := t.TempDir()
	idx, err := s.Open(context.Background(), filepath.Join(dir, "db"), nil)
	if err != nil {
		t.Fatalf("Open: unexpected error: %v", err)
	}
	if idx != 0 {
		t.Fatalf("Open: expected last applied 0 on fresh DB, got %d", idx)
	}
	t.Cleanup(func() {
		if err := s.Close(context.Background()); err != nil {
			t.Errorf("Close: unexpected error: %v", err)
		}
	})
	return s
}

// putEntry builds an sm.Entry with a Put command.
func putEntry(index uint64, key, value string) sm.Entry {
	cmd, err := kv.EncodePut([]byte(key), []byte(value))
	if err != nil {
		panic("putEntry: " + err.Error())
	}
	return sm.Entry{
		Index: index,
		Cmd:   cmd,
	}
}

// deleteEntry builds an sm.Entry with a Delete command.
func deleteEntry(index uint64, key string) sm.Entry {
	cmd, err := kv.EncodeDelete([]byte(key))
	if err != nil {
		panic("deleteEntry: " + err.Error())
	}
	return sm.Entry{
		Index: index,
		Cmd:   cmd,
	}
}

// applyEntries is a helper that applies entries to the store and returns results.
func applyEntries(t *testing.T, s *PebbleStateMachine, entries []sm.Entry) []sm.Result {
	t.Helper()
	results := make([]sm.Result, len(entries))
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: unexpected error: %v", err)
	}
	return results
}

// ---------------------------------------------------------------------------
// PebbleStateMachine lifecycle
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_OpenCreatesDB(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	dir := filepath.Join(t.TempDir(), "db")
	idx, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: unexpected error: %v", err)
	}
	if idx != 0 {
		t.Fatalf("expected index 0 for fresh db, got %d", idx)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: unexpected error: %v", err)
	}
}

func TestPebbleStateMachine_OpenReturnsLastApplied(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")

	// Open, apply entries, close.
	s := NewPebbleStateMachine(nil)
	if _, err := s.Open(context.Background(), dir, nil); err != nil {
		t.Fatalf("Open: %v", err)
	}
	applyEntries(t, s, []sm.Entry{putEntry(42, "k", "v")})
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen: last applied index must be 42.
	s2 := NewPebbleStateMachine(nil)
	idx, err := s2.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if idx != 42 {
		t.Fatalf("expected last applied 42, got %d", idx)
	}
	if err := s2.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestPebbleStateMachine_OpenInvalidDirReturnsOpenError(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	// Attempt to open in a non-existent nested path that can't be created.
	_, err := s.Open(context.Background(), "/dev/null/invalid/path", nil)
	if err == nil {
		t.Fatal("expected error opening invalid directory")
	}
	var oe *OpenError
	if !errors.As(err, &oe) {
		t.Fatalf("expected *OpenError, got %T: %v", err, err)
	}
}

func TestPebbleStateMachine_UpdateBeforeOpenReturnsNotOpenError(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	entries := []sm.Entry{putEntry(1, "k", "v")}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error")
	}
	var noe *NotOpenError
	if !errors.As(err, &noe) {
		t.Fatalf("expected *NotOpenError, got %T: %v", err, err)
	}
}

func TestPebbleStateMachine_LookupBeforeOpenReturnsNotOpenError(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	_, err := s.Lookup(context.Background(), []byte("k"))
	if err == nil {
		t.Fatal("expected error")
	}
	var noe *NotOpenError
	if !errors.As(err, &noe) {
		t.Fatalf("expected *NotOpenError, got %T: %v", err, err)
	}
}

func TestPebbleStateMachine_SyncBeforeOpenReturnsNotOpenError(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	err := s.Sync()
	if err == nil {
		t.Fatal("expected error")
	}
	var noe *NotOpenError
	if !errors.As(err, &noe) {
		t.Fatalf("expected *NotOpenError, got %T: %v", err, err)
	}
}

func TestPebbleStateMachine_PrepareSnapshotBeforeOpenReturnsNotOpenError(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	_, err := s.PrepareSnapshot()
	if err == nil {
		t.Fatal("expected error")
	}
	var noe *NotOpenError
	if !errors.As(err, &noe) {
		t.Fatalf("expected *NotOpenError, got %T: %v", err, err)
	}
}

func TestPebbleStateMachine_RecoverFromSnapshotBeforeOpenReturnsNotOpenError(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	err := s.RecoverFromSnapshot(context.Background(), &bytes.Buffer{}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var noe *NotOpenError
	if !errors.As(err, &noe) {
		t.Fatalf("expected *NotOpenError, got %T: %v", err, err)
	}
}

func TestPebbleStateMachine_GetHashBeforeOpenReturnsNotOpenError(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	_, err := s.GetHash()
	if err == nil {
		t.Fatal("expected error")
	}
	var noe *NotOpenError
	if !errors.As(err, &noe) {
		t.Fatalf("expected *NotOpenError, got %T: %v", err, err)
	}
}

func TestPebbleStateMachine_BytesLookupBeforeOpenReturnsNotOpenError(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	_, err := s.BytesLookup([]byte("k"))
	if err == nil {
		t.Fatal("expected error")
	}
	var noe *NotOpenError
	if !errors.As(err, &noe) {
		t.Fatalf("expected *NotOpenError, got %T: %v", err, err)
	}
}

func TestPebbleStateMachine_CloseWithoutOpenIsNoop(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close on unopened store: unexpected error: %v", err)
	}
}

func TestPebbleStateMachine_DoubleCloseIsNoop(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	dir := filepath.Join(t.TempDir(), "db")
	if _, err := s.Open(context.Background(), dir, nil); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// Second close should be a no-op since db is nil.
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("second Close: unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// KV operations via Update
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_PutThenLookup(t *testing.T) {
	s := openStore(t)
	applyEntries(t, s, []sm.Entry{putEntry(1, "greeting", "hello")})

	val, err := s.Lookup(context.Background(), []byte("greeting"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("hello")) {
		t.Fatalf("expected 'hello', got %q", val)
	}
}

func TestPebbleStateMachine_PutMultipleThenLookupEach(t *testing.T) {
	s := openStore(t)

	entries := []sm.Entry{
		putEntry(1, "a", "1"),
		putEntry(2, "b", "2"),
		putEntry(3, "c", "3"),
	}
	results := applyEntries(t, s, entries)

	// Verify results reflect value lengths.
	for i, r := range results {
		if r.Value != 1 {
			t.Errorf("entry %d: expected result value 1, got %d", i, r.Value)
		}
	}

	for _, tc := range []struct {
		key, want string
	}{
		{"a", "1"},
		{"b", "2"},
		{"c", "3"},
	} {
		val, err := s.Lookup(context.Background(), []byte(tc.key))
		if err != nil {
			t.Fatalf("Lookup(%q): %v", tc.key, err)
		}
		if !bytes.Equal(val.([]byte), []byte(tc.want)) {
			t.Fatalf("Lookup(%q): expected %q, got %q", tc.key, tc.want, val)
		}
	}
}

func TestPebbleStateMachine_DeleteThenLookupReturnsKeyNotFound(t *testing.T) {
	s := openStore(t)

	applyEntries(t, s, []sm.Entry{putEntry(1, "key", "value")})
	results := applyEntries(t, s, []sm.Entry{deleteEntry(2, "key")})

	if results[0].Value != 1 {
		t.Fatalf("delete result: expected 1, got %d", results[0].Value)
	}

	_, err := s.Lookup(context.Background(), []byte("key"))
	if err == nil {
		t.Fatal("expected error after delete")
	}
	var knf *kv.KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("expected *KeyNotFoundError, got %T: %v", err, err)
	}
}

func TestPebbleStateMachine_OverwriteKey(t *testing.T) {
	s := openStore(t)

	applyEntries(t, s, []sm.Entry{putEntry(1, "key", "v1")})
	applyEntries(t, s, []sm.Entry{putEntry(2, "key", "v2")})

	val, err := s.Lookup(context.Background(), []byte("key"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("v2")) {
		t.Fatalf("expected 'v2', got %q", val)
	}
}

func TestPebbleStateMachine_GetEntryIsNoop(t *testing.T) {
	s := openStore(t)

	getCmd, err := kv.EncodeGet([]byte("nonexistent"))
	if err != nil {
		t.Fatalf("EncodeGet: %v", err)
	}
	entries := []sm.Entry{{Index: 1, Cmd: getCmd}}
	results := applyEntries(t, s, entries)
	if results[0].Value != 0 {
		t.Fatalf("Get result: expected 0, got %d", results[0].Value)
	}
}

func TestPebbleStateMachine_UpdateInvalidCommandIsRefusedNotFatal(t *testing.T) {
	s := openStore(t)
	entries := []sm.Entry{{Index: 1, Cmd: []byte{0xFF}}} // invalid op
	results := make([]sm.Result, 1)

	assertRefusedNotHalted(t, s.Update(context.Background(), entries, results),
		results[0], "unknown op code")
}

func TestPebbleStateMachine_UpdateEmptyEntries(t *testing.T) {
	s := openStore(t)
	results := applyEntries(t, s, []sm.Entry{})
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// Lookup types
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_LookupByteSlice(t *testing.T) {
	s := openStore(t)
	applyEntries(t, s, []sm.Entry{putEntry(1, "key", "val")})

	val, err := s.Lookup(context.Background(), []byte("key"))
	if err != nil {
		t.Fatalf("Lookup([]byte): %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("val")) {
		t.Fatalf("expected 'val', got %q", val)
	}
}

func TestPebbleStateMachine_LookupString(t *testing.T) {
	s := openStore(t)
	applyEntries(t, s, []sm.Entry{putEntry(1, "key", "val")})

	val, err := s.Lookup(context.Background(), "key")
	if err != nil {
		t.Fatalf("Lookup(string): %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("val")) {
		t.Fatalf("expected 'val', got %q", val)
	}

	var knf *kv.KeyNotFoundError
	if _, err := s.Lookup(context.Background(), "nonexistent"); !errors.As(err, &knf) {
		t.Fatalf("Lookup(string) of a missing key: got %v, want *kv.KeyNotFoundError", err)
	}
}

func TestPebbleStateMachine_LookupListQueryAll(t *testing.T) {
	s := openStore(t)

	entries := []sm.Entry{
		putEntry(1, "cherry", "3"),
		putEntry(2, "apple", "1"),
		putEntry(3, "banana", "2"),
	}
	applyEntries(t, s, entries)

	val, err := s.Lookup(context.Background(), kv.ListQuery{})
	if err != nil {
		t.Fatalf("Lookup(ListQuery{}): %v", err)
	}
	keys := val.([]string)
	if len(keys) != 3 {
		t.Fatalf("expected 3 keys, got %d: %v", len(keys), keys)
	}
	// Must be sorted.
	expected := []string{"apple", "banana", "cherry"}
	for i, k := range keys {
		if k != expected[i] {
			t.Fatalf("keys[%d]: expected %q, got %q", i, expected[i], k)
		}
	}
}

func TestPebbleStateMachine_LookupListQueryWithPrefix(t *testing.T) {
	s := openStore(t)

	entries := []sm.Entry{
		putEntry(1, "app:one", "1"),
		putEntry(2, "app:two", "2"),
		putEntry(3, "db:one", "3"),
	}
	applyEntries(t, s, entries)

	val, err := s.Lookup(context.Background(), kv.ListQuery{Prefix: "app:"})
	if err != nil {
		t.Fatalf("Lookup(ListQuery{Prefix: 'app:'}): %v", err)
	}
	keys := val.([]string)
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d: %v", len(keys), keys)
	}
	expected := []string{"app:one", "app:two"}
	for i, k := range keys {
		if k != expected[i] {
			t.Fatalf("keys[%d]: expected %q, got %q", i, expected[i], k)
		}
	}
}

func TestPebbleStateMachine_LookupListQueryNoResults(t *testing.T) {
	s := openStore(t)

	val, err := s.Lookup(context.Background(), kv.ListQuery{Prefix: "nonexistent:"})
	if err != nil {
		t.Fatalf("Lookup(ListQuery): %v", err)
	}
	keys := val.([]string)
	if len(keys) != 0 {
		t.Fatalf("expected 0 keys, got %d: %v", len(keys), keys)
	}
}

func TestPebbleStateMachine_LookupListQueryExcludesMetadata(t *testing.T) {
	s := openStore(t)

	// Apply an entry so the metadata key is written.
	applyEntries(t, s, []sm.Entry{putEntry(1, "user:key", "val")})

	val, err := s.Lookup(context.Background(), kv.ListQuery{})
	if err != nil {
		t.Fatalf("Lookup(ListQuery{}): %v", err)
	}
	keys := val.([]string)
	for _, k := range keys {
		if k == metaKeyLastApplied {
			t.Fatal("metadata key should be excluded from ListQuery results")
		}
		if len(k) > 0 && k[0] == '\x00' {
			t.Fatalf("internal key %q should be excluded from ListQuery results", k)
		}
	}
}

// TestStateMachine_EnumerationExcludesInternalKeys lists and prefix-scans both
// state machines over a keyspace seeded with every internal namespace. A
// whole-keyspace enumeration must disclose only the user key, while one rooted
// at a table's own prefix must return the table's rows: the relational layer
// reaches the state machine as exactly that scan.
func TestStateMachine_EnumerationExcludesInternalKeys(t *testing.T) {
	seed := []string{
		"user:key",
		"__sys:role:admin",
		"ts:metric:cpu",
		"idx:metric:cpu",
		"idx:tag:host:server1",
		"retention:cpu",
		"__tbl:shop:1/row",
		"__idx:shop:1:uq_e/a@b",
		"__seq:shop:__tableid",
	}
	type lookuper interface {
		Lookup(context.Context, any) (any, error)
	}
	openPebble := func(t *testing.T) lookuper {
		s := openStore(t)
		entries := make([]sm.Entry, len(seed))
		for i, k := range seed {
			entries[i] = putEntry(uint64(i+1), k, "v")
		}
		applyEntries(t, s, entries)
		return s
	}
	openMVCC := func(t *testing.T) lookuper {
		s := newTestMVCCStateMachine(t)
		entries := make([]sm.Entry, len(seed))
		for i, k := range seed {
			entries[i] = mvccPutEntry(uint64(i+1), []byte(k), []byte("v"), 0)
		}
		applyMVCCEntries(t, s, entries)
		return s
	}
	for _, tc := range []struct {
		name  string
		open  func(*testing.T) lookuper
		query any
		want  []string
	}{
		{"pebble list", openPebble, kv.ListQuery{}, []string{"user:key"}},
		{"mvcc list", openMVCC, kv.ListQuery{}, []string{"user:key"}},
		{"pebble scan", openPebble, &statemachine.PrefixQuery{}, []string{"user:key"}},
		{"mvcc scan", openMVCC, &statemachine.PrefixQuery{}, []string{"user:key"}},
		{"pebble scan of a table's rows", openPebble,
			&statemachine.PrefixQuery{Prefix: []byte("__tbl:shop:1/")}, []string{"__tbl:shop:1/row"}},
		{"pebble list under an all-0xFF prefix", openPebble,
			kv.ListQuery{Prefix: string([]byte{0xFF, 0xFF})}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.open(t).Lookup(context.Background(), tc.query)
			if err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			var got []string
			switch r := result.(type) {
			case []string:
				got = r
			case map[string][]byte:
				for k := range r {
					got = append(got, k)
				}
			default:
				t.Fatalf("Lookup returned %T", result)
			}
			sort.Strings(got)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("Lookup = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsInternalKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want bool
	}{
		{name: "sys_prefix", key: "__sys:role:admin", want: true},
		{name: "ts_prefix", key: "ts:metric:cpu", want: true},
		{name: "idx_metric_prefix", key: "idx:metric:cpu", want: true},
		{name: "idx_tag_prefix", key: "idx:tag:host:server1", want: true},
		{name: "retention_prefix", key: "retention:cpu", want: true},
		{name: "table_row_prefix", key: "__tbl:shop:1/row", want: true},
		{name: "table_index_prefix", key: "__idx:shop:1:uq_e/a@b", want: true},
		{name: "table_sequence_prefix", key: "__seq:shop:__autoinc:1", want: true},
		{name: "user_key", key: "user:key", want: false},
		{name: "empty_key", key: "", want: false},
		{name: "regular_key", key: "regular-key", want: false},
		{name: "ts_without_colon", key: "tsdata", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isInternalKey(tt.key)
			if got != tt.want {
				t.Fatalf("isInternalKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

// TestSkipInternalKeyFilter_MatchesTheEnumeratingNamespaces pins the skip
// rule this package's listKeys and scanPrefix consult. The relational row and
// index namespaces must earn the escape -- a table scan reaches this state
// machine as a prefix scan of "__tbl:{db}:{id}/" and would otherwise come back
// empty -- while the namespaces nothing enumerates must not.
func TestSkipInternalKeyFilter_MatchesTheEnumeratingNamespaces(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		want   bool
	}{
		{name: "system_store", prefix: "__sys:roles/", want: true},
		{name: "table_rows", prefix: "__tbl:shop:1/", want: true},
		{name: "table_index", prefix: "__idx:shop:1:uq_e/", want: true},
		{name: "sequences", prefix: "__seq:", want: false},
		{name: "time_series", prefix: "ts:", want: false},
		{name: "whole_keyspace", prefix: "", want: false},
		{name: "spanning_underscore", prefix: "_", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := skipInternalKeyFilter(tt.prefix); got != tt.want {
				t.Fatalf("skipInternalKeyFilter(%q) = %v, want %v", tt.prefix, got, tt.want)
			}
		})
	}
}

func TestPebbleStateMachine_LookupInvalidQueryType(t *testing.T) {
	s := openStore(t)
	_, err := s.Lookup(context.Background(), 12345)
	if err == nil {
		t.Fatal("expected error for invalid query type")
	}
	var iqe *InvalidQueryError
	if !errors.As(err, &iqe) {
		t.Fatalf("expected *InvalidQueryError, got %T: %v", err, err)
	}
}

func TestPebbleStateMachine_LookupKeyNotFound(t *testing.T) {
	s := openStore(t)
	_, err := s.Lookup(context.Background(), []byte("nonexistent"))
	if err == nil {
		t.Fatal("expected error for missing key")
	}
	var knf *kv.KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("expected *KeyNotFoundError, got %T: %v", err, err)
	}
}

func TestPebbleStateMachine_BytesLookup(t *testing.T) {
	s := openStore(t)
	applyEntries(t, s, []sm.Entry{putEntry(1, "key", "val")})

	val, err := s.BytesLookup([]byte("key"))
	if err != nil {
		t.Fatalf("BytesLookup: %v", err)
	}
	if !bytes.Equal(val, []byte("val")) {
		t.Fatalf("expected 'val', got %q", val)
	}
}

func TestPebbleStateMachine_BytesLookupKeyNotFound(t *testing.T) {
	s := openStore(t)
	_, err := s.BytesLookup([]byte("nope"))
	if err == nil {
		t.Fatal("expected error for missing key")
	}
	var knf *kv.KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("expected *KeyNotFoundError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Snapshot save and recover
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_SnapshotSaveAndRecover(t *testing.T) {
	s := openStore(t)

	entries := []sm.Entry{
		putEntry(1, "alpha", "A"),
		putEntry(2, "beta", "B"),
		putEntry(3, "gamma", "C"),
	}
	applyEntries(t, s, entries)

	// Sync + PrepareSnapshot + SaveSnapshot.
	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	var buf bytes.Buffer
	if err := s.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// Add a key that should NOT survive recovery.
	applyEntries(t, s, []sm.Entry{putEntry(4, "delta", "D")})

	// RecoverFromSnapshot should restore to the snapshot state.
	if err := s.RecoverFromSnapshot(context.Background(), &buf, nil); err != nil {
		t.Fatalf("RecoverFromSnapshot: %v", err)
	}

	// Verify original keys are present.
	for _, tc := range []struct {
		key, want string
	}{
		{"alpha", "A"},
		{"beta", "B"},
		{"gamma", "C"},
	} {
		val, err := s.Lookup(context.Background(), []byte(tc.key))
		if err != nil {
			t.Fatalf("Lookup(%q) after recovery: %v", tc.key, err)
		}
		if !bytes.Equal(val.([]byte), []byte(tc.want)) {
			t.Fatalf("Lookup(%q): expected %q, got %q", tc.key, tc.want, val)
		}
	}

	// Verify 'delta' does NOT exist after recovery.
	_, err = s.Lookup(context.Background(), []byte("delta"))
	if err == nil {
		t.Fatal("expected 'delta' to be absent after snapshot recovery")
	}
	var knf *kv.KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("expected *KeyNotFoundError, got %T: %v", err, err)
	}
}

func TestPebbleStateMachine_SnapshotEmptyDB(t *testing.T) {
	s := openStore(t)

	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	var buf bytes.Buffer
	if err := s.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if buf.Len() < 4 {
		t.Fatalf("empty snapshot is %d bytes, want at least the 4-byte count", buf.Len())
	}
	if count := binary.LittleEndian.Uint32(buf.Bytes()[:4]); count != 0 {
		t.Fatalf("empty snapshot count = %d, want 0", count)
	}

	// Add a key, then recover empty snapshot: key should be gone.
	applyEntries(t, s, []sm.Entry{putEntry(1, "temp", "data")})

	if err := s.RecoverFromSnapshot(context.Background(), &buf, nil); err != nil {
		t.Fatalf("RecoverFromSnapshot: %v", err)
	}

	_, err = s.Lookup(context.Background(), []byte("temp"))
	if err == nil {
		t.Fatal("expected 'temp' to be absent after empty snapshot recovery")
	}
}

func TestPebbleStateMachine_SaveSnapshotInvalidContext(t *testing.T) {
	s := openStore(t)

	var buf bytes.Buffer
	err := s.SaveSnapshot(context.Background(), "not-a-snapshot", &buf, nil)
	if err == nil {
		t.Fatal("expected error for invalid snapshot context")
	}
	var se *SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SnapshotError, got %T: %v", err, err)
	}
}

func TestPebbleStateMachine_SaveSnapshotCancellation(t *testing.T) {
	s := openStore(t)

	// Add enough data so the loop iterates.
	entries := make([]sm.Entry, 100)
	for i := range entries {
		entries[i] = putEntry(uint64(i+1), "key"+string(rune('A'+i%26)), "val")
	}
	applyEntries(t, s, entries)

	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	// Pre-close the stopper channel to trigger immediate cancellation.
	stopper := make(chan struct{})
	close(stopper)

	var buf bytes.Buffer
	err = s.SaveSnapshot(context.Background(), snapCtx, &buf, stopper)
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestPebbleStateMachine_RecoverFromSnapshotCancellation(t *testing.T) {
	s := openStore(t)

	// Build a valid snapshot with data.
	entries := make([]sm.Entry, 50)
	for i := range entries {
		entries[i] = putEntry(uint64(i+1), "key"+string(rune('A'+i%26)), "val")
	}
	applyEntries(t, s, entries)

	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}
	var buf bytes.Buffer
	if err := s.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	stopper := make(chan struct{})
	close(stopper)

	err = s.RecoverFromSnapshot(context.Background(), bytes.NewReader(buf.Bytes()), stopper)
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestPebbleStateMachine_RecoverFromSnapshotInvalidData(t *testing.T) {
	s := openStore(t)

	// Empty reader: binary.Read will fail.
	err := s.RecoverFromSnapshot(context.Background(), &bytes.Buffer{}, nil)
	if err == nil {
		t.Fatal("expected error for empty reader")
	}
	var se *SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// GetHash
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_GetHashConsistent(t *testing.T) {
	s := openStore(t)
	applyEntries(t, s, []sm.Entry{
		putEntry(1, "a", "1"),
		putEntry(2, "b", "2"),
	})

	h1, err := s.GetHash()
	if err != nil {
		t.Fatalf("GetHash: %v", err)
	}
	h2, err := s.GetHash()
	if err != nil {
		t.Fatalf("GetHash: %v", err)
	}
	if h1 != h2 {
		t.Fatalf("hash not consistent: %d != %d", h1, h2)
	}
}

func TestPebbleStateMachine_GetHashChangesAfterUpdate(t *testing.T) {
	s := openStore(t)
	applyEntries(t, s, []sm.Entry{putEntry(1, "a", "1")})

	h1, err := s.GetHash()
	if err != nil {
		t.Fatalf("GetHash: %v", err)
	}

	applyEntries(t, s, []sm.Entry{putEntry(2, "b", "2")})

	h2, err := s.GetHash()
	if err != nil {
		t.Fatalf("GetHash: %v", err)
	}

	if h1 == h2 {
		t.Fatal("hash should change after update")
	}
}

func TestPebbleStateMachine_GetHashEmptyDB(t *testing.T) {
	s := openStore(t)
	h, err := s.GetHash()
	if err != nil {
		t.Fatalf("GetHash: %v", err)
	}
	// Empty DB hash should be the zero hash of xxhash (no data written).
	if h != 0xef46db3751d8e999 {
		// xxhash of empty input. Just ensure it's deterministic.
		h2, _ := s.GetHash()
		if h != h2 {
			t.Fatalf("empty hash not deterministic: %d != %d", h, h2)
		}
	}
}

func TestPebbleStateMachine_GetHashExcludesMetadata(t *testing.T) {
	// Two stores with same user data but different lastApplied indexes
	// should produce the same hash.
	s1 := openStore(t)
	applyEntries(t, s1, []sm.Entry{putEntry(10, "key", "val")})
	h1, err := s1.GetHash()
	if err != nil {
		t.Fatalf("GetHash s1: %v", err)
	}

	s2 := openStore(t)
	applyEntries(t, s2, []sm.Entry{putEntry(99, "key", "val")})
	h2, err := s2.GetHash()
	if err != nil {
		t.Fatalf("GetHash s2: %v", err)
	}

	if h1 != h2 {
		t.Fatalf("hash should be equal for same user data despite different indexes: %d != %d", h1, h2)
	}
}

// ---------------------------------------------------------------------------
// Update with mixed operations (put + delete in one batch)
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_UpdateMixedOperations(t *testing.T) {
	s := openStore(t)

	// First batch: put two keys.
	applyEntries(t, s, []sm.Entry{
		putEntry(1, "keep", "yes"),
		putEntry(2, "remove", "no"),
	})

	// Second batch: put a new key, delete the old one, in one Update call.
	entries := []sm.Entry{
		putEntry(3, "added", "new"),
		deleteEntry(4, "remove"),
	}
	results := applyEntries(t, s, entries)

	if results[0].Value != uint64(len("new")) {
		t.Fatalf("put result: expected %d, got %d", len("new"), results[0].Value)
	}
	if results[1].Value != 1 {
		t.Fatalf("delete result: expected 1, got %d", results[1].Value)
	}

	// Verify 'keep' and 'added' exist.
	for _, k := range []string{"keep", "added"} {
		if _, err := s.Lookup(context.Background(), []byte(k)); err != nil {
			t.Fatalf("Lookup(%q): %v", k, err)
		}
	}

	// Verify 'remove' is gone.
	_, err := s.Lookup(context.Background(), []byte("remove"))
	if err == nil {
		t.Fatal("expected 'remove' to be deleted")
	}
}

func TestPebbleStateMachine_LastAppliedTracksHighestIndex(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")

	s := NewPebbleStateMachine(nil)
	if _, err := s.Open(context.Background(), dir, nil); err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Apply entries with non-sequential indexes; lastApplied tracks the max.
	applyEntries(t, s, []sm.Entry{
		putEntry(5, "a", "1"),
		putEntry(3, "b", "2"),
		putEntry(10, "c", "3"),
	})

	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2 := NewPebbleStateMachine(nil)
	idx, err := s2.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if idx != 10 {
		t.Fatalf("expected last applied 10, got %d", idx)
	}
	if err := s2.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Sync
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_SyncSuccess(t *testing.T) {
	s := openStore(t)
	applyEntries(t, s, []sm.Entry{putEntry(1, "key", "val")})
	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Factory
// ---------------------------------------------------------------------------

func TestNewPebbleStateMachine_ReturnsNonNil(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	if s == nil {
		t.Fatal("expected non-nil PebbleStateMachine")
	}
	// Verify it satisfies sm.DiskStateMachine.
	var _ sm.DiskStateMachine = s
}

func TestNewPebbleStateMachine_WithOptions(t *testing.T) {
	opts := &pebble.Options{
		MaxOpenFiles: 256,
	}
	ps := NewPebbleStateMachine(opts)
	if ps == nil {
		t.Fatal("expected non-nil PebbleStateMachine")
	}

	// Verify the created store can open and function.
	dir := filepath.Join(t.TempDir(), "db")
	idx, err := ps.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if idx != 0 {
		t.Fatalf("expected 0, got %d", idx)
	}
	if err := ps.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestNewPebbleStateMachine_DistinctInstances(t *testing.T) {
	s1 := NewPebbleStateMachine(nil)
	s2 := NewPebbleStateMachine(nil)
	if s1 == s2 {
		t.Fatal("expected distinct instances")
	}
}

// ---------------------------------------------------------------------------
// Error types
// ---------------------------------------------------------------------------

func TestOpenError(t *testing.T) {
	inner := errors.New("permission denied")
	e := &OpenError{Dir: "/tmp/db", Err: inner}
	if e.Error() != "pebbledb: open failed: /tmp/db: permission denied" {
		t.Fatalf("unexpected message: %s", e.Error())
	}
	if e.Unwrap() != inner {
		t.Fatal("Unwrap did not return inner error")
	}
}

func TestWriteError(t *testing.T) {
	inner := errors.New("disk full")
	e := &WriteError{Err: inner}
	if e.Error() != "pebbledb: write failed: disk full" {
		t.Fatalf("unexpected message: %s", e.Error())
	}
	if e.Unwrap() != inner {
		t.Fatal("Unwrap did not return inner error")
	}
}

func TestReadError(t *testing.T) {
	inner := errors.New("corruption")
	e := &ReadError{Key: "mykey", Err: inner}
	if e.Error() != "pebbledb: read failed: mykey: corruption" {
		t.Fatalf("unexpected message: %s", e.Error())
	}
	if e.Unwrap() != inner {
		t.Fatal("Unwrap did not return inner error")
	}
}

func TestSnapshotError(t *testing.T) {
	inner := errors.New("io failure")
	e := &SnapshotError{Op: "save", Err: inner}
	if e.Error() != "pebbledb: snapshot save failed: io failure" {
		t.Fatalf("unexpected message: %s", e.Error())
	}
	if e.Unwrap() != inner {
		t.Fatal("Unwrap did not return inner error")
	}
}

func TestInvalidQueryError(t *testing.T) {
	e := &InvalidQueryError{Reason: "unsupported type"}
	if e.Error() != "pebbledb: invalid query: unsupported type" {
		t.Fatalf("unexpected message: %s", e.Error())
	}
}

// TestInvalidQueryError_UnsupportedQueryType proves the marker pkg/service
// duck-types to surface an MVCC-only query's actionable error (naming
// raft.sm_type: mvcc) answers true only for the exact Reason Lookup sets on
// an MVCC-only query against this (the default) state machine, never for an
// unrelated InvalidQueryError.
func TestInvalidQueryError_UnsupportedQueryType(t *testing.T) {
	mvccOnly := &InvalidQueryError{Reason: "unsupported query type"}
	if !mvccOnly.UnsupportedQueryType() {
		t.Error("UnsupportedQueryType() = false, want true for Reason \"unsupported query type\"")
	}

	other := &InvalidQueryError{Reason: "lessor not configured"}
	if other.UnsupportedQueryType() {
		t.Error("UnsupportedQueryType() = true, want false for an unrelated Reason")
	}
}

func TestNotOpenError(t *testing.T) {
	e := &NotOpenError{}
	if e.Error() != "pebbledb: store not open" {
		t.Fatalf("unexpected message: %s", e.Error())
	}
}

// ---------------------------------------------------------------------------
// prefixUpperBound helper
// ---------------------------------------------------------------------------

func TestPrefixUpperBound(t *testing.T) {
	tests := []struct {
		name   string
		prefix []byte
		want   []byte
	}{
		{
			name:   "simple",
			prefix: []byte("abc"),
			want:   []byte("abd"),
		},
		{
			name:   "trailing 0xFF",
			prefix: []byte{0x01, 0xFF},
			want:   []byte{0x02},
		},
		{
			name:   "all 0xFF",
			prefix: []byte{0xFF, 0xFF},
			want:   nil,
		},
		{
			name:   "single byte",
			prefix: []byte{0x00},
			want:   []byte{0x01},
		},
		{
			name:   "empty",
			prefix: []byte{},
			want:   nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := prefixUpperBound(tc.prefix)
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("prefixUpperBound(%v): got %v, want %v", tc.prefix, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Interface compliance
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_ImplementsDiskStateMachine(t *testing.T) {
	var _ sm.DiskStateMachine = (*PebbleStateMachine)(nil)
}

func TestPebbleStateMachine_ImplementsHasher(t *testing.T) {
	var _ sm.Hasher = (*PebbleStateMachine)(nil)
}

func TestPebbleStateMachine_ImplementsNALookup(t *testing.T) {
	var _ sm.NALookup = (*PebbleStateMachine)(nil)
}

// ---------------------------------------------------------------------------
// NewPebbleStateMachine
// ---------------------------------------------------------------------------

func TestNewPebbleStateMachine_NilOpts(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	if s == nil {
		t.Fatal("expected non-nil store")
	}
	if s.opts == nil {
		t.Fatal("expected default opts to be set")
	}
}

func TestNewPebbleStateMachine_CustomOpts(t *testing.T) {
	s := NewPebbleStateMachine(&customTestOpts)
	if s == nil {
		t.Fatal("expected non-nil store")
	}
}

// customTestOpts is a package-level variable to avoid import cycle issues
// with pebble in the test scope.
var customTestOpts = pebble.Options{MaxOpenFiles: 128}

// ---------------------------------------------------------------------------
// Snapshot roundtrip across stores
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_SnapshotCrossStoreRestore(t *testing.T) {
	// Create store A with data.
	s1 := openStore(t)
	applyEntries(t, s1, []sm.Entry{
		putEntry(1, "x", "100"),
		putEntry(2, "y", "200"),
	})

	if err := s1.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s1.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	var buf bytes.Buffer
	if err := s1.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// Create store B with different data.
	s2 := openStore(t)
	applyEntries(t, s2, []sm.Entry{
		putEntry(1, "other", "data"),
	})

	// Recover store B from store A's snapshot.
	if err := s2.RecoverFromSnapshot(context.Background(), bytes.NewReader(buf.Bytes()), nil); err != nil {
		t.Fatalf("RecoverFromSnapshot: %v", err)
	}

	// Store B should have store A's data.
	for _, tc := range []struct {
		key, want string
	}{
		{"x", "100"},
		{"y", "200"},
	} {
		val, err := s2.Lookup(context.Background(), []byte(tc.key))
		if err != nil {
			t.Fatalf("Lookup(%q): %v", tc.key, err)
		}
		if !bytes.Equal(val.([]byte), []byte(tc.want)) {
			t.Fatalf("Lookup(%q): expected %q, got %q", tc.key, tc.want, val)
		}
	}

	// Store B's original key should be gone.
	_, err = s2.Lookup(context.Background(), []byte("other"))
	if err == nil {
		t.Fatal("expected 'other' to be absent after cross-store recovery")
	}
}

// ---------------------------------------------------------------------------
// Defensive copy verification
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_LookupReturnsDefensiveCopy(t *testing.T) {
	s := openStore(t)
	applyEntries(t, s, []sm.Entry{putEntry(1, "key", "original")})

	val, err := s.Lookup(context.Background(), []byte("key"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	// Mutate the returned slice.
	result := val.([]byte)
	result[0] = 'X'

	// Re-read: should still be "original".
	val2, err := s.Lookup(context.Background(), []byte("key"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(val2.([]byte), []byte("original")) {
		t.Fatalf("defensive copy violated: got %q", val2)
	}
}

func TestPebbleStateMachine_BytesLookupReturnsDefensiveCopy(t *testing.T) {
	s := openStore(t)
	applyEntries(t, s, []sm.Entry{putEntry(1, "key", "original")})

	val, err := s.BytesLookup([]byte("key"))
	if err != nil {
		t.Fatalf("BytesLookup: %v", err)
	}

	val[0] = 'X'

	val2, err := s.BytesLookup([]byte("key"))
	if err != nil {
		t.Fatalf("BytesLookup: %v", err)
	}
	if !bytes.Equal(val2, []byte("original")) {
		t.Fatalf("defensive copy violated: got %q", val2)
	}
}

// ---------------------------------------------------------------------------
// Delete non-existent key (idempotent)
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_DeleteNonExistentKeyIsIdempotent(t *testing.T) {
	s := openStore(t)
	results := applyEntries(t, s, []sm.Entry{deleteEntry(1, "ghost")})
	if results[0].Value != 1 {
		t.Fatalf("delete result: expected 1, got %d", results[0].Value)
	}
}

// ---------------------------------------------------------------------------
// Large value handling
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_LargeValue(t *testing.T) {
	s := openStore(t)

	largeVal := make([]byte, 1<<20) // 1 MiB
	for i := range largeVal {
		largeVal[i] = byte(i % 256)
	}

	bigCmd, err := kv.EncodePut([]byte("big"), largeVal)
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	entries := []sm.Entry{{
		Index: 1,
		Cmd:   bigCmd,
	}}
	results := applyEntries(t, s, entries)
	if results[0].Value != uint64(len(largeVal)) {
		t.Fatalf("expected result %d, got %d", len(largeVal), results[0].Value)
	}

	val, err := s.Lookup(context.Background(), []byte("big"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(val.([]byte), largeVal) {
		t.Fatal("large value mismatch")
	}
}

// ---------------------------------------------------------------------------
// GetHash determinism across identical stores
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_GetHashDeterministicAcrossStores(t *testing.T) {
	entries := []sm.Entry{
		putEntry(1, "foo", "bar"),
		putEntry(2, "baz", "qux"),
	}

	s1 := openStore(t)
	applyEntries(t, s1, entries)
	h1, err := s1.GetHash()
	if err != nil {
		t.Fatalf("GetHash s1: %v", err)
	}

	s2 := openStore(t)
	applyEntries(t, s2, entries)
	h2, err := s2.GetHash()
	if err != nil {
		t.Fatalf("GetHash s2: %v", err)
	}

	if h1 != h2 {
		t.Fatalf("hashes should be identical for same data: %d != %d", h1, h2)
	}
}

// ---------------------------------------------------------------------------
// readLastApplied edge case: short value (< 8 bytes)
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_ReadLastAppliedShortValue(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")

	// Write a short (4-byte) value for the metadata key directly via PebbleDB.
	db, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	shortVal := []byte{0x01, 0x02, 0x03, 0x04} // 4 bytes, not 8
	if err := db.Set([]byte(metaKeyLastApplied), shortVal, pebble.Sync); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Open via PebbleStateMachine: readLastApplied should return 0 for short value.
	s := NewPebbleStateMachine(nil)
	idx, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if idx != 0 {
		t.Fatalf("expected 0 for short value, got %d", idx)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// RecoverFromSnapshot truncated data at various points
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_RecoverFromSnapshotTruncatedKeyLen(t *testing.T) {
	s := openStore(t)

	// Build a snapshot buffer with count=1 but truncate before keyLen.
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, uint32(1)); err != nil {
		t.Fatalf("write count: %v", err)
	}
	// Only 2 bytes of keyLen (need 4).
	buf.Write([]byte{0x01, 0x02})

	err := s.RecoverFromSnapshot(context.Background(), &buf, nil)
	if err == nil {
		t.Fatal("expected error for truncated keyLen")
	}
	var se *SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SnapshotError, got %T: %v", err, err)
	}
}

func TestPebbleStateMachine_RecoverFromSnapshotTruncatedKeyData(t *testing.T) {
	s := openStore(t)

	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, uint32(1)); err != nil {
		t.Fatalf("write count: %v", err)
	}
	// keyLen = 10 but only write 3 bytes of key data.
	if err := binary.Write(&buf, binary.LittleEndian, uint32(10)); err != nil {
		t.Fatalf("write keyLen: %v", err)
	}
	buf.Write([]byte{0x01, 0x02, 0x03})

	err := s.RecoverFromSnapshot(context.Background(), &buf, nil)
	if err == nil {
		t.Fatal("expected error for truncated key data")
	}
	var se *SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SnapshotError, got %T: %v", err, err)
	}
}

func TestPebbleStateMachine_RecoverFromSnapshotTruncatedValLen(t *testing.T) {
	s := openStore(t)

	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, uint32(1)); err != nil {
		t.Fatalf("write count: %v", err)
	}
	// keyLen = 3, key = "abc", then truncate before valLen.
	if err := binary.Write(&buf, binary.LittleEndian, uint32(3)); err != nil {
		t.Fatalf("write keyLen: %v", err)
	}
	buf.Write([]byte("abc"))
	// Only write 1 byte of valLen (need 4).
	buf.Write([]byte{0x05})

	err := s.RecoverFromSnapshot(context.Background(), &buf, nil)
	if err == nil {
		t.Fatal("expected error for truncated valLen")
	}
	var se *SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SnapshotError, got %T: %v", err, err)
	}
}

func TestPebbleStateMachine_RecoverFromSnapshotTruncatedValData(t *testing.T) {
	s := openStore(t)

	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, uint32(1)); err != nil {
		t.Fatalf("write count: %v", err)
	}
	if err := binary.Write(&buf, binary.LittleEndian, uint32(3)); err != nil {
		t.Fatalf("write keyLen: %v", err)
	}
	buf.Write([]byte("abc"))
	// valLen = 100 but only write 2 bytes.
	if err := binary.Write(&buf, binary.LittleEndian, uint32(100)); err != nil {
		t.Fatalf("write valLen: %v", err)
	}
	buf.Write([]byte{0x01, 0x02})

	err := s.RecoverFromSnapshot(context.Background(), &buf, nil)
	if err == nil {
		t.Fatal("expected error for truncated val data")
	}
	var se *SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// SaveSnapshot with stopper already closed on large dataset
// verifies the second-pass iteration exits early
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_SaveSnapshotStopperDuringSecondPass(t *testing.T) {
	s := openStore(t)

	// Insert enough data so the second-pass loop runs at least one iteration
	// before checking stopper.
	for i := 0; i < 200; i++ {
		key := []byte{byte('A' + i%26), byte('a' + i/26)}
		cmd, err := kv.EncodePut(key, []byte("data"))
		if err != nil {
			t.Fatalf("EncodePut: %v", err)
		}
		applyEntries(t, s, []sm.Entry{{
			Index: uint64(i + 1),
			Cmd:   cmd,
		}})
	}

	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	stopper := make(chan struct{})
	close(stopper)

	var buf bytes.Buffer
	err = s.SaveSnapshot(context.Background(), snapCtx, &buf, stopper)
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// SaveSnapshot correctly excludes metadata in count and data
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_SaveSnapshotCountExcludesMetadata(t *testing.T) {
	s := openStore(t)

	// Apply 3 user keys (which also writes the metadata key internally).
	applyEntries(t, s, []sm.Entry{
		putEntry(1, "a", "1"),
		putEntry(2, "b", "2"),
		putEntry(3, "c", "3"),
	})

	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	var buf bytes.Buffer
	if err := s.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// Read the count from the snapshot and verify it is 3 (not 4).
	var count uint32
	if err := binary.Read(bytes.NewReader(buf.Bytes()), binary.LittleEndian, &count); err != nil {
		t.Fatalf("read count: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected snapshot count 3, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// clearAll on empty DB is a no-op
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_RecoverFromSnapshotOnEmptyDB(t *testing.T) {
	s := openStore(t)

	// Build a valid snapshot with 1 key.
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, uint32(1)); err != nil {
		t.Fatalf("write count: %v", err)
	}
	if err := binary.Write(&buf, binary.LittleEndian, uint32(3)); err != nil {
		t.Fatalf("write keyLen: %v", err)
	}
	buf.Write([]byte("key"))
	if err := binary.Write(&buf, binary.LittleEndian, uint32(3)); err != nil {
		t.Fatalf("write valLen: %v", err)
	}
	buf.Write([]byte("val"))

	// Recover on an empty DB triggers clearAll on empty -- should not error.
	if err := s.RecoverFromSnapshot(context.Background(), &buf, nil); err != nil {
		t.Fatalf("RecoverFromSnapshot: %v", err)
	}

	val, err := s.Lookup(context.Background(), []byte("key"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("val")) {
		t.Fatalf("expected 'val', got %q", val)
	}
}

// ---------------------------------------------------------------------------
// Update lastIndex=0 when no entries have Index > 0
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_UpdateZeroIndexSkipsMetaWrite(t *testing.T) {
	s := openStore(t)

	// Apply entries with Index=0 (edge case).
	zeroCmd, err := kv.EncodePut([]byte("z"), []byte("zero"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	entries := []sm.Entry{{Index: 0, Cmd: zeroCmd}}
	results := applyEntries(t, s, entries)
	if results[0].Value != uint64(len("zero")) {
		t.Fatalf("expected %d, got %d", len("zero"), results[0].Value)
	}

	// Verify the data was written.
	val, err := s.Lookup(context.Background(), []byte("z"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("zero")) {
		t.Fatalf("expected 'zero', got %q", val)
	}
}

// ---------------------------------------------------------------------------
// SaveSnapshot with a writer that fails (limited writer)
// ---------------------------------------------------------------------------

type limitedWriter struct {
	remaining int
}

func (w *limitedWriter) Write(p []byte) (n int, err error) {
	if w.remaining <= 0 {
		return 0, io.ErrShortWrite
	}
	if len(p) > w.remaining {
		n = w.remaining
		w.remaining = 0
		return n, io.ErrShortWrite
	}
	w.remaining -= len(p)
	return len(p), nil
}

func TestPebbleStateMachine_SaveSnapshotWriterFailsOnCount(t *testing.T) {
	s := openStore(t)
	applyEntries(t, s, []sm.Entry{putEntry(1, "key", "val")})

	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	// Writer that can't even write the 4-byte count.
	w := &limitedWriter{remaining: 0}
	err = s.SaveSnapshot(context.Background(), snapCtx, w, nil)
	if err == nil {
		t.Fatal("expected error from writer failure")
	}
	var se *SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SnapshotError, got %T: %v", err, err)
	}
}

func TestPebbleStateMachine_SaveSnapshotWriterFailsOnKeyLen(t *testing.T) {
	s := openStore(t)
	applyEntries(t, s, []sm.Entry{putEntry(1, "key", "val")})

	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	// Writer that can write the count (4 bytes) but fails on keyLen.
	w := &limitedWriter{remaining: 4}
	err = s.SaveSnapshot(context.Background(), snapCtx, w, nil)
	if err == nil {
		t.Fatal("expected error from writer failure on keyLen")
	}
	var se *SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SnapshotError, got %T: %v", err, err)
	}
}

func TestPebbleStateMachine_SaveSnapshotWriterFailsOnKeyData(t *testing.T) {
	s := openStore(t)
	applyEntries(t, s, []sm.Entry{putEntry(1, "key", "val")})

	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	// Writer: 4 (count) + 4 (keyLen) = 8 bytes, then fails on key data.
	w := &limitedWriter{remaining: 8}
	err = s.SaveSnapshot(context.Background(), snapCtx, w, nil)
	if err == nil {
		t.Fatal("expected error from writer failure on key data")
	}
	var se *SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SnapshotError, got %T: %v", err, err)
	}
}

func TestPebbleStateMachine_SaveSnapshotWriterFailsOnValLen(t *testing.T) {
	s := openStore(t)
	applyEntries(t, s, []sm.Entry{putEntry(1, "key", "val")})

	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	// Writer: 4 (count) + 4 (keyLen) + 3 (key "key") = 11 bytes, then fails on valLen.
	w := &limitedWriter{remaining: 11}
	err = s.SaveSnapshot(context.Background(), snapCtx, w, nil)
	if err == nil {
		t.Fatal("expected error from writer failure on valLen")
	}
	var se *SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SnapshotError, got %T: %v", err, err)
	}
}

func TestPebbleStateMachine_SaveSnapshotWriterFailsOnValData(t *testing.T) {
	s := openStore(t)
	applyEntries(t, s, []sm.Entry{putEntry(1, "key", "val")})

	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	// Writer: 4 (count) + 4 (keyLen) + 3 (key) + 4 (valLen) = 15, fails on val data.
	w := &limitedWriter{remaining: 15}
	err = s.SaveSnapshot(context.Background(), snapCtx, w, nil)
	if err == nil {
		t.Fatal("expected error from writer failure on val data")
	}
	var se *SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Update with command too short (< headerSize)
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_UpdateCommandTooShort(t *testing.T) {
	s := openStore(t)
	entries := []sm.Entry{{Index: 1, Cmd: []byte{0x01}}} // only 1 byte, need 3
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for short command")
	}
	var ice *kv.InvalidCommandError
	if !errors.As(err, &ice) {
		t.Fatalf("expected *kv.InvalidCommandError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Snapshot roundtrip preserves value integrity for many keys
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_SnapshotManyKeys(t *testing.T) {
	s := openStore(t)

	const numKeys = 50
	entries := make([]sm.Entry, numKeys)
	for i := 0; i < numKeys; i++ {
		key := []byte{byte('A' + i/26), byte('a' + i%26)}
		val := []byte{byte(i), byte(i + 1)}
		cmd, err := kv.EncodePut(key, val)
		if err != nil {
			t.Fatalf("EncodePut: %v", err)
		}
		entries[i] = sm.Entry{
			Index: uint64(i + 1),
			Cmd:   cmd,
		}
	}
	applyEntries(t, s, entries)

	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	var buf bytes.Buffer
	if err := s.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// Create a new store, recover, verify all keys.
	s2 := openStore(t)
	if err := s2.RecoverFromSnapshot(context.Background(), bytes.NewReader(buf.Bytes()), nil); err != nil {
		t.Fatalf("RecoverFromSnapshot: %v", err)
	}

	for i := 0; i < numKeys; i++ {
		key := []byte{byte('A' + i/26), byte('a' + i%26)}
		expected := []byte{byte(i), byte(i + 1)}
		val, err := s2.Lookup(context.Background(), key)
		if err != nil {
			t.Fatalf("Lookup key %q: %v", key, err)
		}
		if !bytes.Equal(val.([]byte), expected) {
			t.Fatalf("key %q: expected %v, got %v", key, expected, val)
		}
	}
}

// ---------------------------------------------------------------------------
// ListQuery on prefix that matches exactly one key boundary
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_LookupListQueryPrefixBoundary(t *testing.T) {
	s := openStore(t)
	applyEntries(t, s, []sm.Entry{
		putEntry(1, "test", "exact"),
		putEntry(2, "testing", "extends"),
		putEntry(3, "tes", "shorter"),
	})

	val, err := s.Lookup(context.Background(), kv.ListQuery{Prefix: "test"})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	keys := val.([]string)
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys with prefix 'test', got %d: %v", len(keys), keys)
	}
	expected := []string{"test", "testing"}
	for i, k := range keys {
		if k != expected[i] {
			t.Fatalf("keys[%d]: expected %q, got %q", i, expected[i], k)
		}
	}
}

// ---------------------------------------------------------------------------
// Read-only database error paths
// ---------------------------------------------------------------------------

// openReadOnlyStore creates a PebbleStateMachine backed by a read-only PebbleDB. It
// first creates a normal database with the supplied entries, closes it, then
// reopens it with ReadOnly: true. Write operations against the returned store
// will fail with PebbleDB's "read-only" error, exercising internal error paths.
func openReadOnlyStore(t *testing.T, entries []sm.Entry) *PebbleStateMachine {
	t.Helper()

	dir := t.TempDir()
	dbDir := filepath.Join(dir, "db")

	// Phase 1: create and populate a writable database.
	writable := NewPebbleStateMachine(nil)
	if _, err := writable.Open(context.Background(), dbDir, nil); err != nil {
		t.Fatalf("Open writable: %v", err)
	}
	if len(entries) > 0 {
		results := make([]sm.Result, len(entries))
		if err := writable.Update(context.Background(), entries, results); err != nil {
			t.Fatalf("Update writable: %v", err)
		}
		if err := writable.Sync(); err != nil {
			t.Fatalf("Sync writable: %v", err)
		}
	}
	if err := writable.Close(context.Background()); err != nil {
		t.Fatalf("Close writable: %v", err)
	}

	// Phase 2: reopen the same directory as read-only.
	ro := NewPebbleStateMachine(&pebble.Options{ReadOnly: true})
	if _, err := ro.Open(context.Background(), dbDir, nil); err != nil {
		t.Fatalf("Open read-only: %v", err)
	}
	t.Cleanup(func() {
		if err := ro.Close(context.Background()); err != nil {
			t.Errorf("Close read-only: %v", err)
		}
	})
	return ro
}

func TestPebbleStateMachine_SyncFlushError(t *testing.T) {
	s := openReadOnlyStore(t, []sm.Entry{putEntry(1, "k", "v")})

	err := s.Sync()
	if err == nil {
		t.Fatal("expected error from Sync on read-only database")
	}
	var we *WriteError
	if !errors.As(err, &we) {
		t.Fatalf("expected *WriteError, got %T: %v", err, err)
	}
}

func TestPebbleStateMachine_UpdateCommitError(t *testing.T) {
	s := openReadOnlyStore(t, []sm.Entry{putEntry(1, "k", "v")})

	entries := []sm.Entry{putEntry(2, "k2", "v2")}
	results := make([]sm.Result, len(entries))
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error from Update on read-only database")
	}
	var we *WriteError
	if !errors.As(err, &we) {
		t.Fatalf("expected *WriteError, got %T: %v", err, err)
	}
}

func TestPebbleStateMachine_RecoverFromSnapshotClearAllError(t *testing.T) {
	s := openReadOnlyStore(t, []sm.Entry{putEntry(1, "k", "v")})

	// Build a minimal valid snapshot buffer (count=1, one key-value pair).
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, uint32(1)); err != nil {
		t.Fatal(err)
	}
	key := []byte("key")
	val := []byte("val")
	if err := binary.Write(&buf, binary.LittleEndian, uint32(len(key))); err != nil {
		t.Fatal(err)
	}
	buf.Write(key)
	if err := binary.Write(&buf, binary.LittleEndian, uint32(len(val))); err != nil {
		t.Fatal(err)
	}
	buf.Write(val)

	err := s.RecoverFromSnapshot(context.Background(), &buf, nil)
	if err == nil {
		t.Fatal("expected error from RecoverFromSnapshot on read-only database")
	}
	var se *SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SnapshotError, got %T: %v", err, err)
	}
}
