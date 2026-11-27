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
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// TestEngineConformance is Lane 1A's acceptance criterion 1 (docs/databases/
// engineering.md, "Target: phased implementation plan"): the SAME scenario --
// KV, MVCC, transactions, CAS, leases, watches, secondary indexes,
// collections, a relational smoke test, and time series -- run unmodified
// against one MVCCEngine over each of memory, file, pebble and
// local-provider objstore (vectorTestBackends, vector_handlers_test.go).
//
// A scenario that only ran against one backend would not catch a handler
// that silently depended on that backend's particular Get/Set/iterator
// semantics -- which is exactly how this lane's audited defect stayed
// invisible: handleTSWrite discarded the apply batch and only worked at all
// because pebble was the one backend time series had ever been wired to.
//
// The relational-smoke-test leg is deliberately scoped to the engine layer:
// a true CREATE TABLE/INSERT/SELECT round trip lives in pkg/sql, gated
// behind the qrdb_sql build tag and the full database/service/catalog stack,
// which is out of reach from a bare storage.Storage + MVCCEngine. What this
// suite checks instead is the same row-shaped-key write/read/index path
// pkg/sql's table storage is ultimately built on: a row key under a table
// namespace, written through the apply batch, found again by exact key and
// through a secondary index over one of its columns.
func TestEngineConformance(t *testing.T) {
	for _, b := range vectorTestBackends() {
		t.Run(b.name, func(t *testing.T) {
			stg := b.new(t)
			t.Cleanup(func() { _ = stg.Close() })

			hub := watch.NewHub()
			t.Cleanup(hub.Close)
			lr := lease.NewLessor()
			ts, err := timeseries.New(stg)
			if err != nil {
				t.Fatalf("timeseries.New: %v", err)
			}

			eng, err := NewMVCCEngine(stg,
				WithMVCCWatchHub(hub),
				WithMVCCLessor(lr),
				WithMVCCTimeSeries(ts),
			)
			if err != nil {
				t.Fatalf("NewMVCCEngine: %v", err)
			}
			t.Cleanup(func() { _ = eng.Close(context.Background()) })

			// ran records the scenarios that executed and passed (a -run
			// filter skips some), so remote_sstables re-reads only their
			// state after its Flush.
			ran := map[string]bool{}
			run := func(name string, f func(t *testing.T)) {
				ran[name] = t.Run(name, func(t *testing.T) {
					ran[name] = true
					f(t)
				}) && ran[name]
			}
			run("kv", func(t *testing.T) { conformanceKV(t, eng) })
			run("mvcc", func(t *testing.T) { conformanceMVCC(t, eng) })
			run("compare_and_swap", func(t *testing.T) { conformanceCompareAndSwap(t, eng) })
			run("compare_and_delete", func(t *testing.T) { conformanceCompareAndDelete(t, eng) })
			run("transaction", func(t *testing.T) { conformanceTransaction(t, eng) })
			run("secondary_index", func(t *testing.T) { conformanceSecondaryIndex(t, eng) })
			run("collections", func(t *testing.T) { conformanceCollections(t, eng) })
			run("relational_smoke", func(t *testing.T) { conformanceRelationalSmoke(t, eng) })
			run("leases", func(t *testing.T) { conformanceLeases(t, eng) })
			run("watches", func(t *testing.T) { conformanceWatch(t, eng, hub) })
			run("time_series", func(t *testing.T) { conformanceTimeSeries(t, eng) })
			if fl, ok := stg.(conformanceFlusher); ok {
				t.Run("remote_sstables", func(t *testing.T) { conformanceRemoteSSTables(t, eng, stg, fl, ran) })
			}
		})
	}
}

// conformanceFlusher is a storage that can flush its memtable to SSTables.
type conformanceFlusher interface {
	Flush() error
}

// conformanceRemoteLister is a storage whose SSTables live in an object
// store it can list (the object-storage-lsm composition).
type conformanceRemoteLister interface {
	RemoteObjects(prefix string) ([]string, error)
}

