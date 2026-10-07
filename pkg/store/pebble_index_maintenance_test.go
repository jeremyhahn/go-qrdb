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
	"strings"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// indexQueryKeys runs an IndexQueryRequest and returns the matching entity
// keys as strings, for assertions that read better than a [][]byte compare.
func indexQueryKeys(t *testing.T, s *PebbleStateMachine, indexName, value string) []string {
	t.Helper()
	result, err := s.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: indexName,
		Value:     []byte(value),
	})
	if err != nil {
		t.Fatalf("Lookup IndexQueryRequest(%s, %s): %v", indexName, value, err)
	}
	raw := result.([][]byte)
	keys := make([]string, len(raw))
	for i, k := range raw {
		keys[i] = string(k)
	}
	return keys
}

func containsKey(keys []string, want string) bool {
	for _, k := range keys {
		if k == want {
			return true
		}
	}
	return false
}

// TestPebbleStateMachine_Put_PopulatesIndex pins that an ordinary `kv put`
// through the real apply path -- not a test manually calling idx.Put --
// populates a registered index. Before this fix, pebbleHandlePut never
// touched s.indexes at all: IndexCreate succeeded and the index existed, but
// every subsequent write was invisible to it, so IndexQuery always answered
// an empty, nil-error result regardless of what was written.
func TestPebbleStateMachine_Put_PopulatesIndex(t *testing.T) {
	s := openExtendedStore(t)

	createCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_status", "status", protocol.IndexCreateOptions{}))
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: createCmd}})

	put1 := mustEncode(kv.EncodePut([]byte("user/1"), []byte(`{"status":"active"}`)))
	put2 := mustEncode(kv.EncodePut([]byte("user/2"), []byte(`{"status":"active"}`)))
	put3 := mustEncode(kv.EncodePut([]byte("user/3"), []byte(`{"status":"inactive"}`)))
	applyExtEntries(t, s, []sm.Entry{
		{Index: 2, Cmd: put1},
		{Index: 3, Cmd: put2},
		{Index: 4, Cmd: put3},
	})

	active := indexQueryKeys(t, s, "by_status", "active")
	if len(active) != 2 || !containsKey(active, "user/1") || !containsKey(active, "user/2") {
		t.Fatalf("IndexQuery(by_status, active) = %v, want [user/1 user/2]", active)
	}
	inactive := indexQueryKeys(t, s, "by_status", "inactive")
	if len(inactive) != 1 || !containsKey(inactive, "user/3") {
		t.Fatalf("IndexQuery(by_status, inactive) = %v, want [user/3]", inactive)
	}
}

// TestPebbleStateMachine_Put_ChangingIndexedFieldMovesEntry pins that
// re-Putting a key under a DIFFERENT field value removes the OLD index
// entry, not just adds the new one -- otherwise a key would accumulate
// stale entries under every value it was ever stored with.
func TestPebbleStateMachine_Put_ChangingIndexedFieldMovesEntry(t *testing.T) {
	s := openExtendedStore(t)

	createCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_status", "status", protocol.IndexCreateOptions{}))
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: createCmd}})

	put1 := mustEncode(kv.EncodePut([]byte("user/1"), []byte(`{"status":"active"}`)))
	applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: put1}})

	if got := indexQueryKeys(t, s, "by_status", "active"); len(got) != 1 {
		t.Fatalf("IndexQuery(by_status, active) before update = %v, want [user/1]", got)
	}

	putUpdate := mustEncode(kv.EncodePut([]byte("user/1"), []byte(`{"status":"retired"}`)))
	applyExtEntries(t, s, []sm.Entry{{Index: 3, Cmd: putUpdate}})

	if got := indexQueryKeys(t, s, "by_status", "active"); len(got) != 0 {
		t.Fatalf("IndexQuery(by_status, active) after status changed away = %v, want []", got)
	}
	if got := indexQueryKeys(t, s, "by_status", "retired"); len(got) != 1 || !containsKey(got, "user/1") {
		t.Fatalf("IndexQuery(by_status, retired) after update = %v, want [user/1]", got)
	}
}

