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

package database

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// TestShardIDs_CallerChosenIDIsRefused is the review's probe. Node A claims
// shard 1000 for x; node B is then asked to create y naming shard 1000, and z
// naming shard 1 -- the system shard. A caller-chosen ID bypassed the claim
// entirely and was recorded in node B's own ledger alone, so both creates
// succeeded: y shared x's Raft group, and z claimed the reserved range, across
// tenants. Shard IDs are issued by the cluster; a create may not name one.
func TestShardIDs_CallerChosenIDIsRefused(t *testing.T) {
	replicated := kvstore.NewMemoryKVStore()
	a := clusterNode(t, replicated)
	b := clusterNode(t, replicated)

	x, err := a.Create(DatabaseConfig{Name: "x", Backend: BackendMemory, Raft: validRaftConfig(0)})
	if err != nil {
		t.Fatalf("create x: %v", err)
	}
	for _, tc := range []struct {
		name string
		id   uint64
	}{{"y", x.ShardID()}, {"z", shard.ShardKV}} {
		_, err := b.Create(DatabaseConfig{Name: tc.name, Backend: BackendMemory, Raft: validRaftConfig(tc.id)})
		var refused *CallerShardIDError
		if !errors.As(err, &refused) {
			t.Fatalf("create %s naming shard %d = %v, want *CallerShardIDError", tc.name, tc.id, err)
		}
		if refused.ShardID != tc.id || refused.Name != tc.name || refused.Error() == "" {
			t.Fatalf("refusal = %+v", refused)
		}
		if _, gerr := b.Get(tc.name); gerr == nil {
			t.Fatalf("refused create of %s left a catalog entry", tc.name)
		}
	}
}

// TestShardIDs_NoIssuerRefusesARaftCreate: a Manager with no claimer has no
// way to issue a shard ID, and no longer falls back to one the caller names,
// so it refuses instead of creating a raft database on shard 0.
func TestShardIDs_NoIssuerRefusesARaftCreate(t *testing.T) {
	m := NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = m.Close() })
	_, err := m.Create(DatabaseConfig{Name: "orphan", Backend: BackendMemory, Raft: validRaftConfig(0)})
	var refused *ShardIssuerRequiredError
	if !errors.As(err, &refused) || refused.Error() == "" {
		t.Fatalf("raft create with no issuer = %v, want *ShardIssuerRequiredError", err)
	}
	if _, gerr := m.Get("orphan"); gerr == nil {
		t.Fatal("a refused create left a catalog entry")
	}
	// A database with no Raft config needs no shard ID and is unaffected.
	if _, err := m.Create(DatabaseConfig{Name: "plain", Backend: BackendMemory}); err != nil {
		t.Fatalf("non-raft create: %v", err)
	}
}

// TestShardIDs_ProvisionRefusesARaftDatabase: no shipped path provisions a
// raft database, and a provisioned one could only take a node-local shard ID,
// which nodes do not agree on. Provision refuses one instead.
func TestShardIDs_ProvisionRefusesARaftDatabase(t *testing.T) {
	m := clusterNode(t, kvstore.NewMemoryKVStore())
	_, err := m.Provision(context.Background(),
		DatabaseConfig{Name: "configured", Backend: BackendMemory, Raft: validRaftConfig(0)})
	var refused *ProvisionRaftError
	if !errors.As(err, &refused) || refused.Name != "configured" || refused.Error() == "" {
		t.Fatalf("Provision with a Raft config = %v, want *ProvisionRaftError", err)
	}
	if _, gerr := m.Get("configured"); gerr == nil {
		t.Fatal("a refused provision left a catalog entry")
	}

	// A database with no Raft config still provisions.
	if _, err := m.Provision(context.Background(),
		DatabaseConfig{Name: "plain", Backend: BackendMemory}); err != nil {
		t.Fatalf("Provision without a Raft config: %v", err)
	}
}

// TestShardIDs_UpdateRaftCannotChangeTheShard: an update that names a
// different shard ID would move a live database onto another group's ID.
// The shard a database was issued is its identity and never changes.
func TestShardIDs_UpdateRaftCannotChangeTheShard(t *testing.T) {
	m := clusterNode(t, kvstore.NewMemoryKVStore())
	db, err := m.Create(DatabaseConfig{Name: "fixed", Backend: BackendMemory, Raft: validRaftConfig(0)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	issued := db.ShardID()

	err = m.UpdateRaft("fixed", &DatabaseRaftConfig{ClusterMode: RaftNone, ShardID: issued + 7})
	var refused *CallerShardIDError
	if !errors.As(err, &refused) {
		t.Fatalf("UpdateRaft naming another shard = %v, want *CallerShardIDError", err)
	}

	// An update that leaves the shard unset keeps the issued one.
	if err := m.UpdateRaft("fixed", &DatabaseRaftConfig{ClusterMode: RaftNone}); err != nil {
		t.Fatalf("UpdateRaft: %v", err)
	}
	got, err := m.Get("fixed")
	if err != nil {
		t.Fatal(err)
	}
	if got.ShardID() != issued {
		t.Fatalf("shard after update = %d, want the issued %d", got.ShardID(), issued)
	}
}
