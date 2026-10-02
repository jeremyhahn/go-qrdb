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
	"log/slog"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// clusterNode is one node's database Manager wired as a cluster composition
// wires it: its own data directory, its own allocator, and the replicated
// system store every node shares.
func clusterNode(t *testing.T, replicated kvstore.AtomicKVStore) *Manager {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	m := NewManager(logger, t.TempDir(), WithAllocator(shard.NewAllocator()))
	claimer, err := shard.NewClaimer(replicated, nil)
	if err != nil {
		t.Fatalf("shard.NewClaimer: %v", err)
	}
	m.SetShardIDClaimer(claimer)
	t.Cleanup(func() { _ = m.Close() })
	return m
}

// TestShardIDs_NeverCollideAcrossNodes is the regression for per-node shard
// ID allocation. Every node's allocator started at shard.UserMin and advanced
// only past IDs THIS node had issued, so two nodes each creating a raft
// database were each handed the same shard ID -- two different Raft groups
// under one ID in a cluster whose placement table is keyed by database, not
// by shard. A database dropped on one node and created again through another
// could likewise be handed the dropped incarnation's ID, which the drop fence
// relies on never happening.
func TestShardIDs_NeverCollideAcrossNodes(t *testing.T) {
	replicated := kvstore.NewMemoryKVStore()
	a := clusterNode(t, replicated)
	b := clusterNode(t, replicated)

	seen := make(map[uint64]string)
	for i, node := range []*Manager{a, b, a, b} {
		name := []string{"orders", "ledger", "events", "audit"}[i]
		db, err := node.Create(DatabaseConfig{
			Name:    name,
			Engine:  EngineKV,
			Backend: BackendMemory,
			Raft:    validRaftConfig(0),
		})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		id := db.ShardID()
		if prev, dup := seen[id]; dup {
			t.Fatalf("shard ID %d issued to both %q and %q: shard IDs are unique per node only", id, prev, name)
		}
		seen[id] = name
	}
}

// TestShardIDs_ProvisionedDatabasesStayNodeLocal pins the one exception: a
// database every node provisions from its configuration must get the same
// shard ID on every node, which only the deterministic local allocator gives.
func TestShardIDs_ProvisionedDatabasesStayNodeLocal(t *testing.T) {
	replicated := kvstore.NewMemoryKVStore()
	a := clusterNode(t, replicated)
	b := clusterNode(t, replicated)
	cfg := func() DatabaseConfig {
		return DatabaseConfig{Name: "configured", Engine: EngineKV, Backend: BackendMemory, Raft: validRaftConfig(0)}
	}
	da, err := a.Provision(context.Background(), cfg())
	if err != nil {
		t.Fatalf("provision on a: %v", err)
	}
	db, err := b.Provision(context.Background(), cfg())
	if err != nil {
		t.Fatalf("provision on b: %v", err)
	}
	if da.ShardID() != db.ShardID() {
		t.Fatalf("the same provisioned database got shard %d on a and %d on b", da.ShardID(), db.ShardID())
	}
	if raw, _ := replicated.Get(context.Background(), shard.HighWaterKey); raw != nil {
		t.Fatalf("provisioning claimed a cluster-wide ID (high-water %q)", raw)
	}
	// An API create after it never takes the provisioned ID.
	created, err := a.Create(DatabaseConfig{Name: "created", Engine: EngineKV, Backend: BackendMemory, Raft: validRaftConfig(0)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ShardID() == da.ShardID() {
		t.Fatalf("created database reused provisioned shard %d", da.ShardID())
	}
}

// TestShardIDs_ClaimMovesTheLocalAllocator: an ID the cluster issued to this
// node is never issued by this node's allocator afterwards.
func TestShardIDs_ClaimMovesTheLocalAllocator(t *testing.T) {
	replicated := kvstore.NewMemoryKVStore()
	if err := replicated.Put(context.Background(), shard.HighWaterKey, []byte("7000")); err != nil {
		t.Fatal(err)
	}
	a := clusterNode(t, replicated)
	db, err := a.Create(DatabaseConfig{Name: "far", Engine: EngineKV, Backend: BackendMemory, Raft: validRaftConfig(0)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if db.ShardID() != 7000 {
		t.Fatalf("shard = %d, want 7000 from the cluster mark", db.ShardID())
	}
	if next := a.allocator.Next(); next != 7001 {
		t.Fatalf("local allocator next = %d, want 7001", next)
	}
}

// failingClaimer refuses every claim, as a claimer over an unreachable
// system store does.
type failingClaimer struct{ err error }

func (f failingClaimer) ClaimShardID(context.Context, uint64) (uint64, error) { return 0, f.err }

// TestShardIDs_FailedClaimCreatesNothing: a create that cannot claim an ID
// refuses with a typed error and leaves no catalog entry behind -- it never
// falls back to a node-local ID that another node may already hold.
func TestShardIDs_FailedClaimCreatesNothing(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	m := NewManager(logger, t.TempDir(), WithAllocator(shard.NewAllocator()))
	t.Cleanup(func() { _ = m.Close() })
	storeDown := errors.New("system store unavailable")
	m.SetShardIDClaimer(failingClaimer{err: storeDown})

	_, err := m.Create(DatabaseConfig{Name: "refused", Engine: EngineKV, Backend: BackendMemory, Raft: validRaftConfig(0)})
	var claimErr *ShardIDClaimError
	if !errors.As(err, &claimErr) || !errors.Is(err, storeDown) {
		t.Fatalf("create with a failing claimer = %v, want *ShardIDClaimError wrapping the store error", err)
	}
	if claimErr.Error() == "" {
		t.Fatal("empty error text")
	}
	if _, err := m.Get("refused"); err == nil {
		t.Fatal("a refused create left a catalog entry")
	}
	if _, err := os.Stat(m.dbDir("refused")); !os.IsNotExist(err) {
		t.Fatalf("a refused create left its directory: %v", err)
	}
	if next := m.allocator.Next(); next != shard.UserMin {
		t.Fatalf("a refused create consumed a local ID: next = %d", next)
	}
}
