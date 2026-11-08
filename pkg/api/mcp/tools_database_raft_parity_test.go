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
	"reflect"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire/dbwiretest"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// fullDiscoveryArgs is dbwiretest.FullRaftConfig's discovery blocks as MCP arguments; the
// names are shared by the create and update tools.
func fullDiscoveryArgs() map[string]any {
	return map[string]any{
		"discovery_method":            "static",
		"static_peers":                []any{"db4.example.com:63001"},
		"static_shared_secret":        dbwiretest.FullStaticSecret,
		"multicast_shared_secret":     dbwiretest.FullMulticastSecret,
		"multicast_group":             "239.255.0.9:63100",
		"multicast_interface":         "eth1",
		"multicast_min_peers":         4,
		"multicast_announce_interval": "3s",
		"multicast_discover_timeout":  "45s",
		"dns_service":                 "qrdb-raft",
		"dns_proto":                   "tcp",
		"dns_domain":                  "cluster.example.com",
		"dns_lookup_timeout":          "7s",
		"token_endpoint":              "https://discovery.example.com/join",
		"token_value":                 dbwiretest.FullDiscoveryToken,
		"token_cluster_size":          5,
		"token_timeout":               "90s",
	}
}

// Every field REST, gRPC and QUIC can set in a Raft update, MCP can set too:
// the top-level peers and local node count, and every discovery block in full
// (announce interval, discover timeout, lookup timeout, token timeout).
func TestDatabaseUpdateRaftTool_EveryFieldReachesTheService(t *testing.T) {
	dbwiretest.AssertEveryFieldSet(t, dbwiretest.FullRaftConfig(), "ShardID")

	svc := newRealDatabaseService(t)
	dbwiretest.Seed(t, svc)
	args := fullDiscoveryArgs()
	args["name"] = dbwiretest.Name
	args["cluster_mode"] = "distributed"
	args["replica_id"] = 2
	args["raft_address"] = "db2.example.com:63001"
	args["peers"] = []any{"db2.example.com:63001", "db3.example.com:63001"}
	args["local_node_count"] = 5

	r, err := handleDatabaseUpdateRaft(svc)(context.Background(), buildCallToolRequest(t, "qrdb_database_update_raft", args))
	if err != nil {
		t.Fatal(err)
	}
	if r.IsError {
		t.Fatalf("update refused: %s", resultText(t, r))
	}
	stored, err := svc.Get(dbwiretest.Name)
	if err != nil {
		t.Fatal(err)
	}
	dbwiretest.AssertRaftConfig(t, "MCP", stored.Raft)
}

// The create tool carries the same fields, under its raft_ prefix for the
// top-level ones.
func TestDatabaseCreateTool_EveryRaftFieldReachesTheService(t *testing.T) {
	// A create with a Raft configuration is issued a shard ID, so the Manager
	// claims them, as every production composition's does.
	claimer, err := shard.NewClaimer(kvstore.NewMemoryKVStore())
	if err != nil {
		t.Fatal(err)
	}
	mgr := databasetest.NewManager(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})), t.TempDir())
	mgr.SetShardIDClaimer(claimer)
	t.Cleanup(func() { _ = mgr.Close() })
	svc := service.NewDatabaseService(mgr)

	args := fullDiscoveryArgs()
	args["name"] = "parity"
	args["backend"] = "memory"
	args["raft_cluster_mode"] = "distributed"
	args["raft_replica_id"] = 2
	args["raft_address"] = "db2.example.com:63001"
	args["raft_peers"] = []any{"db2.example.com:63001", "db3.example.com:63001"}
	args["raft_local_node_count"] = 5

	r, err := handleDatabaseCreate(svc)(context.Background(), buildCallToolRequest(t, "qrdb_database_create", args))
	if err != nil {
		t.Fatal(err)
	}
	if r.IsError {
		t.Fatalf("create refused: %s", resultText(t, r))
	}
	stored, err := svc.Get("parity")
	if err != nil {
		t.Fatal(err)
	}
	dbwiretest.AssertRaftConfig(t, "MCP create", stored.Raft)
}

