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
)

// clusterNode is one node's database Manager wired as a cluster composition
// wires it: its own data directory, its own allocator, and the replicated
// system store every node shares.
func clusterNode(t *testing.T, replicated kvstore.AtomicKVStore) *Manager {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	m := NewManager(logger, t.TempDir())
	m.SetShardIDClaimer(claimerOver(t, replicated))
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

// restoreInto copies every record of from into a fresh store: what a restore
// of the replicated store from a backup produces.
func restoreInto(t *testing.T, from kvstore.KVStore) kvstore.AtomicKVStore {
	t.Helper()
	to := kvstore.NewMemoryKVStore()
	if err := from.Scan(context.Background(), "", func(key string, value []byte) error {
		return to.Put(context.Background(), key, value)
	}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	return to
}

// TestShardIDs_RestoreToAFreshDataDirNeverReissues: a shard ID's issue is
// recorded in the replicated store and nowhere else, so a node rebuilt from a
// backup of that store, on an empty data directory, continues past every ID
// the cluster issued -- including those of databases since dropped.
func TestShardIDs_RestoreToAFreshDataDirNeverReissues(t *testing.T) {
	original := kvstore.NewMemoryKVStore()
	before := clusterNode(t, original)
	issued := make(map[uint64]string)
	for _, name := range []string{"kept", "dropped"} {
		db, err := before.Create(DatabaseConfig{Name: name, Engine: EngineKV, Backend: BackendMemory, Raft: validRaftConfig(0)})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		issued[db.ShardID()] = name
	}
	if err := before.Drop(context.Background(), "dropped"); err != nil {
		t.Fatalf("drop: %v", err)
	}

	// A fresh data directory: nothing of the old node's disk survives.
	after := clusterNode(t, restoreInto(t, original))
	for _, name := range []string{"dropped", "new"} {
		db, err := after.Create(DatabaseConfig{Name: name, Engine: EngineKV, Backend: BackendMemory, Raft: validRaftConfig(0)})
		if err != nil {
			t.Fatalf("create %s after restore: %v", name, err)
		}
		if prev, reused := issued[db.ShardID()]; reused {
			t.Fatalf("after a restore to a fresh data dir, %q got shard %d, already issued to %q", name, db.ShardID(), prev)
		}
	}
}

// TestShardIDs_NoNodeLocalShardState: issuing a shard ID writes nothing to the
// node's data directory. Everything a create leaves there is the database's
// own catalog entry and data.
func TestShardIDs_NoNodeLocalShardState(t *testing.T) {
	m := clusterNode(t, kvstore.NewMemoryKVStore())
	for _, name := range []string{"a", "b"} {
		if _, err := m.Create(DatabaseConfig{Name: name, Engine: EngineKV, Backend: BackendMemory, Raft: validRaftConfig(0)}); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	entries, err := os.ReadDir(m.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != databasesDir {
			t.Fatalf("a raft create left %q in the node's data directory; shard IDs must live only in the replicated store", e.Name())
		}
	}
}

// failingClaimer refuses every claim, as a claimer over an unreachable
// system store does.
type failingClaimer struct{ err error }

func (f failingClaimer) ClaimShardID(context.Context) (uint64, error) { return 0, f.err }

// TestShardIDs_FailedClaimCreatesNothing: a create that cannot claim an ID
// refuses with a typed error and leaves no catalog entry behind -- it never
// falls back to a node-local ID that another node may already hold.
func TestShardIDs_FailedClaimCreatesNothing(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	storeDown := errors.New("system store unavailable")
	m := NewManager(logger, t.TempDir(), WithShardIDClaimer(failingClaimer{err: storeDown}))
	t.Cleanup(func() { _ = m.Close() })

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
}
