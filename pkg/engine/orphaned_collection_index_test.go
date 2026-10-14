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
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// ---------------------------------------------------------------------------
// What an index left behind by a collection drop actually costs.
//
// These tests measure the engine behaviour that decides the severity of
// service.CollectionService.DropCollection not dropping the indexes
// CreateCollection built (go-qrdb#399). They are the evidence for the
// service-layer contract pinned in pkg/service/collection_index_lifecycle_test.go,
// and they must keep holding for that contract to remain necessary.
// ---------------------------------------------------------------------------

func indexCreateEntry(t *testing.T, idx uint64, name, field string, opts protocol.IndexCreateOptions) sm.Entry {
	t.Helper()
	cmd, err := protocol.EncodeIndexCreateOpts(name, field, opts)
	if err != nil {
		t.Fatalf("EncodeIndexCreateOpts: %v", err)
	}
	return sm.Entry{Index: idx, Cmd: cmd}
}

func indexDropEntry(t *testing.T, idx uint64, name string) sm.Entry {
	t.Helper()
	cmd, err := protocol.EncodeIndexDrop(name)
	if err != nil {
		t.Fatalf("EncodeIndexDrop: %v", err)
	}
	return sm.Entry{Index: idx, Cmd: cmd}
}

func indexQueryKeys(t *testing.T, eng *MVCCEngine, name string, value []byte) [][]byte {
	t.Helper()
	res, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: name,
		Value:     value,
	})
	if err != nil {
		t.Fatalf("index query %s: %v", name, err)
	}
	keys, ok := res.([][]byte)
	if !ok {
		t.Fatalf("index query %s: unexpected result type %T", name, res)
	}
	return keys
}

func indexNames(t *testing.T, eng *MVCCEngine) []string {
	t.Helper()
	res, err := eng.Lookup(context.Background(), index.IndexListRequest{})
	if err != nil {
		t.Fatalf("index list: %v", err)
	}
	names, ok := res.([]string)
	if !ok {
		t.Fatalf("index list: unexpected result type %T", res)
	}
	return names
}

// isAppErr reports whether an apply result carries an application-level error
// (the encoding appErrResult produces), and returns its message.
func isAppErr(r sm.Result) (string, bool) {
	const marker = "\x00qrdb.apperr\x00"
	s := string(r.Data)
	if !strings.HasPrefix(s, marker) {
		return "", false
	}
	return strings.TrimPrefix(s, marker), true
}

// TestMVCCEngine_OrphanedCollectionIndex_DoesNotResurrectDroppedDocuments
// answers the first question a leaked index raises: can a query read through
// it and return a document the drop removed?
//
// It cannot. Deleting a document retracts its entries from every index over
// it, and DropCollection deletes every document before it deletes the
// metadata -- so the index a drop leaves behind is EMPTY of that collection's
// rows. The leak is real; a stale read of the dropped data through it is not.
func TestMVCCEngine_OrphanedCollectionIndex_DoesNotResurrectDroppedDocuments(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	const idxName = "mydb.users.email"
	docKey := []byte("__col:mydb:users/1")

	applyMVCCEntries(t, eng, []sm.Entry{
		indexCreateEntry(t, 1, idxName, "email", protocol.IndexCreateOptions{}),
		mvccPutEntry(2, docKey, []byte(`{"email":"alice@example.com"}`), 0),
	})

	keys := indexQueryKeys(t, eng, idxName, []byte("alice@example.com"))
	if len(keys) != 1 || !bytes.Equal(keys[0], docKey) {
		t.Fatalf("index should resolve the live document, got %q", keys)
	}

	// DropCollection deletes every document key before touching metadata.
	applyMVCCEntries(t, eng, []sm.Entry{mvccDeleteEntry(3, docKey)})

	if keys := indexQueryKeys(t, eng, idxName, []byte("alice@example.com")); len(keys) != 0 {
		t.Fatalf("dropped document must not remain reachable through the index, got %q", keys)
	}

	// The definition, however, survives the document deletions.
	names := indexNames(t, eng)
	if len(names) != 1 || names[0] != idxName {
		t.Fatalf("index definition should still exist after the documents are gone, got %v", names)
	}
}

