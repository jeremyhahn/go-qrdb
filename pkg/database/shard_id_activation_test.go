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
	"errors"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// TestShardIDs_RaftCreateWaitsForClusterShardIDs: until the cluster activates
// cluster-wide shard IDs, a raft database create is refused with the typed
// not-active error and leaves nothing behind -- a rolling upgrade cannot
// create one while an old node may still allocate per node. Once active, the
// same create succeeds.
func TestShardIDs_RaftCreateWaitsForClusterShardIDs(t *testing.T) {
	var active atomic.Bool
	claimer, err := shard.NewClaimer(kvstore.NewMemoryKVStore(),
		shard.WithActivation(featuregate.ClusterShardIDs, active.Load))
	if err != nil {
		t.Fatalf("shard.NewClaimer: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	m := newCatalogManager(logger, t.TempDir())
	m.SetShardIDClaimer(claimer)
	t.Cleanup(func() { _ = m.Close() })

	cfg := DatabaseConfig{Name: "early", Backend: BackendMemory, Raft: validRaftConfig(0)}
	_, err = m.Create(cfg)
	var inactive *feature.NotActiveError
	if !errors.As(err, &inactive) || inactive.Feature != featuregate.ClusterShardIDs {
		t.Fatalf("raft create before activation = %v, want *feature.NotActiveError", err)
	}
	if inactive.Error() == "" {
		t.Fatal("empty error text")
	}
	if _, err := m.Get("early"); err == nil {
		t.Fatal("a refused raft create left a catalog entry")
	}
	if _, err := os.Stat(m.dbDir("", "early")); !os.IsNotExist(err) {
		t.Fatalf("a refused raft create left its directory: %v", err)
	}

	// A database with no Raft configuration claims nothing and is not held up.
	if _, err := m.Create(DatabaseConfig{Name: "plain", Backend: BackendMemory}); err != nil {
		t.Fatalf("non-raft create before activation: %v", err)
	}

	active.Store(true)
	db, err := m.Create(cfg)
	if err != nil {
		t.Fatalf("raft create after activation: %v", err)
	}
	if info := db.Info(); info.ShardID != shard.UserMin {
		t.Fatalf("shard ID after activation = %d, want %d", info.ShardID, shard.UserMin)
	}
}
