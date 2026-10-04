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
	"context"
	"sort"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// ---------------------------------------------------------------------------
// Secondary-index key scoping on the Raft apply path (go-qrdb#410)
//
// These tests state the property the apply path must hold: an index maintains
// entries ONLY for keys inside its own key scope. The scope is composed of
// the namespace carried by the index NAME -- "<tenant>/<database>/", exactly
// what the transport registries prefix both keys and index names with -- and
// the relative key prefix its creator declared (a collection's document
// prefix, a DAO entity type).
//
// Before the fix every one of these indexed every JSON value written anywhere
// in the state machine.
// ---------------------------------------------------------------------------

// applyOne applies a single command to the engine and fails the test on a
// Raft-fatal error.
func applyOne(t *testing.T, eng *MVCCEngine, index uint64, cmd []byte) sm.Result {
	t.Helper()
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: index, Cmd: cmd}}, results); err != nil {
		t.Fatalf("Update(entry %d): %v", index, err)
	}
	return results[0]
}

// applyMVCCPut applies an MVCC put through the apply path.
func applyMVCCPut(t *testing.T, eng *MVCCEngine, index uint64, key, value string) sm.Result {
	t.Helper()
	cmd := mustEncode(protocol.EncodeMVCCPut([]byte(key), []byte(value), 0))
	return applyOne(t, eng, index, cmd)
}

// indexedKeys returns the sorted entity keys held by the named index.
func indexedKeys(t *testing.T, eng *MVCCEngine, name string) []string {
	t.Helper()
	eng.indexMu.RLock()
	idx, ok := eng.indexes[name]
	eng.indexMu.RUnlock()
	if !ok {
		t.Fatalf("index %q not registered", name)
	}
	keys, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll(%s): %v", name, err)
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, string(k))
	}
	sort.Strings(out)
	return out
}

// assertIndexed asserts the exact set of keys an index holds.
func assertIndexed(t *testing.T, eng *MVCCEngine, name string, want ...string) {
	t.Helper()
	got := indexedKeys(t, eng, name)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("index %q holds %v; want %v", name, got, want)
	}
	for i := range got {
		// #nosec G602 -- the lengths are compared equal above, and i ranges over got.
		if got[i] != want[i] {
			t.Fatalf("index %q holds %v; want %v", name, got, want)
		}
	}
}

// newScopedEngine builds an MVCCEngine over memory storage.
func newScopedEngine(t *testing.T) *MVCCEngine {
	t.Helper()
	stg := memory.NewStorage()
	t.Cleanup(func() { _ = stg.Close() })
	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close(context.Background()) })
	return eng
}

// TestMVCCEngine_IndexDoesNotSpanTenants is the security statement. Under
// prefix tenant isolation every tenant shares one state machine and is
// separated only by the "<tenant>/<database>/" key prefix that
// tenant.ComposeNamespace composes; the transport registries prefix index
// names with the SAME string. An index belonging to tenant-a must therefore
// never hold a key belonging to tenant-b, because IndexQuery returns those
// keys to the caller.
func TestMVCCEngine_IndexDoesNotSpanTenants(t *testing.T) {
	eng := newScopedEngine(t)

	// The name a namespaced index service composes for tenant-a's "sales"
	// database: tenant.ComposeNamespace("tenant-a", "sales") + "users.email".
	const idxName = "tenant-a/sales/users.email"
	applyOne(t, eng, 1, mustEncode(protocol.EncodeIndexCreateOpts(idxName, "email", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false})))

	applyMVCCPut(t, eng, 2, "tenant-a/sales/__col:sales:users/1", `{"email":"a@example.com"}`)
	applyMVCCPut(t, eng, 3, "tenant-b/sales/__col:sales:users/2", `{"email":"b@example.com"}`)

	assertIndexed(t, eng, idxName, "tenant-a/sales/__col:sales:users/1")
}