// TestMVCCEngine_OrphanedCollectionIndex_SuppressesADivergentRecreate is the
// consequence that makes the leak a correctness defect rather than wasted
// space.
//
// handleIndexCreate is idempotent BY NAME: a create for a name that already
// exists succeeds without reconciling the definition, and the ORIGINAL wins
// (its own comment says a divergent re-create must be rejected at the service
// layer). So if a dropped collection's non-unique index survives, recreating
// the collection with a UNIQUE index on the same field silently gets the old
// non-unique one, and duplicate values are accepted.
func TestMVCCEngine_OrphanedCollectionIndex_SuppressesADivergentRecreate(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	const idxName = "mydb.users.email"

	// First collection: a non-unique index on email.
	applyMVCCEntries(t, eng, []sm.Entry{
		indexCreateEntry(t, 1, idxName, "email", protocol.IndexCreateOptions{}),
	})

	// The collection is dropped WITHOUT dropping the index, then recreated
	// asking for a unique index on the same field.
	applyMVCCEntries(t, eng, []sm.Entry{
		indexCreateEntry(t, 2, idxName, "email", protocol.IndexCreateOptions{Unique: true}),
	})

	results := applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(3, []byte("__col:mydb:users/10"), []byte(`{"email":"dup@example.com"}`), 0),
		mvccPutEntry(4, []byte("__col:mydb:users/11"), []byte(`{"email":"dup@example.com"}`), 0),
	})
	for i, r := range results {
		if msg, ok := isAppErr(r); ok {
			t.Fatalf("put %d unexpectedly rejected: %s", i, msg)
		}
	}
	// Both duplicates landed: the requested unique constraint was never in
	// force, because the surviving definition took precedence.
	if keys := indexQueryKeys(t, eng, idxName, []byte("dup@example.com")); len(keys) != 2 {
		t.Fatalf("orphaned non-unique index should hold both duplicates, got %d", len(keys))
	}
}

// TestMVCCEngine_DroppingTheIndexRestoresTheRecreatedDefinition is the control
// for the test above: with the drop the collection lifecycle now performs, the
// recreated collection gets the definition it asked for and the unique
// constraint is enforced.
func TestMVCCEngine_DroppingTheIndexRestoresTheRecreatedDefinition(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	const idxName = "mydb.users.email"

	applyMVCCEntries(t, eng, []sm.Entry{
		indexCreateEntry(t, 1, idxName, "email", protocol.IndexCreateOptions{}),
		// DropCollection now drops the index it created.
		indexDropEntry(t, 2, idxName),
		indexCreateEntry(t, 3, idxName, "email", protocol.IndexCreateOptions{Unique: true}),
	})

	if names := indexNames(t, eng); len(names) != 1 || names[0] != idxName {
		t.Fatalf("expected the recreated index, got %v", names)
	}

	results := applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(4, []byte("__col:mydb:users/10"), []byte(`{"email":"dup@example.com"}`), 0),
		mvccPutEntry(5, []byte("__col:mydb:users/11"), []byte(`{"email":"dup@example.com"}`), 0),
	})
	if msg, ok := isAppErr(results[0]); ok {
		t.Fatalf("first put should succeed, got %s", msg)
	}
	msg, ok := isAppErr(results[1])
	if !ok {
		t.Fatal("second put with a duplicate value must violate the unique index")
	}
	if !strings.Contains(msg, "unique constraint violation") {
		t.Fatalf("expected a unique-constraint violation, got %q", msg)
	}
}

// TestMVCCEngine_IndexDropIsIdempotent pins the property the retry story in
// CollectionService.DropCollection depends on: dropping an index that is
// already gone succeeds rather than failing the apply.
func TestMVCCEngine_IndexDropIsIdempotent(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	const idxName = "mydb.users.email"

	applyMVCCEntries(t, eng, []sm.Entry{
		indexCreateEntry(t, 1, idxName, "email", protocol.IndexCreateOptions{}),
		indexDropEntry(t, 2, idxName),
	})

	results := applyMVCCEntries(t, eng, []sm.Entry{
		indexDropEntry(t, 3, idxName),
		indexDropEntry(t, 4, "mydb.never.created"),
	})
	for i, r := range results {
		if msg, ok := isAppErr(r); ok {
			t.Fatalf("redundant drop %d should be a no-op, got %s", i, msg)
		}
		if r.Value != 1 {
			t.Fatalf("redundant drop %d should report success, got Value=%d", i, r.Value)
		}
	}
	if names := indexNames(t, eng); len(names) != 0 {
		t.Fatalf("expected no indexes, got %v", names)
	}
}