// conformanceRemoteSSTables runs after every other scenario on a storage
// that flushes. It writes KV keys, a transaction and an indexed row, flushes
// the memtable, and, when the SSTables live in an object store, requires at
// least one object under its "lsm/" prefix. It then reads back what it wrote
// and what the earlier scenarios wrote, iterates in key order and queries the
// secondary indexes, so every read is served from flushed SSTables (remote
// ones on the object-storage-lsm composition) and not from the memtable.
func conformanceRemoteSSTables(t *testing.T, eng *MVCCEngine, stg storage.Storage, fl conformanceFlusher, ran map[string]bool) {
	ctx := context.Background()
	applyMVCCEntries(t, eng, []sm.Entry{
		kvPutEntry(1000, "conf:remote:kv", "remote-value"),
		mvccPutEntry(1001, []byte("conf:remote:range:a"), []byte("a"), 0),
		mvccPutEntry(1002, []byte("conf:remote:range:b"), []byte("b"), 0),
		mvccPutEntry(1003, []byte("conf:remote:range:c"), []byte("c"), 0),
		kvPutEntry(1004, "conf:remote:txn:guard", "expected"),
	})
	applyMVCCEntries(t, eng, []sm.Entry{
		guardedTxnEntry(1005, "conf:remote:txn:guard", "expected", "conf:remote:txn:success", "conf:remote:txn:failure"),
	})
	applyMVCCEntries(t, eng, []sm.Entry{
		indexCreateEntry(t, 1006, "conf_remote_by_owner", "owner", protocol.IndexCreateOptions{}),
	})
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1007, []byte("conf:remote:row:1"), []byte(`{"owner":"remote@example.com"}`), 0),
	})

	if err := fl.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if rl, ok := stg.(conformanceRemoteLister); ok {
		objects, err := rl.RemoteObjects(objstoreLSMTestPrefix + "/")
		if err != nil {
			t.Fatalf("list %s/: %v", objstoreLSMTestPrefix, err)
		}
		if len(objects) == 0 {
			t.Fatalf("no object under %s/ after Flush: the SSTables were not created on the object store", objstoreLSMTestPrefix)
		}
	}

	want := map[string]string{
		"conf:remote:kv":          "remote-value",
		"conf:remote:range:b":     "b",
		"conf:remote:txn:success": "",
	}
	if ran["kv"] {
		want["conf:kv:a"] = "1"
	}
	if ran["transaction"] {
		want["conf:txn:success"] = ""
	}
	for key, want := range want {
		val, err := eng.Lookup(ctx, key)
		if err != nil {
			t.Fatalf("Lookup(%q) after Flush: %v", key, err)
		}
		got, _ := val.([]byte)
		if got == nil {
			t.Fatalf("Lookup(%q) after Flush: key missing", key)
		}
		if want != "" && string(got) != want {
			t.Fatalf("Lookup(%q) after Flush = %q, want %q", key, got, want)
		}
	}
	if val, _ := eng.Lookup(ctx, "conf:remote:txn:failure"); val != nil {
		t.Error("transaction with a matching guard ran its failure branch after Flush")
	}

	got, err := eng.Lookup(ctx, &statemachine.PrefixQuery{Prefix: []byte("conf:remote:range:"), Limit: 2})
	if err != nil {
		t.Fatalf("PrefixQuery after Flush: %v", err)
	}
	firstTwo := got.(map[string][]byte)
	if len(firstTwo) != 2 || string(firstTwo["conf:remote:range:a"]) != "a" || string(firstTwo["conf:remote:range:b"]) != "b" {
		t.Fatalf("PrefixQuery limit 2 after Flush = %q, want the first two keys in order (a, b)", firstTwo)
	}

	it := stg.NewIterator(&storage.IteratorOptions{})
	var prev []byte
	n := 0
	for ok := it.SeekToFirst(); ok; ok = it.Next() {
		key := it.Key()
		if prev != nil && bytes.Compare(prev, key) >= 0 {
			t.Fatalf("storage iterator after Flush out of order: %q then %q", prev, key)
		}
		prev = append(prev[:0], key...)
		n++
	}
	if err := it.Error(); err != nil {
		t.Fatalf("storage iterator after Flush: %v", err)
	}
	_ = it.Close()
	if n == 0 {
		t.Fatal("storage iterator after Flush saw no keys")
	}

	keys := indexQueryKeys(t, eng, "conf_remote_by_owner", []byte("remote@example.com"))
	if len(keys) != 1 || !bytes.Equal(keys[0], []byte("conf:remote:row:1")) {
		t.Fatalf("index query after Flush = %q, want exactly conf:remote:row:1", keys)
	}
	if ran["secondary_index"] {
		keys = indexQueryKeys(t, eng, "conf_by_email", []byte("a@example.com"))
		if len(keys) != 1 || !bytes.Equal(keys[0], []byte("conf:idx:1")) {
			t.Fatalf("secondary_index scenario's index query after Flush = %q, want exactly conf:idx:1", keys)
		}
	}
}

// conformanceKV exercises the base KV protocol (kv.OpPut), independent of
// MVCC revisioning.
func conformanceKV(t *testing.T, eng *MVCCEngine) {
	applyMVCCEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "conf:kv:a", "1"),
	})
	val, err := eng.Lookup(context.Background(), "conf:kv:a")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("1")) {
		t.Fatalf("Lookup = %q, want %q", val, "1")
	}
}