// TestPebbleStateMachine_Put_EnforcesUniqueIndex pins that a unique index
// actually REFUSES a conflicting write through the real apply path, the
// same enforcement index_cli_test.go already proves for the "mvcc" sm type
// (go-qrdb's CLI integration suite) but which the "pebble" sm type -- the
// DEFAULT -- never exercised through anything but a direct idx.Put call.
//
// The refusal MUST be a non-fatal application result (sm.Result.Data, nil
// error), not a Go error from Update: a Go error from the apply path is
// go-quicraft's signal that the state machine itself is faulty, and an
// ordinary, client-correctable unique-constraint rejection tripped its SM
// circuit breaker the first time this was wired as a plain error -- the
// whole shard then refused every later request, not just this one write.
func TestPebbleStateMachine_Put_EnforcesUniqueIndex(t *testing.T) {
	s := openExtendedStore(t)

	createCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_email", "email", protocol.IndexCreateOptions{Unique: true}))
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: createCmd}})

	put1 := mustEncode(kv.EncodePut([]byte("user/1"), []byte(`{"email":"a@example.com"}`)))
	applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: put1}})

	put2 := mustEncode(kv.EncodePut([]byte("user/2"), []byte(`{"email":"a@example.com"}`)))
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), []sm.Entry{{Index: 3, Cmd: put2}}, results)
	if err != nil {
		t.Fatalf("Update returned a Go error for an ordinary unique-constraint conflict: %v "+
			"(this must be a non-fatal application result, or it trips go-quicraft's SM circuit breaker)", err)
	}
	msg, ok := protocol.DecodeAppError(results[0].Data)
	if !ok {
		t.Fatalf("results[0].Data = %v, want an encoded AppErrorConflict for the unique-constraint refusal", results[0].Data)
	}
	if !strings.Contains(msg, "by_email") {
		t.Errorf("conflict message %q does not name the violated index by_email", msg)
	}

	// The apply loop must still be healthy: a later, unrelated write and
	// query succeed exactly as if the conflict had never happened.
	put3 := mustEncode(kv.EncodePut([]byte("user/3"), []byte(`{"email":"b@example.com"}`)))
	applyExtEntries(t, s, []sm.Entry{{Index: 4, Cmd: put3}})

	keys := indexQueryKeys(t, s, "by_email", "a@example.com")
	if len(keys) != 1 || !containsKey(keys, "user/1") {
		t.Fatalf("IndexQuery(by_email, a@example.com) after the refused write = %v, want exactly [user/1]", keys)
	}
	keys = indexQueryKeys(t, s, "by_email", "b@example.com")
	if len(keys) != 1 || !containsKey(keys, "user/3") {
		t.Fatalf("IndexQuery(by_email, b@example.com) after the conflict = %v, want exactly [user/3] "+
			"(the apply loop must still be healthy)", keys)
	}
}

// TestPebbleStateMachine_Delete_RemovesIndexEntry pins that deleting a key
// removes its entry from every index it was indexed under.
func TestPebbleStateMachine_Delete_RemovesIndexEntry(t *testing.T) {
	s := openExtendedStore(t)

	createCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_status", "status", protocol.IndexCreateOptions{}))
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: createCmd}})

	put1 := mustEncode(kv.EncodePut([]byte("user/1"), []byte(`{"status":"active"}`)))
	applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: put1}})
	if got := indexQueryKeys(t, s, "by_status", "active"); len(got) != 1 {
		t.Fatalf("IndexQuery(by_status, active) before delete = %v, want [user/1]", got)
	}

	delCmd := mustEncode(kv.EncodeDelete([]byte("user/1")))
	applyExtEntries(t, s, []sm.Entry{{Index: 3, Cmd: delCmd}})

	if got := indexQueryKeys(t, s, "by_status", "active"); len(got) != 0 {
		t.Fatalf("IndexQuery(by_status, active) after delete = %v, want []", got)
	}
}

