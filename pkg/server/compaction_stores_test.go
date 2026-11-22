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

package server

import (
	"context"
	"sort"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// leaderlessView is a compaction-capable MVCC view that cannot report
// leadership: it has the compaction table and prefix compaction, and no
// LeadsShard.
type leaderlessView struct {
	service.MVCCKVServicer
	service.CompactionStateStore
	service.PrefixCompactor
}

// plainMVCCView is an MVCC service with no compaction table at all.
type plainMVCCView struct{ service.MVCCKVServicer }

// TestServer_CompactionStores_Gating: a volume tenant's Host is node-local
// and runs ungated; a shared store that cannot report leadership is run by
// no node rather than by every node; a service with no compaction table is
// left out; and a started raft group whose services cannot be resolved is
// skipped without failing the rest.
func TestServer_CompactionStores_Gating(t *testing.T) {
	shared := newSchedStore(1)
	vol := newSchedStore(1)

	reg := NewDatabaseServiceRegistry("default")
	reg.Register("default", &DatabaseServices{Name: "default", MVCC: &leaderlessView{CompactionStateStore: shared, PrefixCompactor: shared}})
	reg.Register("plain", &DatabaseServices{Name: "plain", MVCC: &plainMVCCView{}})

	hosts := &TenantHostRegistry{hosts: map[string]*tenantHost{
		"vol-up": {services: &DatabaseServices{MVCC: &schedMVCCView{schedStore: vol}}},
		"empty":  {services: nil},
	}}
	raft := &RaftBackendFactory{shards: map[raftShardKey]*raftShard{
		{tenantID: "acme", database: "orders"}: {tenantID: "acme", database: "orders"},
	}}

	srv := New(testConfig(t, false, false), newMockKVService(), &mockAdminService{},
		WithDatabaseServiceRegistry(reg), WithTenantHostRegistry(hosts), WithRaftBackendFactory(raft))
	stores := srv.compactionStores(context.Background())

	names := make([]string, len(stores))
	for i, st := range stores {
		names[i] = st.name
		if st.name == "tenant host vol-up" && st.leader != nil {
			t.Fatal("a volume tenant's node-local Host was gated by leadership")
		}
	}
	sort.Strings(names)
	if len(names) != 1 || names[0] != "tenant host vol-up" {
		t.Fatalf("stores = %v, want only the volume tenant's Host", names)
	}
}

// A database the Manager opened in its own store is compacted on its own
// host: the shared bundles never reach it, so it is listed by itself and
// ungated (a standalone node is its only replica). A system-tenant database
// on the shared store is not listed again.
func TestCompactionStores_IncludeMaterializedDatabases(t *testing.T) {
	factory := &fakeMaterializingFactory{}
	dbMgr := databasetest.NewManager(nil, t.TempDir(), database.WithBackendFactory(factory))
	t.Cleanup(func() { _ = dbMgr.Close() })
	if _, err := dbMgr.Create(database.DatabaseConfig{TenantID: "acme", Name: "orders", Backend: database.BackendMemory}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := dbMgr.Create(database.DatabaseConfig{Name: "plain", Backend: database.BackendMemory}); err != nil {
		t.Fatalf("Create plain: %v", err)
	}

	srv := New(testConfig(t, false, false), newMockKVService(), &mockAdminService{}, WithDatabaseManager(dbMgr))
	var names []string
	for _, st := range srv.compactionStores(context.Background()) {
		names = append(names, st.name)
		if st.name == "materialized acme/orders" && st.leader != nil {
			t.Fatal("a materialized database's store was gated by leadership")
		}
	}
	sort.Strings(names)
	found := false
	for _, n := range names {
		if n == "materialized acme/orders" {
			found = true
		}
		if n == "materialized /plain" || n == "materialized plain" {
			t.Fatalf("a shared-store database was listed as materialized: %v", names)
		}
	}
	if !found {
		t.Fatalf("stores = %v, want one named %q", names, "materialized acme/orders")
	}
}