// conformanceMVCC exercises an MVCC put, a revisioned read, and a delete.
func conformanceMVCC(t *testing.T, eng *MVCCEngine) {
	key := []byte("conf:mvcc:a")
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(10, key, []byte("v1"), 0),
	})
	got, err := eng.Lookup(context.Background(), service.MVCCGetQuery{Key: key})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	kv := got.(*service.KeyValue)
	if !bytes.Equal(kv.Value, []byte("v1")) {
		t.Fatalf("Lookup = %q, want %q", kv.Value, "v1")
	}

	applyMVCCEntries(t, eng, []sm.Entry{mvccDeleteEntry(11, key)})
	if _, err := eng.Lookup(context.Background(), service.MVCCGetQuery{Key: key}); err == nil {
		t.Fatal("Lookup after delete: expected an error, got none")
	}
}

// conformanceCompareAndSwap exercises a guarded swap over the apply batch.
func conformanceCompareAndSwap(t *testing.T, eng *MVCCEngine) {
	key := "conf:cas:a"
	applyMVCCEntries(t, eng, []sm.Entry{kvPutEntry(20, key, "before")})

	results := applyMVCCEntries(t, eng, []sm.Entry{
		mvccCompareAndSwapEntry(21, key, "before", "after"),
	})
	if results[0].Value != protocol.ResultSwapped {
		t.Fatalf("CompareAndSwap result = %d, want ResultSwapped", results[0].Value)
	}
	val, err := eng.Lookup(context.Background(), key)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("after")) {
		t.Fatalf("Lookup after swap = %q, want %q", val, "after")
	}
}

// conformanceCompareAndDelete exercises a guarded delete over the apply batch.
func conformanceCompareAndDelete(t *testing.T, eng *MVCCEngine) {
	key := "conf:cad:a"
	applyMVCCEntries(t, eng, []sm.Entry{kvPutEntry(30, key, "gone-soon")})

	results := applyMVCCEntries(t, eng, []sm.Entry{
		mvccCompareAndDeleteEntry(31, key, "gone-soon"),
	})
	if results[0].Value != protocol.ResultDeleted {
		t.Fatalf("CompareAndDelete result = %d, want ResultDeleted", results[0].Value)
	}
	val, err := eng.Lookup(context.Background(), key)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if val != nil {
		t.Fatalf("Lookup after guarded delete = %q, want nil", val)
	}
}

// conformanceTransaction exercises a compare-and-branch transaction.
func conformanceTransaction(t *testing.T, eng *MVCCEngine) {
	applyMVCCEntries(t, eng, []sm.Entry{kvPutEntry(40, "conf:txn:guard", "expected")})

	applyMVCCEntries(t, eng, []sm.Entry{
		guardedTxnEntry(41, "conf:txn:guard", "expected", "conf:txn:success", "conf:txn:failure"),
	})

	if val, _ := eng.Lookup(context.Background(), "conf:txn:success"); val == nil {
		t.Error("transaction with a matching guard did not run its success branch")
	}
	if val, _ := eng.Lookup(context.Background(), "conf:txn:failure"); val != nil {
		t.Error("transaction with a matching guard ran its failure branch too")
	}
}

// conformanceSecondaryIndex exercises index creation, a unique constraint,
// and a query -- the storage-backed index path this lane makes unconditional
// (index.NewStoragePebbleIndex over every backend, not just pebble).
func conformanceSecondaryIndex(t *testing.T, eng *MVCCEngine) {
	applyMVCCEntries(t, eng, []sm.Entry{
		indexCreateEntry(t, 50, "conf_by_email", "email", protocol.IndexCreateOptions{Unique: true}),
	})
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(51, []byte("conf:idx:1"), []byte(`{"email":"a@example.com"}`), 0),
	})

	keys := indexQueryKeys(t, eng, "conf_by_email", []byte("a@example.com"))
	if len(keys) != 1 || !bytes.Equal(keys[0], []byte("conf:idx:1")) {
		t.Fatalf("index query = %q, want exactly conf:idx:1", keys)
	}

	results := applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(52, []byte("conf:idx:2"), []byte(`{"email":"a@example.com"}`), 0),
	})
	if _, refused := protocol.DecodeAppError(results[0].Data); !refused {
		t.Fatal("a second document claiming the same unique email was not refused")
	}
}

// conformanceCollections exercises a collection-shaped document key
// alongside a secondary index over it, the same raw mechanism
// orphaned_collection_index_test.go already proves against MVCCEngine.
func conformanceCollections(t *testing.T, eng *MVCCEngine) {
	docKey := []byte("__col:conf:items/1")
	applyMVCCEntries(t, eng, []sm.Entry{
		indexCreateEntry(t, 60, "conf.items.sku", "sku", protocol.IndexCreateOptions{}),
		mvccPutEntry(61, docKey, []byte(`{"sku":"widget-1"}`), 0),
	})

	keys := indexQueryKeys(t, eng, "conf.items.sku", []byte("widget-1"))
	if len(keys) != 1 || !bytes.Equal(keys[0], docKey) {
		t.Fatalf("collection index query = %q, want exactly %q", keys, docKey)
	}

	applyMVCCEntries(t, eng, []sm.Entry{mvccDeleteEntry(62, docKey)})
	if keys := indexQueryKeys(t, eng, "conf.items.sku", []byte("widget-1")); len(keys) != 0 {
		t.Fatalf("deleted document still reachable through its collection index: %q", keys)
	}
}