// TestMVCCEngine_IndexQueryDoesNotReturnAnotherTenantsKeys is the same
// boundary observed through the query surface a caller actually reaches:
// IndexQuery by value. A tenant that knows another tenant's indexed VALUE
// must not learn that tenant's key names from its own index.
func TestMVCCEngine_IndexQueryDoesNotReturnAnotherTenantsKeys(t *testing.T) {
	eng := newScopedEngine(t)

	const idxName = "tenant-a/sales/users.email"
	applyOne(t, eng, 1, mustEncode(protocol.EncodeIndexCreateOpts(idxName, "email", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false})))
	applyMVCCPut(t, eng, 2, "tenant-b/sales/__col:sales:users/2", `{"email":"victim@example.com"}`)

	got, err := eng.Lookup(context.Background(), service.IndexQueryRequest{
		IndexName: idxName,
		Value:     []byte("victim@example.com"),
	})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	keys, ok := got.([][]byte)
	if !ok {
		t.Fatalf("Lookup returned %T; want [][]byte", got)
	}
	if len(keys) != 0 {
		t.Fatalf("IndexQuery on tenant-a's index returned tenant-b keys: %q", keys)
	}
}

// TestMVCCEngine_IndexDoesNotSpanDatabases states the cross-database half of
// the same property: within one tenant, an index named for one database does
// not index another database's keys.
func TestMVCCEngine_IndexDoesNotSpanDatabases(t *testing.T) {
	eng := newScopedEngine(t)

	const idxName = "tenant-a/sales/users.email"
	applyOne(t, eng, 1, mustEncode(protocol.EncodeIndexCreateOpts(idxName, "email", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false})))

	applyMVCCPut(t, eng, 2, "tenant-a/sales/__col:sales:users/1", `{"email":"in@example.com"}`)
	applyMVCCPut(t, eng, 3, "tenant-a/hr/__col:hr:people/2", `{"email":"out@example.com"}`)

	assertIndexed(t, eng, idxName, "tenant-a/sales/__col:sales:users/1")
}

// TestMVCCEngine_IndexHonoursDeclaredKeyPrefix states the collection case
// measured in go-qrdb#410: an index created for one collection must index
// that collection's documents and nothing else -- not another collection in
// another database, and not a plain key-value key.
//
// The collection service declares the scope explicitly, because the document
// key prefix "__col:{database}:{collection}/" is not derivable from the
// index name "{database}.{collection}.{field}" without guessing.
func TestMVCCEngine_IndexHonoursDeclaredKeyPrefix(t *testing.T) {
	eng := newScopedEngine(t)

	const idxName = "dbA.users.email"
	cmd := mustEncode(protocol.EncodeIndexCreateOpts(idxName, "email", protocol.IndexCreateOptions{
		KeyPrefix: "__col:dbA:users/",
	}))
	applyOne(t, eng, 1, cmd)

	applyMVCCPut(t, eng, 2, "__col:dbA:users/1", `{"email":"mine@example.com"}`)
	applyMVCCPut(t, eng, 3, "__col:dbB:people/2", `{"email":"theirs@example.com"}`)
	applyMVCCPut(t, eng, 4, "plain-kv-key", `{"email":"loose@example.com"}`)

	assertIndexed(t, eng, idxName, "__col:dbA:users/1")
}

// TestMVCCEngine_IndexKeyPrefixComposesWithNamespace states that the two
// halves of a scope compose: the namespace comes off the index NAME (applied
// by the transport registry, which prefixes keys with the same string) and
// the declared key prefix is relative to it, exactly as the document key is.
func TestMVCCEngine_IndexKeyPrefixComposesWithNamespace(t *testing.T) {
	eng := newScopedEngine(t)

	const idxName = "tenant-a/sales/sales.users.email"
	cmd := mustEncode(protocol.EncodeIndexCreateOpts(idxName, "email", protocol.IndexCreateOptions{
		KeyPrefix: "__col:sales:users/",
	}))
	applyOne(t, eng, 1, cmd)

	applyMVCCPut(t, eng, 2, "tenant-a/sales/__col:sales:users/1", `{"email":"in@example.com"}`)
	// Right collection, wrong tenant.
	applyMVCCPut(t, eng, 3, "tenant-b/sales/__col:sales:users/2", `{"email":"out@example.com"}`)
	// Right tenant and database, wrong collection.
	applyMVCCPut(t, eng, 4, "tenant-a/sales/__col:sales:orders/3", `{"email":"out2@example.com"}`)

	assertIndexed(t, eng, idxName, "tenant-a/sales/__col:sales:users/1")
}

