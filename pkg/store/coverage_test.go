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

//go:build !integration

package store

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// factory.go: WithWALDir option
// ---------------------------------------------------------------------------

func TestWithWALDir_SetsWALDir(t *testing.T) {
	walDir := t.TempDir()
	opt := WithWALDir(walDir)
	pOpts := &pebble.Options{}
	opt(pOpts)
	if pOpts.WALDir != walDir {
		t.Fatalf("expected WALDir %q, got %q", walDir, pOpts.WALDir)
	}
}

// ---------------------------------------------------------------------------
// pebble.go: Lookup with string query (PebbleStateMachine)
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_LookupStringKeyNotFound(t *testing.T) {
	s := openStore(t)
	_, err := s.Lookup(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing key via string")
	}
	var knf *kv.KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("expected *KeyNotFoundError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// pebble.go: PebbleStateMachine.listKeys excludes __sys: keys
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_ListKeysExcludesSysKeys(t *testing.T) {
	s := openStore(t)

	// Write system and user keys.
	applyEntries(t, s, []sm.Entry{
		putEntry(1, "__sys:role:admin", "data"),
		putEntry(2, "user:key", "val"),
	})

	val, err := s.Lookup(context.Background(), kv.ListQuery{})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	keys := val.([]string)
	for _, k := range keys {
		if len(k) >= 6 && k[:6] == "__sys:" {
			t.Fatalf("system key should be excluded: %q", k)
		}
	}
	found := false
	for _, k := range keys {
		if k == "user:key" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected user:key to be present")
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: MVCCStateMachine.listKeys excludes __sys: keys
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_ListKeysExcludesSysKeys(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("__sys:test:entry"), []byte("data"), 0),
		mvccPutEntry(2, []byte("user:key"), []byte("val"), 0),
	})

	result, err := s.Lookup(context.Background(), kv.ListQuery{})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	keys := result.([]string)
	for _, k := range keys {
		if len(k) >= 6 && k[:6] == "__sys:" {
			t.Fatalf("system key should be excluded: %q", k)
		}
	}
}

// ---------------------------------------------------------------------------
// pebble.go: PebbleStateMachine.listKeys excludes TimeSeries internal keys
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_ListKeysExcludesTimeSeriesKeys(t *testing.T) {
	s := openStore(t)

	applyEntries(t, s, []sm.Entry{
		putEntry(1, "ts:metric:cpu:...", "data"),
		putEntry(2, "idx:metric:cpu", "data"),
		putEntry(3, "idx:tag:host:server1", "data"),
		putEntry(4, "retention:cpu", "data"),
		putEntry(5, "user:visible", "val"),
	})

	val, err := s.Lookup(context.Background(), kv.ListQuery{})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	keys := val.([]string)

	tsPrefixes := []string{"ts:", "idx:metric:", "idx:tag:", "retention:"}
	for _, k := range keys {
		for _, prefix := range tsPrefixes {
			if strings.HasPrefix(k, prefix) {
				t.Fatalf("internal TS key should be excluded: %q", k)
			}
		}
	}

	found := false
	for _, k := range keys {
		if k == "user:visible" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected user:visible to be present")
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: MVCCStateMachine.listKeys excludes TimeSeries internal keys
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_ListKeysExcludesTimeSeriesKeys(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("ts:metric:cpu"), []byte("data"), 0),
		mvccPutEntry(2, []byte("idx:metric:cpu"), []byte("data"), 0),
		mvccPutEntry(3, []byte("idx:tag:host:server1"), []byte("data"), 0),
		mvccPutEntry(4, []byte("retention:cpu"), []byte("data"), 0),
		mvccPutEntry(5, []byte("user:visible"), []byte("val"), 0),
	})

	result, err := s.Lookup(context.Background(), kv.ListQuery{})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	keys := result.([]string)

	tsPrefixes := []string{"ts:", "idx:metric:", "idx:tag:", "retention:"}
	for _, k := range keys {
		for _, prefix := range tsPrefixes {
			if strings.HasPrefix(k, prefix) {
				t.Fatalf("internal TS key should be excluded: %q", k)
			}
		}
	}

	found := false
	for _, k := range keys {
		if k == "user:visible" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected user:visible to be present")
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: MVCCStateMachine.lookupPrefix excludes internal keys
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_LookupPrefixExcludesInternalKeys(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("__sys:role:admin"), []byte("data"), 0),
		mvccPutEntry(2, []byte("ts:metric:cpu"), []byte("data"), 0),
		mvccPutEntry(3, []byte("app:key1"), []byte("val1"), 0),
	})

	result, err := s.Lookup(context.Background(), &statemachine.PrefixQuery{Prefix: []byte("")})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	m := result.(map[string][]byte)

	if _, ok := m["app:key1"]; !ok {
		t.Fatal("expected app:key1 to be present in prefix scan results")
	}
	if _, ok := m["__sys:role:admin"]; ok {
		t.Fatal("__sys:role:admin should be excluded from prefix scan results")
	}
	if _, ok := m["ts:metric:cpu"]; ok {
		t.Fatal("ts:metric:cpu should be excluded from prefix scan results")
	}
}

// ---------------------------------------------------------------------------
// pebble.go: isInternalKey helper
// ---------------------------------------------------------------------------

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