// TestPebbleStateMachine_CompareAndSwap_MaintainsIndex pins that the guarded
// CompareAndSwap write path -- DAO's CompareAndSwap, SaveWithCondition's
// backing primitive -- maintains indexes exactly like a plain Put.
func TestPebbleStateMachine_CompareAndSwap_MaintainsIndex(t *testing.T) {
	s := openExtendedStore(t)

	createCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_status", "status", protocol.IndexCreateOptions{}))
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: createCmd}})

	put1 := mustEncode(kv.EncodePut([]byte("user/1"), []byte(`{"status":"active"}`)))
	applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: put1}})

	casCmd := mustEncode(protocol.EncodeCompareAndSwap(
		[]byte("user/1"), []byte(`{"status":"active"}`), []byte(`{"status":"retired"}`)))
	applyExtEntries(t, s, []sm.Entry{{Index: 3, Cmd: casCmd}})

	if got := indexQueryKeys(t, s, "by_status", "active"); len(got) != 0 {
		t.Fatalf("IndexQuery(by_status, active) after CompareAndSwap away from it = %v, want []", got)
	}
	if got := indexQueryKeys(t, s, "by_status", "retired"); len(got) != 1 || !containsKey(got, "user/1") {
		t.Fatalf("IndexQuery(by_status, retired) after CompareAndSwap = %v, want [user/1]", got)
	}
}

// TestPebbleStateMachine_IndexCreate_BackfillsExistingData pins that
// creating an index over data written BEFORE the index existed backfills
// it, rather than only indexing writes from that point forward. Before this
// fix pebbleHandleIndexCreate never backfilled at all.
func TestPebbleStateMachine_IndexCreate_BackfillsExistingData(t *testing.T) {
	s := openExtendedStore(t)

	put1 := mustEncode(kv.EncodePut([]byte("user/1"), []byte(`{"status":"active"}`)))
	put2 := mustEncode(kv.EncodePut([]byte("user/2"), []byte(`{"status":"inactive"}`)))
	applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: put1},
		{Index: 2, Cmd: put2},
	})

	createCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_status", "status", protocol.IndexCreateOptions{}))
	applyExtEntries(t, s, []sm.Entry{{Index: 3, Cmd: createCmd}})

	if got := indexQueryKeys(t, s, "by_status", "active"); len(got) != 1 || !containsKey(got, "user/1") {
		t.Fatalf("IndexQuery(by_status, active) after backfill = %v, want [user/1]", got)
	}
	if got := indexQueryKeys(t, s, "by_status", "inactive"); len(got) != 1 || !containsKey(got, "user/2") {
		t.Fatalf("IndexQuery(by_status, inactive) after backfill = %v, want [user/2]", got)
	}
}

// requireIndexConflict fails the test unless the single result is the
// non-fatal unique-constraint refusal naming indexName.
func requireIndexConflict(t *testing.T, res sm.Result, indexName string) {
	t.Helper()
	msg, ok := protocol.DecodeAppError(res.Data)
	if !ok {
		t.Fatalf("result Data = %v, want an encoded AppErrorConflict for index %s", res.Data, indexName)
	}
	if !strings.Contains(msg, indexName) {
		t.Errorf("conflict message %q does not name the violated index %s", msg, indexName)
	}
}

// TestPebbleStateMachine_PutIfAbsent_EnforcesUniqueIndex pins that the
// PutIfAbsent write path populates a unique index and refuses a second key
// carrying the same value as a non-fatal application result, leaving the
// refused key unwritten.
func TestPebbleStateMachine_PutIfAbsent_EnforcesUniqueIndex(t *testing.T) {
	s := openExtendedStore(t)

	createCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_email", "email", protocol.IndexCreateOptions{Unique: true}))
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: createCmd}})

	first := mustEncode(kv.EncodePutIfAbsent([]byte("user/1"), []byte(`{"email":"a@example.com"}`)))
	res := applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: first}})
	if res[0].Value != kv.ResultStored {
		t.Fatalf("first PutIfAbsent Value = %d, want ResultStored", res[0].Value)
	}

	second := mustEncode(kv.EncodePutIfAbsent([]byte("user/2"), []byte(`{"email":"a@example.com"}`)))
	res = applyExtEntries(t, s, []sm.Entry{{Index: 3, Cmd: second}})
	requireIndexConflict(t, res[0], "by_email")

	if got := indexQueryKeys(t, s, "by_email", "a@example.com"); len(got) != 1 || !containsKey(got, "user/1") {
		t.Fatalf("IndexQuery(by_email, a@example.com) = %v, want exactly [user/1]", got)
	}
	if _, err := s.Lookup(context.Background(), []byte("user/2")); err == nil {
		t.Fatal("user/2 was readable after its PutIfAbsent was refused")
	}
}