// A read-modify-write through MCP keeps every block it sends back. The update
// names static discovery but carries the multicast, DNS and token blocks'
// non-secret fields, as a client that read the database back would; each
// block is kept, and each secret with it.
func TestDatabaseUpdateRaftTool_ReadModifyWriteKeepsEveryBlock(t *testing.T) {
	svc := newRealDatabaseService(t)
	dbwiretest.Seed(t, svc)
	seed := dbwiretest.RaftConfig()

	r, err := handleDatabaseUpdateRaft(svc)(context.Background(), buildCallToolRequest(t, "qrdb_database_update_raft", map[string]any{
		"name": dbwiretest.Name, "cluster_mode": "distributed", "replica_id": 1,
		"raft_address":        seed.RaftAddress,
		"peers":               []any{seed.Peers[0]},
		"discovery_method":    "static",
		"static_peers":        []any{seed.Static.Peers[0]},
		"multicast_group":     seed.Multicast.MulticastGroup,
		"multicast_min_peers": seed.Multicast.MinPeers,
		"dns_service":         seed.DNS.Service,
		"dns_proto":           seed.DNS.Proto,
		"dns_domain":          seed.DNS.Domain,
		"token_endpoint":      seed.Token.Endpoint,
		"token_cluster_size":  seed.Token.ClusterSize,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if r.IsError {
		t.Fatalf("read-modify-write refused: %s", resultText(t, r))
	}
	stored, err := svc.Get(dbwiretest.Name)
	if err != nil {
		t.Fatal(err)
	}
	got := *stored.Raft
	got.ShardID = 0
	if !reflect.DeepEqual(&got, seed) {
		t.Fatalf("stored Raft config after a read-modify-write =\n%+v\nwant it unchanged\n%+v", &got, seed)
	}
}

// A block the update neither names nor carries an argument for is left out,
// and so removed with its secret -- as on every other transport, where leaving
// a block out of the request removes it.
func TestDatabaseUpdateRaftTool_ABlockLeftOutIsRemoved(t *testing.T) {
	svc := newRealDatabaseService(t)
	dbwiretest.Seed(t, svc)

	r, err := handleDatabaseUpdateRaft(svc)(context.Background(), buildCallToolRequest(t, "qrdb_database_update_raft", map[string]any{
		"name": dbwiretest.Name, "cluster_mode": "distributed", "replica_id": 1,
		"peers":            []any{"db.example.com:63001"},
		"discovery_method": "static",
		"static_peers":     []any{"db.example.com:63001"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if r.IsError {
		t.Fatalf("update refused: %s", resultText(t, r))
	}
	stored, err := svc.Get(dbwiretest.Name)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Raft.Static == nil || stored.Raft.Static.SharedSecret != dbwiretest.StaticSecret {
		t.Fatalf("static block = %+v, want it kept with its secret", stored.Raft.Static)
	}
	if stored.Raft.Multicast != nil || stored.Raft.DNS != nil || stored.Raft.Token != nil {
		t.Fatalf("blocks left out of the update were kept: multicast %+v, dns %+v, token %+v",
			stored.Raft.Multicast, stored.Raft.DNS, stored.Raft.Token)
	}
}

// Both Raft tools declare every argument their builder reads, so a client
// discovering the tools can find each field.
func TestDatabaseRaftTools_DeclareEveryRaftArgument(t *testing.T) {
	for _, tc := range []struct {
		tool  func() mcp.Tool
		names raftArgs
	}{
		{toolDatabaseCreate, createRaftArgs},
		{toolDatabaseUpdateRaft, updateRaftArgs},
	} {
		tool := tc.tool()
		want := []string{"discovery_method", tc.names.clusterMode, tc.names.replicaID,
			tc.names.address, tc.names.peers, tc.names.localNodeCount}
		for _, block := range discoveryBlocks {
			want = append(want, block.args...)
		}
		for _, name := range want {
			if _, ok := tool.InputSchema.Properties[name]; !ok {
				t.Errorf("%s does not declare %q", tool.Name, name)
			}
		}
	}
}

// A block can be sent with nothing but an empty argument: that is how a
// client keeps a block whose only stored field is its secret.
func TestDatabaseUpdateRaftTool_AnEmptyArgumentSendsTheBlock(t *testing.T) {
	svc := newRealDatabaseService(t)
	dbwiretest.Seed(t, svc)
	update := handleDatabaseUpdateRaft(svc)
	base := func(extra map[string]any) map[string]any {
		args := map[string]any{
			"name": dbwiretest.Name, "cluster_mode": "distributed", "replica_id": 1,
			"discovery_method": "static", "static_peers": []any{"db.example.com:63001"},
		}
		for k, v := range extra {
			args[k] = v
		}
		return args
	}
	// A multicast block holding only its secret.
	r, err := update(context.Background(), buildCallToolRequest(t, "qrdb_database_update_raft",
		base(map[string]any{"multicast_shared_secret": "only-the-secret"})))
	if err != nil || r.IsError {
		t.Fatalf("setting a secret-only multicast block: %v %v", err, r)
	}
	// Sent back with one empty argument and no secret.
	r, err = update(context.Background(), buildCallToolRequest(t, "qrdb_database_update_raft",
		base(map[string]any{"multicast_interface": ""})))
	if err != nil {
		t.Fatal(err)
	}
	if r.IsError {
		t.Fatalf("update refused: %s", resultText(t, r))
	}
	stored, err := svc.Get(dbwiretest.Name)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Raft.Multicast == nil || stored.Raft.Multicast.SharedSecret != "only-the-secret" {
		t.Fatalf("multicast block = %+v, want it kept with its secret", stored.Raft.Multicast)
	}
}

// peers and raft_peers must be arrays of strings, as static_peers must.
func TestDatabaseRaftTools_PeersMustBeStrings(t *testing.T) {
	svc := newRealDatabaseService(t)
	dbwiretest.Seed(t, svc)
	r, err := handleDatabaseUpdateRaft(svc)(context.Background(), buildCallToolRequest(t, "qrdb_database_update_raft", map[string]any{
		"name": dbwiretest.Name, "cluster_mode": "distributed", "peers": "db.example.com:63001",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsError {
		t.Fatalf("update with a string for peers accepted: %s", resultText(t, r))
	}
	r, err = handleDatabaseCreate(svc)(context.Background(), buildCallToolRequest(t, "qrdb_database_create", map[string]any{
		"name": "badpeers", "backend": "memory",
		"raft_cluster_mode": "distributed", "raft_peers": []any{1},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsError {
		t.Fatalf("create with a number in raft_peers accepted: %s", resultText(t, r))
	}
}