// TestMVCCEngine_IndexOnTheApplyPathIsNotKeyScoped pins what an UNSCOPED index
// still does on the Raft apply path, and it survives go-qrdb#410 deliberately.
//
// #410 narrowed apply-path index maintenance to the keys an index owns, where
// ownership is the namespace carried in the index name plus any declared key
// prefix. This index has NEITHER: the name `dbA.users.email` carries no
// `{tenant}/{database}/` namespace, and the options below declare no prefix. So
// it is the documented residual — an index nothing can attribute is offered
// every written value, exactly as before — and that is why this test still
// passes after the fix rather than being deleted with it.
//
// No transport can create such an index: every registry namespaces both keys
// and index names. A single-tenant, registry-less in-process composer can, and
// for that composer "the whole state machine" and "its own database" are the
// same set.
//
// Written for go-qrdb#399 to size what a leaked index costs while it survives.
// Retained, with this narrowed reading, so that a later change which closes the
// unscoped residual has to update this statement deliberately rather than by
// accident — which is the job a characterisation test exists to do.
func TestMVCCEngine_IndexOnTheApplyPathIsNotKeyScoped(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	const idxName = "dbA.users.email"

	applyMVCCEntries(t, eng, []sm.Entry{
		indexCreateEntry(t, 1, idxName, "email", protocol.IndexCreateOptions{}),
		mvccPutEntry(2, []byte("__col:dbA:users/1"), []byte(`{"email":"x@example.com"}`), 0),
		mvccPutEntry(3, []byte("__col:dbB:people/2"), []byte(`{"email":"x@example.com"}`), 0),
		mvccPutEntry(4, []byte("plain-kv-key"), []byte(`{"email":"x@example.com"}`), 0),
	})

	got := indexQueryKeys(t, eng, idxName, []byte("x@example.com"))
	want := [][]byte{
		[]byte("__col:dbA:users/1"),
		[]byte("__col:dbB:people/2"),
		[]byte("plain-kv-key"),
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d keys, got %d: %q", len(want), len(got), got)
	}
	for _, w := range want {
		found := false
		for _, g := range got {
			if bytes.Equal(g, w) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected %q in the index, got %q", w, got)
		}
	}
}

// TestMVCCEngine_QueryingADroppedIndexFailsRatherThanAnsweringEmpty pins the
// half of the teardown window that is safe, and that
// docs/collections/architecture.md now asserts: a query naming an index that
// no longer exists is an ERROR, not an empty result set.
//
// The distinction decides how the drop-ordering trade-off reads. If a dropped
// index answered empty with a nil error, a collection briefly missing its
// indexes would silently return "no matches" -- the failure the secondary
// index subsystem is built to avoid. It does not; reads fail loudly and only
// WRITES go unguarded while an index is absent.
func TestMVCCEngine_QueryingADroppedIndexFailsRatherThanAnsweringEmpty(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	const idxName = "mydb.users.email"

	applyMVCCEntries(t, eng, []sm.Entry{
		indexCreateEntry(t, 1, idxName, "email", protocol.IndexCreateOptions{}),
		mvccPutEntry(2, []byte("__col:mydb:users/1"), []byte(`{"email":"alice@example.com"}`), 0),
		indexDropEntry(t, 3, idxName),
	})

	reads := map[string]interface{}{
		"query": index.IndexQueryRequest{IndexName: idxName, Value: []byte("alice@example.com")},
		"scan":  index.IndexScanRequest{IndexName: idxName, Start: []byte("a"), End: []byte("z")},
		"query on a name never created": index.IndexQueryRequest{
			IndexName: "mydb.never.created", Value: []byte("x"),
		},
	}
	for name, req := range reads {
		t.Run(name, func(t *testing.T) {
			res, err := eng.Lookup(context.Background(), req)
			if err == nil {
				t.Fatalf("expected an error for an absent index, got result %#v with nil error", res)
			}
			var missing *index.NotFoundError
			if !errors.As(err, &missing) {
				t.Fatalf("expected *index.NotFoundError, got %T: %v", err, err)
			}
			if !strings.Contains(err.Error(), "not found") {
				t.Fatalf("error should name the missing index, got %q", err)
			}
		})
	}
}
