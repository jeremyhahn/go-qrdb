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

func catalogCreate(t *testing.T, entry uint64, opts protocol.IndexCreateOptions) sm.Entry {
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
