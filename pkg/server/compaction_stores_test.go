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
