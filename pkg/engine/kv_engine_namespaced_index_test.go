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
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// createScopedIndex applies an index-create command carrying a declared key
// prefix, exactly as the Raft path receives one from the index service.
func createScopedIndex(t *testing.T, eng *KVEngine, raftIndex uint64, name, field, keyPrefix string) {
	t.Helper()
	applyKVEntries(t, eng, []sm.Entry{{
		Index: raftIndex,
		Cmd: mustEncode(protocol.EncodeIndexCreateOpts(name, field, protocol.IndexCreateOptions{
			KeyPrefix: keyPrefix,
		})),
	}})
}

// TestKVEngine_PutWithIndexes_MaintainsNamespacedTenantIndex is the engine
// half of go-qrdb#40's priority 1. A shared-prefix tenant's store confines
// every key to "{tenant}/" and names its indexes "{tenant}/{entity}.{Field}".
// PutWithIndexes used to take the key's first path segment as the entity type
// -- "acme" for "acme/workitem/1" -- and keep only indexes NAMED "acme.", so
// the tenant's index was never maintained. It now selects by scope, the same
// rule the Raft apply path uses.
func TestKVEngine_PutWithIndexes_MaintainsNamespacedTenantIndex(t *testing.T) {
	eng := newScopedKVEngine(t)
	createScopedIndex(t, eng, 1, "acme/workitem.State", "state", "acme/workitem/")
	createScopedIndex(t, eng, 2, "globex/workitem.State", "state", "globex/workitem/")

	if err := eng.PutWithIndexes([]byte("acme/workitem/1"), []byte(`{"state":"ready"}`)); err != nil {
		t.Fatalf("PutWithIndexes(acme): %v", err)
	}
	if err := eng.PutWithIndexes([]byte("globex/workitem/2"), []byte(`{"state":"ready"}`)); err != nil {
		t.Fatalf("PutWithIndexes(globex): %v", err)
	}

	assertKVIndexed(t, eng, "acme/workitem.State", "acme/workitem/1")
	assertKVIndexed(t, eng, "globex/workitem.State", "globex/workitem/2")
}

// TestKVEngine_PutWithIndexes_MaintainsPerDatabaseIndex covers the
// "{tenant}/{database}/" namespace the transports compose: the name carries
// the namespace and the declared prefix is relative to it.
func TestKVEngine_PutWithIndexes_MaintainsPerDatabaseIndex(t *testing.T) {
	eng := newScopedKVEngine(t)
	createScopedIndex(t, eng, 1, "acme/sales/workitem.State", "state", "workitem/")

	for _, k := range []string{"acme/sales/workitem/1", "acme/hr/workitem/2", "globex/sales/workitem/3"} {
		if err := eng.PutWithIndexes([]byte(k), []byte(`{"state":"ready"}`)); err != nil {
			t.Fatalf("PutWithIndexes(%s): %v", k, err)
		}
	}

	assertKVIndexed(t, eng, "acme/sales/workitem.State", "acme/sales/workitem/1")
}

// TestKVEngine_DeleteWithIndexes_RetractsNamespacedEntry is the delete half:
// a namespaced key's entry is removed from its own index and no other.
func TestKVEngine_DeleteWithIndexes_RetractsNamespacedEntry(t *testing.T) {
	eng := newScopedKVEngine(t)
	createScopedIndex(t, eng, 1, "acme/workitem.State", "state", "acme/workitem/")
	createScopedIndex(t, eng, 2, "globex/workitem.State", "state", "globex/workitem/")

	for _, k := range []string{"acme/workitem/1", "globex/workitem/1"} {
		if err := eng.PutWithIndexes([]byte(k), []byte(`{"state":"ready"}`)); err != nil {
			t.Fatalf("PutWithIndexes(%s): %v", k, err)
		}
	}
	if err := eng.DeleteWithIndexes([]byte("acme/workitem/1")); err != nil {
		t.Fatalf("DeleteWithIndexes: %v", err)
	}

	assertKVIndexed(t, eng, "acme/workitem.State")
	assertKVIndexed(t, eng, "globex/workitem.State", "globex/workitem/1")
}

// TestKVEngine_PutWithIndexes_DoesNotCrossEntityTypes keeps the property the
// name filter used to provide: an entity index registered on the engine
// indexes only its own entity type's keys.
func TestKVEngine_PutWithIndexes_DoesNotCrossEntityTypes(t *testing.T) {
	eng := newScopedKVEngine(t)
	if err := eng.RegisterEntityType("users", []EntityIndex{{FieldName: "Email", JSONFieldName: "email", CaseSensitive: true}}); err != nil {
		t.Fatalf("RegisterEntityType users: %v", err)
	}
	if err := eng.RegisterEntityType("orders", []EntityIndex{{FieldName: "Email", JSONFieldName: "email", CaseSensitive: true}}); err != nil {
		t.Fatalf("RegisterEntityType orders: %v", err)
	}

	if err := eng.PutWithIndexes([]byte("users/1"), []byte(`{"email":"a@example.com"}`)); err != nil {
		t.Fatalf("PutWithIndexes: %v", err)
	}

	assertKVIndexed(t, eng, "users.Email", "users/1")
	assertKVIndexed(t, eng, "orders.Email")
}
