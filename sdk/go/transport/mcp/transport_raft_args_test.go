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
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The MCP transport sends every field of a Raft request the other transports
// send -- the peers and the local node count included -- under the server's
// argument names: raft_-prefixed on create, bare on update.
func TestApplyRaftArgs_SendsPeersAndLocalNodeCount(t *testing.T) {
	cfg := &transport.DatabaseRaftConfig{
		ClusterMode:    "distributed",
		ReplicaID:      2,
		RaftAddress:    "db2.example.com:63001",
		Peers:          []string{"db2.example.com:63001", "db3.example.com:63001"},
		LocalNodeCount: 3,
	}
	for _, tc := range []struct {
		prefix string
		want   map[string]any
	}{
		{"raft_", map[string]any{
			"raft_cluster_mode":     "distributed",
			"raft_replica_id":       uint64(2),
			"raft_address":          "db2.example.com:63001",
			"raft_peers":            []string{"db2.example.com:63001", "db3.example.com:63001"},
			"raft_local_node_count": 3,
		}},
		{"", map[string]any{
			"cluster_mode":     "distributed",
			"replica_id":       uint64(2),
			"raft_address":     "db2.example.com:63001",
			"peers":            []string{"db2.example.com:63001", "db3.example.com:63001"},
			"local_node_count": 3,
		}},
	} {
		args := map[string]any{}
		applyRaftArgs(args, cfg, tc.prefix)
		if !reflect.DeepEqual(args, tc.want) {
			t.Errorf("prefix %q: args = %#v, want %#v", tc.prefix, args, tc.want)
		}
	}
}