// TestPebbleStateMachine_CompareAndSwap_EnforcesUniqueIndex pins that a swap
// onto a value another key already holds in a unique index is refused as a
// non-fatal application result and leaves both the key and the index as they
// were.
func TestPebbleStateMachine_CompareAndSwap_EnforcesUniqueIndex(t *testing.T) {
	s := openExtendedStore(t)

	createCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_email", "email", protocol.IndexCreateOptions{Unique: true}))
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: createCmd}})
	applyExtEntries(t, s, []sm.Entry{
		{Index: 2, Cmd: mustEncode(kv.EncodePut([]byte("user/1"), []byte(`{"email":"a@example.com"}`)))},
		{Index: 3, Cmd: mustEncode(kv.EncodePut([]byte("user/2"), []byte(`{"email":"b@example.com"}`)))},
	})

	casCmd := mustEncode(protocol.EncodeCompareAndSwap(
		[]byte("user/2"), []byte(`{"email":"b@example.com"}`), []byte(`{"email":"a@example.com"}`)))
	res := applyExtEntries(t, s, []sm.Entry{{Index: 4, Cmd: casCmd}})
	requireIndexConflict(t, res[0], "by_email")

	if got := indexQueryKeys(t, s, "by_email", "a@example.com"); len(got) != 1 || !containsKey(got, "user/1") {
		t.Fatalf("IndexQuery(by_email, a@example.com) = %v, want exactly [user/1]", got)
	}
	if got := indexQueryKeys(t, s, "by_email", "b@example.com"); len(got) != 1 || !containsKey(got, "user/2") {
		t.Fatalf("IndexQuery(by_email, b@example.com) = %v, want exactly [user/2]", got)
	}
}

// TestPebbleStateMachine_CompareAndDelete_RemovesIndexEntry pins that the
// guarded delete removes the key's index entry, while a delete whose expected
// value does not match leaves both the key and its entry in place.
func TestPebbleStateMachine_CompareAndDelete_RemovesIndexEntry(t *testing.T) {
	s := openExtendedStore(t)

	createCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_status", "status", protocol.IndexCreateOptions{}))
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: createCmd}})
	applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: mustEncode(kv.EncodePut([]byte("user/1"), []byte(`{"status":"active"}`)))}})

	mismatch := mustEncode(protocol.EncodeCompareAndDelete([]byte("user/1"), []byte(`{"status":"other"}`)))
	res := applyExtEntries(t, s, []sm.Entry{{Index: 3, Cmd: mismatch}})
	if res[0].Value != protocol.ResultNotDeleted {
		t.Fatalf("mismatched CompareAndDelete Value = %d, want ResultNotDeleted", res[0].Value)
	}
	if got := indexQueryKeys(t, s, "by_status", "active"); len(got) != 1 {
		t.Fatalf("IndexQuery(by_status, active) after a refused delete = %v, want [user/1]", got)
	}

	match := mustEncode(protocol.EncodeCompareAndDelete([]byte("user/1"), []byte(`{"status":"active"}`)))
	res = applyExtEntries(t, s, []sm.Entry{{Index: 4, Cmd: match}})
	if res[0].Value != protocol.ResultDeleted {
		t.Fatalf("CompareAndDelete Value = %d, want ResultDeleted", res[0].Value)
	}
	if got := indexQueryKeys(t, s, "by_status", "active"); len(got) != 0 {
		t.Fatalf("IndexQuery(by_status, active) after CompareAndDelete = %v, want []", got)
	}
}

