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

package lifecycle

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// newAdapterOverRealEngine wires the adapter the way a booted node does:
// storage -> KVEngine -> LocalBackend -> KVService/IndexService -> adapter.
//
// Every other test of this adapter drives a mockIndexServicer, which can only
// show what the adapter ASKED for. That is what let go-qrdb#34 and #38 hide:
// the adapter's request was fine and the engine did something else with it.
// These tests assert what the engine ANSWERS.
func newAdapterOverRealEngine(t *testing.T) kvstore.KVStore {
	t.Helper()
	stg := memory.NewStorage()
	t.Cleanup(func() { _ = stg.Close() })

	host := backend.NewLocalBackend(engine.NewKVEngine(stg))
	return newKVStoreAdapter(
		service.NewKVService(host, 0),
		service.NewIndexService(host, 0),
	)
}

// TestKVStoreAdapter_CaseInsensitiveIndexMatchesOverARealEngine is the
// verification go-qrdb#34 names: an entity registered with a case-insensitive
// index definition must be found by a differently-cased probe, asserting a
// NON-EMPTY result.
//
// Before the flag reached the engine this could not even be attempted -- the
// adapter refused the definition with kvstore.ErrIndexUnsupported, because
// accepting it would have created a case-SENSITIVE index and answered this
// query empty with a nil error.
func TestKVStoreAdapter_CaseInsensitiveIndexMatchesOverARealEngine(t *testing.T) {
	store := newAdapterOverRealEngine(t)
	ctx := context.Background()

	err := store.RegisterEntityIndexes("member", []kvstore.EntityIndex{
		{FieldName: "Team", JSONFieldName: "team", CaseSensitive: false},
	})
	if err != nil {
		t.Fatalf("RegisterEntityIndexes: %v", err)
	}

	if err := store.Put(ctx, "member/1", []byte(`{"team":"Alpha"}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	for _, probe := range []string{"Alpha", "alpha", "ALPHA"} {
		keys, qErr := store.QueryIndex(ctx, "member.Team", []byte(probe))
		if qErr != nil {
			t.Fatalf("QueryIndex(%q): %v", probe, qErr)
		}
		if len(keys) == 0 {
			t.Errorf("QueryIndex(%q) returned EMPTY with a nil error over an index "+
				"holding the match -- the silent-empty defect go-qrdb#34 removes", probe)
		}
	}

	// A term the index does not hold must still miss, or the assertions above
	// would pass against an index that matches everything.
	keys, err := store.QueryIndex(ctx, "member.Team", []byte("beta"))
	if err != nil {
		t.Fatalf("QueryIndex(beta): %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("QueryIndex(beta) matched %d keys; want 0", len(keys))
	}
}

// TestKVStoreAdapter_OrdinaryIndexStaysCaseSensitiveOverARealEngine is the
// control and the polarity guard, over the same real engine.
//
// index.IndexConfig names CaseSensitive for the DEFAULT, so a config left at
// its zero value folds case. If the adapter forwarded an unset field, or the
// engine derived the wrong side of the flag, every ordinary index in every
// deployment would silently become case-insensitive -- and no existing test
// would fail, because folding only ever ADDS matches.
func TestKVStoreAdapter_OrdinaryIndexStaysCaseSensitiveOverARealEngine(t *testing.T) {
	store := newAdapterOverRealEngine(t)
	ctx := context.Background()

	if err := store.RegisterEntityIndexes("member", memberIndexDefs()); err != nil {
		t.Fatalf("RegisterEntityIndexes: %v", err)
	}
	if err := store.Put(ctx, "member/1", []byte(`{"team":"Alpha"}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	keys, err := store.QueryIndex(ctx, "member.Team", []byte("Alpha"))
	if err != nil {
		t.Fatalf("QueryIndex(Alpha): %v", err)
	}
	if len(keys) == 0 {
		t.Fatal("QueryIndex(Alpha) returned empty over an ordinary index holding it")
	}

	keys, err = store.QueryIndex(ctx, "member.Team", []byte("alpha"))
	if err != nil {
		t.Fatalf("QueryIndex(alpha): %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("a differently-cased probe matched %d keys on an ordinary index; "+
			"want 0 -- the default must stay case-SENSITIVE", len(keys))
	}
}