// TestMVCCEngine_IndexBackfillIsKeyScoped covers index creation over an
// already-populated shard. Creation backfills, so an unscoped backfill leaks
// exactly what unscoped maintenance leaks -- and does it in one step.
func TestMVCCEngine_IndexBackfillIsKeyScoped(t *testing.T) {
	eng := newScopedEngine(t)

	applyMVCCPut(t, eng, 1, "tenant-a/sales/__col:sales:users/1", `{"email":"a@example.com"}`)
	applyMVCCPut(t, eng, 2, "tenant-b/sales/__col:sales:users/2", `{"email":"b@example.com"}`)

	const idxName = "tenant-a/sales/users.email"
	applyOne(t, eng, 3, mustEncode(protocol.EncodeIndexCreateOpts(idxName, "email", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false})))

	assertIndexed(t, eng, idxName, "tenant-a/sales/__col:sales:users/1")
}

// TestMVCCEngine_UniqueIndexDoesNotRejectAnotherTenantsWrite states the
// write-side half of the leak. A unique index is a pre-check on the apply
// path: an unscoped unique index lets one tenant's stored value REJECT
// another tenant's write, which is both a denial and an oracle for the
// existence of a value the caller cannot read.
func TestMVCCEngine_UniqueIndexDoesNotRejectAnotherTenantsWrite(t *testing.T) {
	eng := newScopedEngine(t)

	const idxName = "tenant-a/sales/users.email"
	applyOne(t, eng, 1, mustEncode(protocol.EncodeIndexCreateOpts(idxName, "email", protocol.IndexCreateOptions{Unique: true, Sparse: false, Numeric: false})))

	applyMVCCPut(t, eng, 2, "tenant-a/sales/u/1", `{"email":"shared@example.com"}`)

	res := applyMVCCPut(t, eng, 3, "tenant-b/sales/u/2", `{"email":"shared@example.com"}`)
	if len(res.Data) != 0 {
		t.Fatalf("tenant-b's write was rejected by tenant-a's unique index: %s", res.Data)
	}

	// And the write landed.
	kv, err := eng.store.Get([]byte("tenant-b/sales/u/2"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if kv == nil {
		t.Fatal("tenant-b's write did not land")
	}
}

// TestMVCCEngine_DeleteDoesNotRetractAcrossScopes states the delete-side
// half: removing a key must not touch an index that does not cover it. An
// unscoped retraction is a cross-tenant write into another tenant's index.
func TestMVCCEngine_DeleteDoesNotRetractAcrossScopes(t *testing.T) {
	eng := newScopedEngine(t)

	const idxName = "tenant-a/sales/users.email"
	applyOne(t, eng, 1, mustEncode(protocol.EncodeIndexCreateOpts(idxName, "email", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false})))
	applyMVCCPut(t, eng, 2, "tenant-a/sales/u/1", `{"email":"same@example.com"}`)

	// tenant-b writes and deletes the same indexed value. Neither operation
	// may reach tenant-a's index.
	applyMVCCPut(t, eng, 3, "tenant-b/sales/u/1", `{"email":"same@example.com"}`)
	applyOne(t, eng, 4, mustEncode(protocol.EncodeMVCCDelete([]byte("tenant-b/sales/u/1"))))

	assertIndexed(t, eng, idxName, "tenant-a/sales/u/1")
}

// TestMVCCEngine_TransactionPathIsKeyScoped covers the batched apply paths.
// handleTransaction and handleBatch maintain indexes through their own call
// sites, so a fix applied only to the single-put handlers would leave the
// boundary open to any caller willing to wrap the write in a transaction.
func TestMVCCEngine_TransactionPathIsKeyScoped(t *testing.T) {
	eng := newScopedEngine(t)

	const idxName = "tenant-a/sales/users.email"
	applyOne(t, eng, 1, mustEncode(protocol.EncodeIndexCreateOpts(idxName, "email", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false})))

	ops := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("tenant-a/sales/u/1"), []byte(`{"email":"in@example.com"}`), nil, 0)),
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("tenant-b/sales/u/2"), []byte(`{"email":"out@example.com"}`), nil, 0)),
	}
	applyOne(t, eng, 2, mustEncode(protocol.EncodeBatch(ops)))

	assertIndexed(t, eng, idxName, "tenant-a/sales/u/1")
}

