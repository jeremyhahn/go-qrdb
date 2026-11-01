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

package mcp

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire/dbwiretest"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Every field of the SDK's Raft request -- discovery blocks and their secrets
// included -- reaches the database through the server's MCP tools.
func TestMCPDatabase_RaftRequestReachesTheService(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := databasetest.NewManager(logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	svc := service.NewDatabaseService(mgr)
	dbwiretest.Seed(t, svc)
	tr, cleanup := connectDatabaseInProcess(t, svc)
	defer cleanup()

	if _, err := tr.UpdateDatabaseRaft(context.Background(), transport.UpdateDatabaseRaftRequest{
		Name: dbwiretest.Name, Raft: dbwiretest.FullRaftRequest(),
	}); err != nil {
		t.Fatalf("UpdateDatabaseRaft: %v", err)
	}
	stored, err := svc.Get(dbwiretest.Name)
	if err != nil {
		t.Fatal(err)
	}
	dbwiretest.AssertRaftConfig(t, "MCP", stored.Raft)
}

// A discovery block sent without its secret, and with every other field
// empty, still reaches the server as a block -- so a client writing back a
// block whose only stored field is its secret keeps the secret instead of
// removing the block.
func TestMCPDatabase_AnEmptyBlockIsStillSent(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := databasetest.NewManager(logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	svc := service.NewDatabaseService(mgr)
	dbwiretest.Seed(t, svc)
	tr, cleanup := connectDatabaseInProcess(t, svc)
	defer cleanup()
	ctx := context.Background()

	raft := func(mc *transport.MulticastDiscoveryConfig) *transport.DatabaseRaftConfig {
		return &transport.DatabaseRaftConfig{
			ClusterMode: "distributed", ReplicaID: 1, Discovery: "static",
			Peers:     []string{"db.example.com:63001"},
			Static:    &transport.StaticDiscoveryConfig{Peers: []string{"db.example.com:63001"}},
			Multicast: mc,
		}
	}
	if _, err := tr.UpdateDatabaseRaft(ctx, transport.UpdateDatabaseRaftRequest{
		Name: dbwiretest.Name, Raft: raft(&transport.MulticastDiscoveryConfig{SharedSecret: "only-the-secret"}),
	}); err != nil {
		t.Fatalf("setting a secret-only multicast block: %v", err)
	}
	if _, err := tr.UpdateDatabaseRaft(ctx, transport.UpdateDatabaseRaftRequest{
		Name: dbwiretest.Name, Raft: raft(&transport.MulticastDiscoveryConfig{}),
	}); err != nil {
		t.Fatalf("writing it back without the secret: %v", err)
	}
	stored, err := svc.Get(dbwiretest.Name)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Raft.Multicast == nil || stored.Raft.Multicast.SharedSecret != "only-the-secret" {
		t.Fatalf("multicast block = %+v, want it kept with its secret", stored.Raft.Multicast)
	}
}
