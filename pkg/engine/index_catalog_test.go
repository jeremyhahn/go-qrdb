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
	"slices"
	"testing"

	sm "github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// A secondary index's definition lives in the index catalog, written in the
// apply batch of its create, so an engine opened over storage that outlived
// the one that applied the create -- a reopened standalone store, which has
// no log to replay, or a snapshot recovery -- serves the index.

const catalogIdx = "by_email"

// catalogOpts is a definition that exercises every option a restore must
// carry: unique, owned, case-insensitive and scoped to a key prefix.
var catalogOpts = protocol.IndexCreateOptions{
	Unique:          true,
	CaseInsensitive: true,
	KeyPrefix:       "user/",
	Owner:           "collection:users",
}

// catalogCreate is a create proposed under the index-catalog feature, the
// only kind that records its definition (see uncataloguedCreate).
func catalogCreate(t *testing.T, entry uint64, opts protocol.IndexCreateOptions) sm.Entry {
	t.Helper()
	opts.Catalog = true
	return uncataloguedCreate(t, entry, opts)
}

// uncataloguedCreate is the create exactly as given: with opts.Catalog clear
// it is what a proposer sends before index-catalog is active on its group.
func uncataloguedCreate(t *testing.T, entry uint64, opts protocol.IndexCreateOptions) sm.Entry {
	t.Helper()
	return sm.Entry{Index: entry, Cmd: mustEncode(protocol.EncodeIndexCreateOpts(catalogIdx, "email", opts))}
}

func catalogDrop(t *testing.T, entry uint64, opts protocol.IndexDropOptions) sm.Entry {
	t.Helper()
	cmd, err := protocol.EncodeIndexDropOpts(catalogIdx, opts)
	if err != nil {
		t.Fatalf("EncodeIndexDropOpts: %v", err)
	}
	return sm.Entry{Index: entry, Cmd: cmd}
}

func openCatalogEngine(t *testing.T, stg storage.Storage) *MVCCEngine {
	t.Helper()
	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close(context.Background()) })
	return eng
}

// lookupEmail answers the keys the index holds for email, or the lookup
// error.
func lookupEmail(eng *MVCCEngine, email string) ([]string, error) {
	got, err := eng.Lookup(context.Background(), service.IndexQueryRequest{IndexName: catalogIdx, Value: []byte(email)})
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0)
	for _, k := range got.([][]byte) {
		keys = append(keys, string(k))
	}
	slices.Sort(keys)
	return keys, nil
}

func wantEmailKeys(t *testing.T, eng *MVCCEngine, email string, want ...string) {
	t.Helper()
	got, err := lookupEmail(eng, email)
	if err != nil {
		t.Fatalf("lookup %q: %v", email, err)
	}
	if want == nil {
		want = []string{}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("lookup %q = %v, want %v", email, got, want)
	}
}

func wantIndexNotFound(t *testing.T, eng *MVCCEngine) {
	t.Helper()
	_, err := lookupEmail(eng, "a@example.com")
	var nf *index.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("lookup = %v, want index not found", err)
	}
}