// conformanceRelationalSmoke exercises a row-shaped key under a table
// namespace, written through the apply batch and found again by exact key
// and through a secondary index over one of its columns -- the mechanism
// pkg/sql's table storage is built on (see the TestEngineConformance doc
// comment for why a full SQL round trip is not exercised here).
func conformanceRelationalSmoke(t *testing.T, eng *MVCCEngine) {
	rowKey := []byte("__tbl:conf:users/1")
	applyMVCCEntries(t, eng, []sm.Entry{
		indexCreateEntry(t, 70, "conf.users.name", "name", protocol.IndexCreateOptions{}),
		mvccPutEntry(71, rowKey, []byte(`{"name":"alice","age":"30"}`), 0),
	})

	got, err := eng.Lookup(context.Background(), service.MVCCGetQuery{Key: rowKey})
	if err != nil {
		t.Fatalf("Lookup row by key: %v", err)
	}
	if !bytes.Contains(got.(*service.KeyValue).Value, []byte("alice")) {
		t.Fatalf("row value = %q, want it to contain %q", got.(*service.KeyValue).Value, "alice")
	}

	keys := indexQueryKeys(t, eng, "conf.users.name", []byte("alice"))
	if len(keys) != 1 || !bytes.Equal(keys[0], rowKey) {
		t.Fatalf("row index query = %q, want exactly %q", keys, rowKey)
	}
}

// conformanceLeases exercises a lease grant, a lease-bound put, and a revoke
// that retracts the bound key -- mirroring
// TestMVCCEngine_LeaseRevoke_RetractsIndexes (mvcc_engine_lease_test.go).
func conformanceLeases(t *testing.T, eng *MVCCEngine) {
	grantCmd := mustEncode(protocol.EncodeLeaseGrant(60))
	leaseID := int64(applyLeaseCmd(t, eng, 80, grantCmd).Value)
	if leaseID == 0 {
		t.Fatal("LeaseGrant returned lease ID 0")
	}

	applyLeaseCmd(t, eng, 81, mustEncode(protocol.EncodeMVCCPut([]byte("conf:lease:a"), []byte("v"), leaseID)))
	if val, err := eng.Lookup(context.Background(), "conf:lease:a"); err != nil || val == nil {
		t.Fatalf("lease-bound key not readable after put: val=%v err=%v", val, err)
	}

	applyLeaseCmd(t, eng, 82, mustEncode(protocol.EncodeLeaseRevoke(leaseID)))
	if val, _ := eng.Lookup(context.Background(), "conf:lease:a"); val != nil {
		t.Fatalf("lease-bound key survived LeaseRevoke: %q", val)
	}
}

// conformanceWatch exercises a watch notification delivered after the apply
// batch commits, mirroring TestMVCCEngine_WatchNotification_Put.
func conformanceWatch(t *testing.T, eng *MVCCEngine, hub *watch.Hub) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := hub.Watch(ctx, "conf:watch:a")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	applyMVCCEntries(t, eng, []sm.Entry{mvccPutEntry(90, []byte("conf:watch:a"), []byte("v"), 0)})

	select {
	case resp := <-w.Events():
		if len(resp.Events) == 0 {
			t.Fatal("expected at least 1 event")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for watch event")
	}
}

// conformanceTimeSeries exercises a time-series write through the apply
// batch (this lane's audited fix) and a query read back through the
// Storage's own storage.Reader.
func conformanceTimeSeries(t *testing.T, eng *MVCCEngine) {
	now := time.Now().UnixNano()
	tsCmd := mustEncode(protocol.EncodeTSWrite("conf.metric", now, 42.5, map[string]string{"host": "a"}))
	results := applyMVCCEntries(t, eng, []sm.Entry{{Index: 100, Cmd: tsCmd}})
	if results[0].Value != 1 {
		t.Fatalf("TS write result = %d, want 1", results[0].Value)
	}

	start := time.Unix(0, now).Add(-time.Second)
	end := time.Unix(0, now).Add(time.Second)
	got, err := eng.Lookup(context.Background(), service.TSQueryRequest{
		Metric: "conf.metric",
		Start:  start,
		End:    end,
	})
	if err != nil {
		t.Fatalf("TS query: %v", err)
	}
	points := got.([]timeseries.DataPoint)
	if len(points) != 1 || points[0].Value != 42.5 {
		t.Fatalf("TS query = %+v, want exactly one point with value 42.5", points)
	}
}