// TestPebbleStateMachine_Batch_MaintainsIndex pins that the ops of a Batch
// maintain registered indexes exactly as single writes do: a put adds its
// entry, a put over an existing key moves it, and a delete removes it.
func TestPebbleStateMachine_Batch_MaintainsIndex(t *testing.T) {
	s := openExtendedStore(t)

	createCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_status", "status", protocol.IndexCreateOptions{}))
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: createCmd}})
	applyExtEntries(t, s, []sm.Entry{
		{Index: 2, Cmd: mustEncode(kv.EncodePut([]byte("user/1"), []byte(`{"status":"active"}`)))},
		{Index: 3, Cmd: mustEncode(kv.EncodePut([]byte("user/2"), []byte(`{"status":"active"}`)))},
	})

	batch := mustEncode(protocol.EncodeBatch([][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("user/1"), []byte(`{"status":"retired"}`), nil, 0)),
		mustEncode(protocol.EncodeOp(protocol.OpMVCCDelete, []byte("user/2"), nil, nil, 0)),
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("user/3"), []byte(`{"status":"active"}`), nil, 0)),
	}))
	res := applyExtEntries(t, s, []sm.Entry{{Index: 4, Cmd: batch}})
	if res[0].Value != 3 {
		t.Fatalf("batch Value = %d, want 3 ops applied", res[0].Value)
	}

	if got := indexQueryKeys(t, s, "by_status", "active"); len(got) != 1 || !containsKey(got, "user/3") {
		t.Fatalf("IndexQuery(by_status, active) after batch = %v, want [user/3]", got)
	}
	if got := indexQueryKeys(t, s, "by_status", "retired"); len(got) != 1 || !containsKey(got, "user/1") {
		t.Fatalf("IndexQuery(by_status, retired) after batch = %v, want [user/1]", got)
	}
}

// TestPebbleStateMachine_Batch_UniqueConflictLeavesIndexUntouched pins that a
// Batch op violating a unique index fails the apply -- a batch has no per-op
// result slot to carry a conflict -- and mutates neither the index nor the
// data.
func TestPebbleStateMachine_Batch_UniqueConflictLeavesIndexUntouched(t *testing.T) {
	s := openExtendedStore(t)

	createCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_email", "email", protocol.IndexCreateOptions{Unique: true}))
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: createCmd}})
	applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: mustEncode(kv.EncodePut([]byte("user/1"), []byte(`{"email":"a@example.com"}`)))}})

	batch := mustEncode(protocol.EncodeBatch([][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("user/2"), []byte(`{"email":"a@example.com"}`), nil, 0)),
	}))
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 3, Cmd: batch}}, results); err == nil {
		t.Fatal("Update accepted a batch that violates a unique index, want an apply error")
	}

	if got := indexQueryKeys(t, s, "by_email", "a@example.com"); len(got) != 1 || !containsKey(got, "user/1") {
		t.Fatalf("IndexQuery(by_email, a@example.com) after the refused batch = %v, want exactly [user/1]", got)
	}
	if _, err := s.Lookup(context.Background(), []byte("user/2")); err == nil {
		t.Fatal("user/2 was readable after its batch was refused")
	}
}

// TestPebbleStateMachine_ScopedIndex_IgnoresKeysOutsideScope pins that an
// index declared for one key prefix is maintained -- on write, on delete and
// by the creation backfill -- only for keys under that prefix.
func TestPebbleStateMachine_ScopedIndex_IgnoresKeysOutsideScope(t *testing.T) {
	s := openExtendedStore(t)

	// Written before the index exists, so the backfill has to apply the scope.
	applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: mustEncode(kv.EncodePut([]byte("orders/1"), []byte(`{"status":"active"}`)))},
		{Index: 2, Cmd: mustEncode(kv.EncodePut([]byte("users/1"), []byte(`{"status":"active"}`)))},
	})
	createCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_status", "status", protocol.IndexCreateOptions{KeyPrefix: "orders/"}))
	applyExtEntries(t, s, []sm.Entry{{Index: 3, Cmd: createCmd}})

	if got := indexQueryKeys(t, s, "by_status", "active"); len(got) != 1 || !containsKey(got, "orders/1") {
		t.Fatalf("IndexQuery(by_status, active) after scoped backfill = %v, want [orders/1]", got)
	}

	applyExtEntries(t, s, []sm.Entry{
		{Index: 4, Cmd: mustEncode(kv.EncodePut([]byte("orders/2"), []byte(`{"status":"active"}`)))},
		{Index: 5, Cmd: mustEncode(kv.EncodePut([]byte("users/2"), []byte(`{"status":"active"}`)))},
	})
	if got := indexQueryKeys(t, s, "by_status", "active"); len(got) != 2 || containsKey(got, "users/2") {
		t.Fatalf("IndexQuery(by_status, active) after writes = %v, want [orders/1 orders/2]", got)
	}

	applyExtEntries(t, s, []sm.Entry{
		{Index: 6, Cmd: mustEncode(kv.EncodeDelete([]byte("users/1")))},
		{Index: 7, Cmd: mustEncode(kv.EncodeDelete([]byte("orders/1")))},
	})
	if got := indexQueryKeys(t, s, "by_status", "active"); len(got) != 1 || !containsKey(got, "orders/2") {
		t.Fatalf("IndexQuery(by_status, active) after deletes = %v, want [orders/2]", got)
	}
}