func TestMVCCEngine_IndexSurvivesReopen(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	first := openCatalogEngine(t, stg)
	applyMVCCEntries(t, first, []sm.Entry{catalogCreate(t, 1, catalogOpts)})
	applyMVCCEntries(t, first, []sm.Entry{
		mvccPutEntry(2, []byte("user/1"), []byte(`{"email":"a@example.com"}`), 0),
		mvccPutEntry(3, []byte("user/2"), []byte(`{"email":"b@example.com"}`), 0),
	})
	if err := first.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	eng := openCatalogEngine(t, stg)
	// Both keys answer, and the case-insensitive definition came back.
	wantEmailKeys(t, eng, "A@EXAMPLE.COM", "user/1")
	wantEmailKeys(t, eng, "b@example.com", "user/2")

	// The restored index is still unique.
	res := applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(4, []byte("user/3"), []byte(`{"email":"a@example.com"}`), 0),
	})
	if _, ok := protocol.DecodeAppError(res[0].Data); !ok {
		t.Fatalf("a duplicate put after the reopen was not refused: result %+v", res[0])
	}

	// The restored scope still bounds what the index holds.
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(5, []byte("other/1"), []byte(`{"email":"c@example.com"}`), 0),
	})
	wantEmailKeys(t, eng, "c@example.com")

	// The restored owner still guards the drop.
	res = applyMVCCEntries(t, eng, []sm.Entry{
		catalogDrop(t, 6, protocol.IndexDropOptions{CheckOwner: true, Owner: "collection:other"}),
	})
	if _, owner, ok := protocol.DecodeIndexOwnershipError(res[0].Data); !ok || owner != catalogOpts.Owner {
		t.Fatalf("a drop by another owner after the reopen was not refused: result %+v", res[0])
	}
	wantEmailKeys(t, eng, "a@example.com", "user/1")

	eng.indexMu.RLock()
	gotOwner, gotType, gotScope := eng.indexOwners[catalogIdx], eng.indexValueTypes[catalogIdx], eng.indexScopes[catalogIdx]
	eng.indexMu.RUnlock()
	if gotOwner != catalogOpts.Owner {
		t.Fatalf("restored owner = %q, want %q", gotOwner, catalogOpts.Owner)
	}
	if want := IndexCreateValueType(catalogOpts); gotType != want {
		t.Fatalf("restored value type = %v, want %v", gotType, want)
	}
	if want := ComposeIndexScope(catalogIdx, catalogOpts.KeyPrefix); !bytes.Equal(gotScope, want) {
		t.Fatalf("restored scope = %q, want %q", gotScope, want)
	}
}

func TestMVCCEngine_IndexDropSurvivesReopen(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	first := openCatalogEngine(t, stg)
	applyMVCCEntries(t, first, []sm.Entry{catalogCreate(t, 1, catalogOpts)})
	applyMVCCEntries(t, first, []sm.Entry{catalogDrop(t, 2, protocol.IndexDropOptions{})})
	if err := first.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	eng := openCatalogEngine(t, stg)
	wantIndexNotFound(t, eng)

	res := applyMVCCEntries(t, eng, []sm.Entry{catalogCreate(t, 3, protocol.IndexCreateOptions{Owner: "collection:new"})})
	if res[0].Value != 1 || len(res[0].Data) != 0 {
		t.Fatalf("a create under the dropped name was refused: result %+v", res[0])
	}
	eng.indexMu.RLock()
	gotOwner := eng.indexOwners[catalogIdx]
	eng.indexMu.RUnlock()
	if gotOwner != "collection:new" {
		t.Fatalf("owner after the second create = %q, want collection:new", gotOwner)
	}
}

func TestMVCCEngine_IndexRebuildKeepsTheNewDefinition(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	numeric := protocol.IndexCreateOptions{Numeric: true}
	lexical := protocol.IndexCreateOptions{}
	if IndexCreateValueType(numeric) == IndexCreateValueType(lexical) {
		t.Fatal("the two definitions must differ in value type")
	}

	first := openCatalogEngine(t, stg)
	applyMVCCEntries(t, first, []sm.Entry{catalogCreate(t, 1, numeric)})
	applyMVCCEntries(t, first, []sm.Entry{catalogCreate(t, 2, lexical)})
	if err := first.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	eng := openCatalogEngine(t, stg)
	eng.indexMu.RLock()
	got, ok := eng.indexValueTypes[catalogIdx]
	eng.indexMu.RUnlock()
	if !ok {
		t.Fatal("the rebuilt index was not restored")
	}
	if want := IndexCreateValueType(lexical); got != want {
		t.Fatalf("restored value type = %v, want the rebuilt %v", got, want)
	}
}

