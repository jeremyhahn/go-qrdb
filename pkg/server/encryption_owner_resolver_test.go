// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/placement"
)

// fakeTopology is a static placement.TopologyProvider for owner-resolver tests.
type fakeTopology struct{ nodes []placement.NodeLoad }

func (f fakeTopology) Nodes() []placement.NodeLoad { return f.nodes }

const ownerSystemShard = 1

// TestOwnerResolver_SystemScope returns the full roster (deduped) for the system
// barrier and the system shard.
func TestOwnerResolver_SystemScope(t *testing.T) {
	topo := fakeTopology{nodes: []placement.NodeLoad{
		{NodeID: "n1"}, {NodeID: "n2"}, {NodeID: "n2"}, {NodeID: ""}, {NodeID: "n3"},
	}}
	r := newClusterOwnerResolver(nil, topo, ownerSystemShard, "n1")

	shard, owners, err := r.Owners(context.Background(), "", "")
	if err != nil {
		t.Fatalf("Owners(system): %v", err)
	}
	if shard != ownerSystemShard {
		t.Fatalf("system shard = %d, want %d", shard, ownerSystemShard)
	}
	sort.Strings(owners)
	if len(owners) != 3 || owners[0] != "n1" || owners[1] != "n2" || owners[2] != "n3" {
		t.Fatalf("system owners = %v, want deduped [n1 n2 n3]", owners)
	}
}

// TestOwnerResolver_SystemScope_NoTopologyFallsBackLocal proves a missing roster
// falls back to the local node rather than an empty owner set.
func TestOwnerResolver_SystemScope_NoTopologyFallsBackLocal(t *testing.T) {
	r := newClusterOwnerResolver(nil, nil, ownerSystemShard, "self")
	_, owners, err := r.Owners(context.Background(), "", "")
	if err != nil || len(owners) != 1 || owners[0] != "self" {
		t.Fatalf("Owners(system,no-topo) = %v, %v; want [self]", owners, err)
	}
}

// TestOwnerResolver_TenantScope returns the placed replicas and the placement
// shard.
func TestOwnerResolver_TenantScope(t *testing.T) {
	ctx := context.Background()
	store, err := placement.NewStore(newMemKV())
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	if perr := store.Put(ctx, &placement.Placement{
		TenantID: "acme", DatabaseID: "appdata", Database: "appdata", ShardID: 42,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "n1"}, {ReplicaID: 2, NodeID: "n2"}, {ReplicaID: 3, NodeID: "n3"},
		},
	}); perr != nil {
		t.Fatalf("placement.Put: %v", perr)
	}
	r := newClusterOwnerResolver(store, nil, ownerSystemShard, "n1")

	shard, owners, err := r.Owners(ctx, "acme", "appdata")
	if err != nil {
		t.Fatalf("Owners(tenant): %v", err)
	}
	if shard != 42 {
		t.Fatalf("tenant shard = %d, want 42", shard)
	}
	sort.Strings(owners)
	if len(owners) != 3 || owners[0] != "n1" || owners[2] != "n3" {
		t.Fatalf("tenant owners = %v, want [n1 n2 n3]", owners)
	}
}

// TestOwnerResolver_WholeTenantScope unions replicas across all the tenant's
// databases (empty database = tenant-barrier migration covers every db).
func TestOwnerResolver_WholeTenantScope(t *testing.T) {
	ctx := context.Background()
	store, err := placement.NewStore(newMemKV())
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	if perr := store.Put(ctx, &placement.Placement{
		TenantID: "acme", DatabaseID: "db1", Database: "db1", ShardID: 10,
		Replicas: []placement.Replica{{ReplicaID: 1, NodeID: "n1"}, {ReplicaID: 2, NodeID: "n2"}},
	}); perr != nil {
		t.Fatalf("Put db1: %v", perr)
	}
	if perr := store.Put(ctx, &placement.Placement{
		TenantID: "acme", DatabaseID: "db2", Database: "db2", ShardID: 11,
		Replicas: []placement.Replica{{ReplicaID: 1, NodeID: "n2"}, {ReplicaID: 2, NodeID: "n3"}},
	}); perr != nil {
		t.Fatalf("Put db2: %v", perr)
	}
	r := newClusterOwnerResolver(store, nil, ownerSystemShard, "n1")

	shard, owners, err := r.Owners(ctx, "acme", "")
	if err != nil {
		t.Fatalf("Owners(whole-tenant): %v", err)
	}
	if shard != 0 {
		t.Fatalf("whole-tenant shard = %d, want 0 (no single shard)", shard)
	}
	sort.Strings(owners)
	if len(owners) != 3 || owners[0] != "n1" || owners[1] != "n2" || owners[2] != "n3" {
		t.Fatalf("whole-tenant owners = %v, want deduped union [n1 n2 n3]", owners)
	}
}

