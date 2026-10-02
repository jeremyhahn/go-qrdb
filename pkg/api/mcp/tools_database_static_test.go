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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire/dbwiretest"
)

// MCP builds the static discovery block whether or not the request carries a
// secret, as it does the multicast one, and carries its peers: an update that
// leaves the secret out keeps the stored one, as on every other transport,
// instead of dropping the block and the secret with it.
func TestDatabaseUpdateRaftTool_StaticBlockWithoutTheSecret(t *testing.T) {
	svc := newRealDatabaseService(t)
	dbwiretest.Seed(t, svc)

	r, err := handleDatabaseUpdateRaft(svc)(context.Background(), buildCallToolRequest(t, "qrdb_database_update_raft", map[string]any{
		"name": dbwiretest.Name, "cluster_mode": "distributed", "replica_id": 1,
		"discovery_method": "static", "static_peers": []any{"db.example.com:63001"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if r.IsError {
		t.Fatalf("update without the static secret refused: %s", resultText(t, r))
	}
	stored, err := svc.Get(dbwiretest.Name)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Raft.Static == nil || stored.Raft.Static.SharedSecret != dbwiretest.StaticSecret {
		t.Fatalf("static block after an update without its secret = %+v, want the stored secret kept", stored.Raft.Static)
	}
	if len(stored.Raft.Static.Peers) != 1 || stored.Raft.Static.Peers[0] != "db.example.com:63001" {
		t.Fatalf("static peers = %v, want the ones sent", stored.Raft.Static.Peers)
	}
}

// static_peers must be an array of strings.
func TestDatabaseUpdateRaftTool_StaticPeersMustBeStrings(t *testing.T) {
	svc := newRealDatabaseService(t)
	dbwiretest.Seed(t, svc)
	r, err := handleDatabaseUpdateRaft(svc)(context.Background(), buildCallToolRequest(t, "qrdb_database_update_raft", map[string]any{
		"name": dbwiretest.Name, "cluster_mode": "distributed",
		"discovery_method": "static", "static_peers": []any{"db.example.com:63001", 7},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsError {
		t.Fatalf("static_peers with a non-string element accepted: %s", resultText(t, r))
	}
}