// In standalone the engine is the state machine and keeps serving after a
// failed apply, so an index published (or unpublished) for a batch that
// never committed must not outlive the failure.
func TestMVCCEngine_FailedCommitRestoresTheIndexes(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		stg := &commitFailingStorage{Storage: memory.NewStorage()}
		defer func() { _ = stg.Close() }()
		eng := openCatalogEngine(t, stg)

		stg.fail.Store(true)
		err := eng.Update(context.Background(), []sm.Entry{catalogCreate(t, 1, catalogOpts)}, make([]sm.Result, 1))
		if !errors.Is(err, errCommitRefused) {
			t.Fatalf("Update = %v, want the commit fault", err)
		}
		wantIndexNotFound(t, eng)
	})

	t.Run("drop", func(t *testing.T) {
		stg := &commitFailingStorage{Storage: memory.NewStorage()}
		defer func() { _ = stg.Close() }()
		eng := openCatalogEngine(t, stg)
		applyMVCCEntries(t, eng, []sm.Entry{catalogCreate(t, 1, catalogOpts)})

		stg.fail.Store(true)
		err := eng.Update(context.Background(), []sm.Entry{catalogDrop(t, 2, protocol.IndexDropOptions{})}, make([]sm.Result, 1))
		if !errors.Is(err, errCommitRefused) {
			t.Fatalf("Update = %v, want the commit fault", err)
		}
		if _, err := lookupEmail(eng, "a@example.com"); err != nil {
			t.Fatalf("lookup after a failed drop = %v, want the index still served", err)
		}
		eng.indexMu.RLock()
		gotOwner := eng.indexOwners[catalogIdx]
		eng.indexMu.RUnlock()
		if gotOwner != catalogOpts.Owner {
			t.Fatalf("owner after a failed drop = %q, want %q", gotOwner, catalogOpts.Owner)
		}
	})
}

// wantIndexType fails unless eng serves catalogIdx at value type want.
func wantIndexType(t *testing.T, eng *MVCCEngine, want protocol.IndexCreateOptions) {
	t.Helper()
	eng.indexMu.RLock()
	got, ok := eng.indexValueTypes[catalogIdx]
	eng.indexMu.RUnlock()
	if !ok {
		t.Fatal("the index is not served")
	}
	if w := IndexCreateValueType(want); got != w {
		t.Fatalf("value type = %v, want %v", got, w)
	}
}

// reopenCatalogEngine closes eng and opens a fresh engine over stg, which
// replays nothing: what it serves is what the committed batches left.
func reopenCatalogEngine(t *testing.T, eng *MVCCEngine, stg storage.Storage) *MVCCEngine {
	t.Helper()
	if err := eng.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return openCatalogEngine(t, stg)
}

// The clear and the backfill of an index create or drop are queued in the
// apply batch that writes (or deletes) the catalog record, so a batch that
// fails at Commit -- after every inner write was queued -- leaves the
// definition and its entries exactly as the last committed batch did, on the
// live engine and on one reopened over the storage.
func TestMVCCEngine_FailedIndexBatchKeepsTheCommittedIndex(t *testing.T) {
	// "5.0" is the term 5 to a numeric index and the string "5.0" to a
	// lexical one, so a lookup of "5" tells which terms the index holds.
	numeric := protocol.IndexCreateOptions{Numeric: true}
	lexical := protocol.IndexCreateOptions{}
	populate := func(t *testing.T, eng *MVCCEngine) {
		t.Helper()
		applyMVCCEntries(t, eng, []sm.Entry{
			mvccPutEntry(2, []byte("user/1"), []byte(`{"email":"a@example.com"}`), 0),
			mvccPutEntry(3, []byte("user/2"), []byte(`{"email":"b@example.com"}`), 0),
		})
	}
	populateNumbers := func(t *testing.T, eng *MVCCEngine) {
		t.Helper()
		applyMVCCEntries(t, eng, []sm.Entry{
			mvccPutEntry(2, []byte("user/1"), []byte(`{"email":"5.0"}`), 0),
		})
	}

	t.Run("drop", func(t *testing.T) {
		stg := &commitFailingStorage{Storage: memory.NewStorage()}
		defer func() { _ = stg.Close() }()
		eng := openCatalogEngine(t, stg)
		applyMVCCEntries(t, eng, []sm.Entry{catalogCreate(t, 1, catalogOpts)})
		populate(t, eng)

		stg.fail.Store(true)
		err := eng.Update(context.Background(), []sm.Entry{catalogDrop(t, 4, protocol.IndexDropOptions{})}, make([]sm.Result, 1))
		if !errors.Is(err, errCommitRefused) {
			t.Fatalf("Update = %v, want the commit fault", err)
		}
		stg.fail.Store(false)
		for _, e := range []*MVCCEngine{eng, reopenCatalogEngine(t, eng, stg)} {
			wantIndexType(t, e, catalogOpts)
			wantEmailKeys(t, e, "a@example.com", "user/1")
			wantEmailKeys(t, e, "b@example.com", "user/2")
		}
	})

	t.Run("failed rebuild", func(t *testing.T) {
		stg := &commitFailingStorage{Storage: memory.NewStorage()}
		defer func() { _ = stg.Close() }()
		eng := openCatalogEngine(t, stg)
		applyMVCCEntries(t, eng, []sm.Entry{catalogCreate(t, 1, numeric)})
		populateNumbers(t, eng)
		wantEmailKeys(t, eng, "5", "user/1")

		stg.fail.Store(true)
		err := eng.Update(context.Background(), []sm.Entry{catalogCreate(t, 3, lexical)}, make([]sm.Result, 1))
		if !errors.Is(err, errCommitRefused) {
			t.Fatalf("Update = %v, want the commit fault", err)
		}
		stg.fail.Store(false)
		for _, e := range []*MVCCEngine{eng, reopenCatalogEngine(t, eng, stg)} {
			wantIndexType(t, e, numeric)
			wantEmailKeys(t, e, "5", "user/1")
		}
	})

	t.Run("rebuild", func(t *testing.T) {
		stg := &commitFailingStorage{Storage: memory.NewStorage()}
		defer func() { _ = stg.Close() }()
		eng := openCatalogEngine(t, stg)
		applyMVCCEntries(t, eng, []sm.Entry{catalogCreate(t, 1, numeric)})
		populateNumbers(t, eng)
		applyMVCCEntries(t, eng, []sm.Entry{catalogCreate(t, 3, lexical)})
		for _, e := range []*MVCCEngine{eng, reopenCatalogEngine(t, eng, stg)} {
			wantIndexType(t, e, lexical)
			wantEmailKeys(t, e, "5")
			wantEmailKeys(t, e, "5.0", "user/1")
		}
	})
}