// TestMVCCEngine_UnscopedIndexRemainsShardWide pins the residual, so that
// narrowing it later is a deliberate act rather than a silent drift.
//
// An index whose name carries NO "<tenant>/<database>/" namespace and whose
// creator declared NO key prefix cannot be attributed to a keyspace: the name
// is the only thing the apply path is given, and a bare name says nothing
// about which keys belong to it. Such an index keeps the shard-wide behaviour
// go-qrdb#410 measured. This is reachable only in a single-tenant composition
// -- every transport registry namespaces both keys and index names, so an
// index created over ANY transport carries a namespace and is scoped by the
// tests above.
func TestMVCCEngine_UnscopedIndexRemainsShardWide(t *testing.T) {
	eng := newScopedEngine(t)

	const idxName = "by_email"
	applyOne(t, eng, 1, mustEncode(protocol.EncodeIndexCreateOpts(idxName, "email", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false})))

	applyMVCCPut(t, eng, 2, "anything/1", `{"email":"a@example.com"}`)
	applyMVCCPut(t, eng, 3, "elsewhere/2", `{"email":"b@example.com"}`)

	assertIndexed(t, eng, idxName, "anything/1", "elsewhere/2")
}

// ---------------------------------------------------------------------------
// KVEngine — the same property on the batching apply path
// ---------------------------------------------------------------------------

// kvIndexedKeys returns the sorted entity keys held by a KVEngine index.
func kvIndexedKeys(t *testing.T, eng *KVEngine, name string) []string {
	t.Helper()
	idx, ok := eng.indexes[name]
	if !ok {
		t.Fatalf("index %q not registered", name)
	}
	keys, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll(%s): %v", name, err)
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, string(k))
	}
	sort.Strings(out)
	return out
}

// assertKVIndexed asserts the exact set of keys a KVEngine index holds.
func assertKVIndexed(t *testing.T, eng *KVEngine, name string, want ...string) {
	t.Helper()
	got := kvIndexedKeys(t, eng, name)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("index %q holds %v; want %v", name, got, want)
	}
	for i := range got {
		// #nosec G602 -- the lengths are compared equal above, and i ranges over got.
		if got[i] != want[i] {
			t.Fatalf("index %q holds %v; want %v", name, got, want)
		}
	}
}

// newScopedKVEngine builds a KVEngine over memory storage.
func newScopedKVEngine(t *testing.T) *KVEngine {
	t.Helper()
	stg := memory.NewStorage()
	t.Cleanup(func() { _ = stg.Close() })
	eng := NewKVEngine(stg)
	t.Cleanup(func() { _ = eng.Close(context.Background()) })
	return eng
}

// TestKVEngine_IndexDoesNotSpanTenants is TestMVCCEngine_IndexDoesNotSpanTenants
// for the KV engine, whose apply path maintains indexes through the batch
// helpers instead of the direct ones. Both engines serve the same transports.
func TestKVEngine_IndexDoesNotSpanTenants(t *testing.T) {
	eng := newScopedKVEngine(t)

	const idxName = "tenant-a/sales/users.email"
	applyKVEntries(t, eng, []sm.Entry{{
		Index: 1,
		Cmd:   mustEncode(protocol.EncodeIndexCreateOpts(idxName, "email", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false})),
	}})

	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(2, "tenant-a/sales/u/1", `{"email":"a@example.com"}`),
		kvPutEntry(3, "tenant-b/sales/u/2", `{"email":"b@example.com"}`),
	})

	assertKVIndexed(t, eng, idxName, "tenant-a/sales/u/1")
}

// TestKVEngine_IndexHonoursDeclaredKeyPrefix states the declared half of the
// scope on the KV engine: an index created for one collection's documents
// does not index another collection's, another database's, or a plain key.
func TestKVEngine_IndexHonoursDeclaredKeyPrefix(t *testing.T) {
	eng := newScopedKVEngine(t)

	const idxName = "dbA.users.email"
	applyKVEntries(t, eng, []sm.Entry{{
		Index: 1,
		Cmd: mustEncode(protocol.EncodeIndexCreateOpts(idxName, "email", protocol.IndexCreateOptions{
			KeyPrefix: "__col:dbA:users/",
		})),
	}})

	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(2, "__col:dbA:users/1", `{"email":"mine@example.com"}`),
		kvPutEntry(3, "__col:dbB:people/2", `{"email":"theirs@example.com"}`),
		kvPutEntry(4, "plain-kv-key", `{"email":"loose@example.com"}`),
	})

	assertKVIndexed(t, eng, idxName, "__col:dbA:users/1")
}