// TestPebbleStateMachine_IndexCreate_BackfillSkipsUnindexableValues pins that
// the creation backfill passes over values it cannot index -- not JSON, empty,
// or lacking the indexed field -- and still indexes the rest.
func TestPebbleStateMachine_IndexCreate_BackfillSkipsUnindexableValues(t *testing.T) {
	s := openExtendedStore(t)

	applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: mustEncode(kv.EncodePut([]byte("doc/plain"), []byte("not json")))},
		{Index: 2, Cmd: mustEncode(kv.EncodePut([]byte("doc/empty"), []byte{}))},
		{Index: 3, Cmd: mustEncode(kv.EncodePut([]byte("doc/other"), []byte(`{"name":"x"}`)))},
		{Index: 4, Cmd: mustEncode(kv.EncodePut([]byte("doc/ok"), []byte(`{"status":"active"}`)))},
	})
	createCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_status", "status", protocol.IndexCreateOptions{}))
	applyExtEntries(t, s, []sm.Entry{{Index: 5, Cmd: createCmd}})

	if got := indexQueryKeys(t, s, "by_status", "active"); len(got) != 1 || !containsKey(got, "doc/ok") {
		t.Fatalf("IndexQuery(by_status, active) = %v, want [doc/ok]", got)
	}
}

// TestPebbleStateMachine_NonJSONValues_BypassIndexMaintenance pins that
// writing and deleting a value that is not a JSON object succeeds while an
// index is registered: such a value has nothing to index and must not fail
// the apply.
func TestPebbleStateMachine_NonJSONValues_BypassIndexMaintenance(t *testing.T) {
	s := openExtendedStore(t)

	createCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_status", "status", protocol.IndexCreateOptions{}))
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: createCmd}})

	applyExtEntries(t, s, []sm.Entry{
		{Index: 2, Cmd: mustEncode(kv.EncodePut([]byte("blob/1"), []byte("not json")))},
		{Index: 3, Cmd: mustEncode(kv.EncodeDelete([]byte("blob/1")))},
	})
	if got := indexQueryKeys(t, s, "by_status", "not json"); len(got) != 0 {
		t.Fatalf("IndexQuery(by_status, \"not json\") = %v, want []", got)
	}
}

// TestPebbleBackfillRange pins the scan bounds the backfill uses: the whole
// user keyspace for an empty scope, the prefix range for a normal scope, and
// an open-ended 0xFF bound when the scope has no finite upper bound.
func TestPebbleBackfillRange(t *testing.T) {
	tests := []struct {
		name      string
		scope     []byte
		wantStart []byte
		wantEnd   []byte
	}{
		{"empty scope spans the keyspace", nil, []byte{0x00}, []byte{0xFF}},
		{"prefix scope ends at its successor", []byte("orders/"), []byte("orders/"), []byte("orders0")},
		{"all-0xFF scope has no successor", []byte{0xFF, 0xFF}, []byte{0xFF, 0xFF}, []byte{0xFF}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end := pebbleBackfillRange(tt.scope)
			if !bytes.Equal(start, tt.wantStart) || !bytes.Equal(end, tt.wantEnd) {
				t.Fatalf("pebbleBackfillRange(%q) = (%q, %q), want (%q, %q)",
					tt.scope, start, end, tt.wantStart, tt.wantEnd)
			}
		})
	}
}
