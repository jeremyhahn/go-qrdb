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

package placement

import (
	"strings"
	"testing"
)

// TestRefusalMessagesNameWhatWasRefused: each refusal names the database, the
// node and what the caller needs to act on -- the replica set to retry
// against, or the path that was refused and the remedy.
func TestRefusalMessagesNameWhatWasRefused(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		contains []string
	}{
		{
			name: "not a replica lists every replica",
			err: &NotAReplicaError{TenantID: "acme", Database: "orders", ShardID: 42, NodeID: "n3",
				Replicas: []Replica{{NodeID: "n1"}, {NodeID: "n2"}}},
			contains: []string{"n3", "acme/orders", "shard 42", "n1", ", n2"},
		},
		{
			name:     "not a replica with no replicas",
			err:      &NotAReplicaError{TenantID: "acme", Database: "orders", ShardID: 7, NodeID: "n3"},
			contains: []string{"n3", "acme/orders", "shard 7"},
		},
		{
			name:     "replica naming a path names the path and the remedy",
			err:      &ReplicaDataRootError{TenantID: "acme", Database: "orders", NodeID: "n1", DataRoot: "/srv/n1/acme"},
			contains: []string{"n1", "acme/orders", "/srv/n1/acme", "re-create the database"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.err.Error()
			for _, want := range tc.contains {
				if !strings.Contains(got, want) {
					t.Errorf("message %q does not contain %q", got, want)
				}
			}
		})
	}
}