// TestKVEngine_IndexBackfillIsKeyScoped covers creation over a populated
// shard on the KV engine.
func TestKVEngine_IndexBackfillIsKeyScoped(t *testing.T) {
	eng := newScopedKVEngine(t)

	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "tenant-a/sales/u/1", `{"email":"a@example.com"}`),
		kvPutEntry(2, "tenant-b/sales/u/2", `{"email":"b@example.com"}`),
	})

	const idxName = "tenant-a/sales/users.email"
	applyKVEntries(t, eng, []sm.Entry{{
		Index: 3,
		Cmd:   mustEncode(protocol.EncodeIndexCreateOpts(idxName, "email", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false})),
	}})

	assertKVIndexed(t, eng, idxName, "tenant-a/sales/u/1")
}

// TestKVEngine_RegisterEntityTypeScopesToTheEntity states that a DAO entity
// index registered directly on the engine covers that entity type's keyspace
// on the APPLY path. PutWithIndexes/DeleteWithIndexes select by the same
// scope; see kv_engine_namespaced_index_test.go.
func TestKVEngine_RegisterEntityTypeScopesToTheEntity(t *testing.T) {
	eng := newScopedKVEngine(t)

	if err := eng.RegisterEntityType("users", []EntityIndex{{
		FieldName:     "Email",
		JSONFieldName: "email",
		CaseSensitive: true,
	}}); err != nil {
		t.Fatalf("RegisterEntityType: %v", err)
	}

	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "users/1", `{"email":"in@example.com"}`),
		kvPutEntry(2, "orders/2", `{"email":"out@example.com"}`),
	})

	assertKVIndexed(t, eng, "users.Email", "users/1")
}

// ---------------------------------------------------------------------------
// Scope composition
// ---------------------------------------------------------------------------

func TestIndexNamespace(t *testing.T) {
	tests := []struct {
		name  string
		index string
		want  string
	}{
		{name: "tenant and database", index: "tenant-a/sales/users.email", want: "tenant-a/sales/"},
		{name: "bootstrap pair", index: "system/system/by_email", want: "system/system/"},
		{name: "inner name carries a slash", index: "t/db/a/b/c", want: "t/db/"},
		{name: "namespace only", index: "t/db/", want: "t/db/"},
		{name: "one segment only", index: "tenant-a/users.email", want: ""},
		{name: "no separator", index: "users.email", want: ""},
		{name: "empty", index: "", want: ""},
		{name: "leading separator is not a segment", index: "/db/users.email", want: ""},
		{name: "empty database segment", index: "t//users.email", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IndexNamespace(tt.index); got != tt.want {
				t.Errorf("IndexNamespace(%q) = %q; want %q", tt.index, got, tt.want)
			}
		})
	}
}

func TestComposeIndexScope(t *testing.T) {
	tests := []struct {
		name      string
		index     string
		keyPrefix string
		want      string
		wantNil   bool
	}{
		{name: "namespace only", index: "t/db/users.email", want: "t/db/"},
		{name: "declared only", index: "dbA.users.email", keyPrefix: "__col:dbA:users/", want: "__col:dbA:users/"},
		{name: "both compose", index: "t/db/dbA.users.email", keyPrefix: "__col:dbA:users/", want: "t/db/__col:dbA:users/"},
		{name: "neither is unscoped", index: "by_email", wantNil: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComposeIndexScope(tt.index, tt.keyPrefix)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("ComposeIndexScope(%q, %q) = %q; want nil", tt.index, tt.keyPrefix, got)
				}
				return
			}
			if string(got) != tt.want {
				t.Fatalf("ComposeIndexScope(%q, %q) = %q; want %q", tt.index, tt.keyPrefix, got, tt.want)
			}
		})
	}
}