// TestPebbleStateMachine_ListAndScanGuardTheRelationalKeyspace exercises both
// halves of the guard through the state machine itself: a whole-keyspace
// enumeration must not disclose relational keys, and an enumeration rooted at
// the table's own prefix must return them.
func TestPebbleStateMachine_ListAndScanGuardTheRelationalKeyspace(t *testing.T) {
	s := openStore(t)

	seed := []struct{ key, value string }{
		{"app/a", "user data"},
		{"__tbl:shop:1/row", "classified"},
		{"__idx:shop:1:uq_e/a@b", "pk"},
		{"__seq:shop:__tableid", "counter"},
	}
	entries := make([]sm.Entry, len(seed))
	for i, kvp := range seed {
		entries[i] = putEntry(uint64(i+1), kvp.key, kvp.value)
	}
	applyEntries(t, s, entries)

	keys, err := s.listKeys("")
	if err != nil {
		t.Fatalf("listKeys: %v", err)
	}
	if len(keys) != 1 || keys[0] != "app/a" {
		t.Fatalf("listKeys(\"\") = %q, want only the user key", keys)
	}

	pairs, err := s.scanPrefix(&statemachine.PrefixQuery{})
	if err != nil {
		t.Fatalf("scanPrefix: %v", err)
	}
	if len(pairs) != 1 {
		t.Fatalf("scanPrefix(\"\") = %v, want only the user key", pairs)
	}

	rows, err := s.scanPrefix(&statemachine.PrefixQuery{Prefix: []byte("__tbl:shop:1/")})
	if err != nil {
		t.Fatalf("scanPrefix rows: %v", err)
	}
	if string(rows["__tbl:shop:1/row"]) != "classified" {
		t.Fatalf("the table's own scan returned %v -- the guard is hiding the relational layer from its own rows", rows)
	}
}

// TestPebbleStateMachine_LookupRichQuery dispatches a rich time-series query
// through Lookup against a store that has time-series storage configured,
// which is the branch the "time-series storage not configured" refusal beside
// it guards.
func TestPebbleStateMachine_LookupRichQuery(t *testing.T) {
	s := openStoreWithTS(t)

	got, err := s.Lookup(context.Background(), timeseries.TSRichQueryRequest{
		Request: &timeseries.QueryRequest{
			Metric:    "cpu",
			StartTime: time.Unix(0, 0),
			EndTime:   time.Unix(0, 0).Add(time.Hour),
		},
	})
	if err != nil {
		t.Fatalf("Lookup rich query: %v", err)
	}
	if _, ok := got.(*timeseries.QueryResult); !ok {
		t.Fatalf("Lookup rich query returned %T, want *timeseries.QueryResult", got)
	}
}

// TestPebbleStateMachine_PutIfAbsentNotifiesWatchers covers the notification
// half of a successful conditional put: a watcher must see the created key.
// The declined case carries no notification because nothing changed, so this
// is the only path that emits one.
func TestPebbleStateMachine_PutIfAbsentNotifiesWatchers(t *testing.T) {
	s, hub := openStoreWithHub(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := hub.Watch(ctx, "fresh")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = w.Close() }()

	results := applyEntries(t, s, []sm.Entry{
		mvccPutIfAbsentEntry(1, []byte("fresh"), []byte("v")),
	})
	if results[0].Value != kv.ResultStored {
		t.Fatalf("PutIfAbsent on an absent key returned %d, want stored", results[0].Value)
	}

	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 || resp.Events[0].Type != watch.EventPut {
			t.Fatalf("expected one put event, got %+v", resp.Events)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the conditional put's watch event")
	}
}

// TestPebbleStateMachine_ClearAllOnEmptyDB covers the empty-database path of
// the snapshot recovery helper: a node recovering into a store that holds
// nothing has no range to delete, and that is a no-op rather than an error.
func TestPebbleStateMachine_ClearAllOnEmptyDB(t *testing.T) {
	s := openStore(t)
	if err := s.clearAll(); err != nil {
		t.Fatalf("clearAll on an empty database: %v", err)
	}
}

// ---------------------------------------------------------------------------
// pebble.go: PebbleStateMachine SaveSnapshot empty DB count=0
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_SaveSnapshotEmptyDBVerifyCount(t *testing.T) {
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
		t.Fatalf("expected at least 4 bytes, got %d", buf.Len())
	}
	count := binary.LittleEndian.Uint32(buf.Bytes()[:4])
	if count != 0 {
		t.Fatalf("expected count 0 for empty DB snapshot, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// pebble.go: SnapshotExcludesMetadata for PebbleStateMachine
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_SnapshotExcludesMetadata(t *testing.T) {
	s := openStore(t)

	applyEntries(t, s, []sm.Entry{putEntry(1, "userkey", "val")})

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

	// Recover on a second store.
	s2 := openStore(t)
	if err := s2.RecoverFromSnapshot(context.Background(), bytes.NewReader(buf.Bytes()), nil); err != nil {
		t.Fatalf("RecoverFromSnapshot: %v", err)
	}

	val, err := s2.Lookup(context.Background(), []byte("userkey"))
	if err != nil {
		t.Fatalf("Lookup after recover: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("val")) {
		t.Fatalf("expected 'val', got %q", val)
	}
}

// ---------------------------------------------------------------------------
// pebble.go: PebbleStateMachine listKeys all-0xFF prefix upper bound
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_ListKeysAllFFPrefix(t *testing.T) {
	s := openStore(t)

	// Use a prefix that's all 0xFF - prefixUpperBound returns nil.
	val, err := s.Lookup(context.Background(), kv.ListQuery{Prefix: string([]byte{0xFF, 0xFF})})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	keys := val.([]string)
	if len(keys) != 0 {
		t.Fatalf("expected empty keys, got %v", keys)
	}
}