// TestOwnerResolver_TenantScope_NoPlacementFallsBackLocal proves a meta-managed
// single-node database (no placement entry) migrates on the local node alone.
func TestOwnerResolver_TenantScope_NoPlacementFallsBackLocal(t *testing.T) {
	ctx := context.Background()
	store, err := placement.NewStore(newMemKV())
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	r := newClusterOwnerResolver(store, nil, ownerSystemShard, "self")

	shard, owners, err := r.Owners(ctx, "acme", "nodb")
	if err != nil {
		t.Fatalf("Owners(no-placement): %v", err)
	}
	if shard != 0 || len(owners) != 1 || owners[0] != "self" {
		t.Fatalf("Owners(no-placement) = shard %d %v, want shard 0 [self]", shard, owners)
	}
}

// TestOwnerResolver_TenantScope_NilStoreFallsBackLocal covers the no-placement-
// configured deployment.
func TestOwnerResolver_TenantScope_NilStoreFallsBackLocal(t *testing.T) {
	r := newClusterOwnerResolver(nil, nil, ownerSystemShard, "self")
	_, owners, err := r.Owners(context.Background(), "acme", "db")
	if err != nil || len(owners) != 1 || owners[0] != "self" {
		t.Fatalf("Owners(nil-store) = %v, %v; want [self]", owners, err)
	}
}

// TestNewClusterOwnerResolver_Exported covers the exported daemon constructor and
// confirms it resolves the same way as the internal one.
func TestNewClusterOwnerResolver_Exported(t *testing.T) {
	r := NewClusterOwnerResolver(nil, fakeTopology{nodes: []placement.NodeLoad{{NodeID: "n1"}, {NodeID: "n2"}}}, ownerSystemShard, "n1")
	shard, owners, err := r.Owners(context.Background(), "", "")
	if err != nil || shard != ownerSystemShard || len(owners) != 2 {
		t.Fatalf("NewClusterOwnerResolver Owners(system) = shard %d %v, %v", shard, owners, err)
	}
}

// TestOwnerResolver_EmptyLocalNodeID proves the local-only fallback yields an
// empty owner set when the local node id is unknown (the coordinator then rejects
// with ErrNoOwners).
func TestOwnerResolver_EmptyLocalNodeID(t *testing.T) {
	r := newClusterOwnerResolver(nil, nil, ownerSystemShard, "")
	if _, owners, _ := r.Owners(context.Background(), "acme", "db"); len(owners) != 0 {
		t.Fatalf("Owners(no local id) = %v, want empty", owners)
	}
	if _, owners, _ := r.Owners(context.Background(), "", ""); len(owners) != 0 {
		t.Fatalf("Owners(system,no local id,no topo) = %v, want empty", owners)
	}
}

// TestOwnerResolver_TenantScope_StoreErrorPropagated proves a non-NotFound
// placement error surfaces rather than silently falling back to local.
func TestOwnerResolver_TenantScope_StoreErrorPropagated(t *testing.T) {
	store, err := placement.NewStore(errKVScan{})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	r := newClusterOwnerResolver(store, nil, ownerSystemShard, "self")
	if _, _, oerr := r.Owners(context.Background(), "acme", "db"); oerr == nil {
		t.Fatal("Owners with a failing placement store must surface the error")
	}
}

// TestOwnerResolver_WholeTenant_StoreErrorPropagated proves a ListForTenant
// failure surfaces an error rather than silently narrowing to local-only (which
// would under-migrate a placed multi-replica tenant).
func TestOwnerResolver_WholeTenant_StoreErrorPropagated(t *testing.T) {
	store, err := placement.NewStore(errKVScan{})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	r := newClusterOwnerResolver(store, nil, ownerSystemShard, "self")
	if _, owners, oerr := r.Owners(context.Background(), "acme", ""); oerr == nil {
		t.Fatalf("Owners(whole-tenant, failing store) = %v, nil; want an error not local fallback", owners)
	}
}

// errKVScan is a placement.KV whose ops fail (non-NotFound), used to prove error
// propagation in the owner resolver.
type errKVScan struct{}

func (errKVScan) Put(context.Context, string, []byte) error   { return errOwnerKV }
func (errKVScan) Get(context.Context, string) ([]byte, error) { return nil, errOwnerKV }
func (errKVScan) Delete(context.Context, string) error        { return errOwnerKV }
func (errKVScan) Scan(context.Context, string, func(string, []byte) error) error {
	return errOwnerKV
}

var errOwnerKV = errors.New("owner kv down")
