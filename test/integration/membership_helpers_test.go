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

//go:build integration

package integration_test

import (
	"testing"
)

// nextReplicaID reads the shard ID and the highest replica ID from the
// leader's /peers -- which, unlike /status, lists every member of the shard
// -- and returns the shard with the next unused replica ID.
func nextReplicaID(t *testing.T, ca *testCA, leaderPort int) (shardID, next uint64) {
	t.Helper()
	peers := httpPeers(t, ca, leaderPort)
	shards, ok := peers["shards"].([]interface{})
	if !ok || len(shards) == 0 {
		t.Fatalf("leader /peers missing shards: %v", peers)
	}
	var maxReplicaID uint64
	for _, raw := range shards {
		sp, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		sid, _ := sp["shard_id"].(float64)
		shardID = uint64(sid)
		peerList, ok := sp["peers"].([]interface{})
		if !ok {
			continue
		}
		for _, pRaw := range peerList {
			p, ok := pRaw.(map[string]interface{})
			if !ok {
				continue
			}
			rid, _ := p["replica_id"].(float64)
			if rid := uint64(rid); rid > maxReplicaID {
				maxReplicaID = rid
			}
		}
	}
	if maxReplicaID == 0 {
		t.Fatalf("could not determine an existing replica_id from leader /peers: %v", peers)
	}
	return shardID, maxReplicaID + 1
}