func TestKeyInScope(t *testing.T) {
	tests := []struct {
		name  string
		scope string
		key   string
		nilSc bool
		want  bool
	}{
		{name: "inside", scope: "t/db/", key: "t/db/users/1", want: true},
		{name: "outside", scope: "t/db/", key: "t/other/users/1", want: false},
		{name: "prefix of the scope is not inside it", scope: "t/db/", key: "t/db", want: false},
		{name: "sibling tenant sharing a prefix", scope: "t/db/", key: "t/dbx/users/1", want: false},
		{name: "empty scope admits everything", scope: "", key: "anything", want: true},
		{name: "nil scope admits everything", nilSc: true, key: "anything", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var scope []byte
			if !tt.nilSc {
				scope = []byte(tt.scope)
			}
			if got := KeyInScope(scope, []byte(tt.key)); got != tt.want {
				t.Errorf("KeyInScope(%q, %q) = %v; want %v", tt.scope, tt.key, got, tt.want)
			}
		})
	}
}

func TestBackfillRange(t *testing.T) {
	tests := []struct {
		name      string
		scope     []byte
		wantStart string
		wantEnd   string
	}{
		{name: "unscoped scans everything", scope: nil, wantStart: "\x00", wantEnd: "\xff"},
		{name: "empty scans everything", scope: []byte{}, wantStart: "\x00", wantEnd: "\xff"},
		{name: "scoped bounds the scan", scope: []byte("t/db/"), wantStart: "t/db/", wantEnd: "t/db0"},
		{name: "all-0xFF scope has no successor", scope: []byte{0xFF, 0xFF}, wantStart: "\xff\xff", wantEnd: "\xff"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end := backfillRange(tt.scope)
			if string(start) != tt.wantStart || string(end) != tt.wantEnd {
				t.Errorf("backfillRange(%q) = (%q, %q); want (%q, %q)",
					tt.scope, start, end, tt.wantStart, tt.wantEnd)
			}
		})
	}
}

func TestAnyIndexInScope(t *testing.T) {
	eng := newScopedEngine(t)
	applyOne(t, eng, 1, mustEncode(protocol.EncodeIndexCreateOpts("t/a/by_email", "email", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false})))
	applyOne(t, eng, 2, mustEncode(protocol.EncodeIndexCreateOpts("t/b/by_email", "email", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false})))

	if !anyIndexInScope(eng.indexes, eng.indexScopes, []byte("t/b/u/1")) {
		t.Error("a key inside one index's scope must be reported in scope")
	}
	if anyIndexInScope(eng.indexes, eng.indexScopes, []byte("t/c/u/1")) {
		t.Error("a key inside no index's scope must be reported out of scope")
	}
	if !anyIndexInScope(eng.indexes, nil, []byte("t/c/u/1")) {
		t.Error("a nil scope map must admit every key")
	}
}

// TestKVEngine_CompoundIndexIsKeyScoped states the property for compound
// indexes, which have their own maintenance helpers and their own map. They
// are registered in-process only -- no wire command creates one -- so the
// reachable blast radius is narrower than for single-field indexes, but the
// defect is the same one and the mechanism is the same.
func TestKVEngine_CompoundIndexIsKeyScoped(t *testing.T) {
	eng := newScopedKVEngine(t)

	if err := eng.RegisterEntityType("users", []EntityIndex{{
		FieldNames:     []string{"City", "Email"},
		JSONFieldNames: []string{"city", "email"},
		CaseSensitive:  true,
	}}); err != nil {
		t.Fatalf("RegisterEntityType: %v", err)
	}

	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "users/1", `{"city":"paris","email":"in@example.com"}`),
		kvPutEntry(2, "orders/2", `{"city":"paris","email":"out@example.com"}`),
	})

	var idxName string
	for name := range eng.compoundIndexes {
		idxName = name
	}
	if idxName == "" {
		t.Fatal("no compound index registered")
	}
	keys, err := eng.compoundIndexes[idxName].GetCompound([][]byte{[]byte("paris"), []byte("out@example.com")})
	if err != nil {
		t.Fatalf("GetCompound: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("compound index over entity %q holds foreign keys: %q", "users", keys)
	}

	own, err := eng.compoundIndexes[idxName].GetCompound([][]byte{[]byte("paris"), []byte("in@example.com")})
	if err != nil {
		t.Fatalf("GetCompound: %v", err)
	}
	if len(own) != 1 || string(own[0]) != "users/1" {
		t.Fatalf("compound index lost its own key: %q", own)
	}
}
