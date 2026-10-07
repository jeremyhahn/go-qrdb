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

package quic

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newShardIDQUICServer serves the database API over a real DatabaseService
// whose Manager claims shard IDs, as every production composition does.
func newShardIDQUICServer(t *testing.T) *Server {
	t.Helper()
	claimer, err := shard.NewClaimer(kvstore.NewMemoryKVStore())
	if err != nil {
		t.Fatalf("shard.NewClaimer: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := database.NewManager(logger, t.TempDir())
	mgr.SetShardIDClaimer(claimer)
	t.Cleanup(func() { _ = mgr.Close() })
	return NewServer(nil, nil, "localhost:0", WithDatabaseService(service.NewDatabaseService(mgr)))
}

// QUIC's create and Raft-update requests have no shard ID field, like every
// other transport's: the database gets the ID the cluster issues, never a
// caller-supplied one.
func TestHandleDatabase_RequestsCarryNoShardID(t *testing.T) {
	srv := newShardIDQUICServer(t)
	ctx := context.Background()

	create, _ := json.Marshal(map[string]any{
		"name": "picked", "backend": "memory",
		"raft": map[string]any{"clusterMode": "local", "replicaId": 1, "localNodeCount": 1},
	})
	resp, err := srv.handleDatabaseCreate(ctx, create)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var info transport.DatabaseInfo
	if err := json.Unmarshal(resp, &info); err != nil {
		t.Fatal(err)
	}
	if info.ShardID != shard.UserMin {
		t.Fatalf("created shard ID = %d, want the cluster-issued %d", info.ShardID, shard.UserMin)
	}

	update, _ := json.Marshal(map[string]any{
		"name": "picked",
		"raft": map[string]any{"clusterMode": "local", "replicaId": 1, "localNodeCount": 1},
	})
	resp, err = srv.handleDatabaseUpdateRaft(ctx, update)
	if err != nil {
		t.Fatalf("raft update: %v", err)
	}
	info = transport.DatabaseInfo{}
	if err := json.Unmarshal(resp, &info); err != nil {
		t.Fatal(err)
	}
	if info.ShardID != shard.UserMin {
		t.Fatalf("shard ID after the update = %d, want it unchanged at %d", info.ShardID, shard.UserMin)
	}
}

// TestHandleDatabase_CreateRefusesACallerSuppliedShardID pins OD-13's generic
// rule as it applies to a caller-chosen shard ID: a `shardId` key sent inside
// the raft block is not a declared field, and the strict decoder refuses the
// whole request rather than silently dropping just that key.
func TestHandleDatabase_CreateRefusesACallerSuppliedShardID(t *testing.T) {
	srv := newShardIDQUICServer(t)
	ctx := context.Background()

	create, _ := json.Marshal(map[string]any{
		"name": "rejected", "backend": "memory",
		"raft": map[string]any{"clusterMode": "local", "shardId": 4242, "replicaId": 1, "localNodeCount": 1},
	})
	if _, err := srv.handleDatabaseCreate(ctx, create); err == nil {
		t.Fatal("expected an error for the caller-supplied shardId, got nil")
	}
}