// catalogKeys lists the keys stg holds in the index catalog.
func catalogKeys(t *testing.T, stg storage.Storage) []string {
	t.Helper()
	start, end := index.CatalogRange()
	it := stg.NewIterator(&storage.IteratorOptions{LowerBound: start, UpperBound: end})
	defer func() { _ = it.Close() }()
	var keys []string
	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		keys = append(keys, string(it.Key()))
	}
	if err := it.Error(); err != nil {
		t.Fatalf("iterate the catalog: %v", err)
	}
	return keys
}

// A create proposed before index-catalog is active on the group writes no
// catalog record: the catalog is in replica hashes and snapshots, and a
// member that predates it writes none, so the group's replicas hash alike.
// The index is served as before; a fresh engine over the storage restores
// it only once it is created again under the feature.
func TestMVCCEngine_UncataloguedCreateWritesNoCatalogRecord(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	first := openCatalogEngine(t, stg)
	applyMVCCEntries(t, first, []sm.Entry{uncataloguedCreate(t, 1, catalogOpts)})
	applyMVCCEntries(t, first, []sm.Entry{
		mvccPutEntry(2, []byte("user/1"), []byte(`{"email":"a@example.com"}`), 0),
	})
	wantEmailKeys(t, first, "a@example.com", "user/1")
	if keys := catalogKeys(t, stg); len(keys) != 0 {
		t.Fatalf("an uncatalogued create wrote catalog keys %q", keys)
	}

	eng := reopenCatalogEngine(t, first, stg)
	wantIndexNotFound(t, eng)

	// Created again under the feature, the index is recorded and restored.
	applyMVCCEntries(t, eng, []sm.Entry{catalogCreate(t, 3, catalogOpts)})
	if keys := catalogKeys(t, stg); len(keys) != 1 {
		t.Fatalf("catalog keys after a catalogued create = %q, want one", keys)
	}
	eng = reopenCatalogEngine(t, eng, stg)
	wantEmailKeys(t, eng, "a@example.com", "user/1")
}

// A catalogued definition replaced by an uncatalogued rebuild leaves no
// record behind, so a fresh engine never restores the definition the log
// has since replaced.
func TestMVCCEngine_UncataloguedRebuildClearsTheRecord(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := openCatalogEngine(t, stg)
	applyMVCCEntries(t, eng, []sm.Entry{catalogCreate(t, 1, protocol.IndexCreateOptions{Numeric: true})})
	applyMVCCEntries(t, eng, []sm.Entry{uncataloguedCreate(t, 2, protocol.IndexCreateOptions{})})
	if keys := catalogKeys(t, stg); len(keys) != 0 {
		t.Fatalf("catalog keys after an uncatalogued rebuild = %q, want none", keys)
	}
}
