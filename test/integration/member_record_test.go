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
	"time"
)

// TestCluster_StaticClusterPublishesMemberRecords: every raft-mode node
// publishes its lease-backed member record -- its Raft address and the API
// endpoints it serves -- without --dynamic-membership. Replica forwarding
// resolves its targets from these records, so a default static cluster must
// carry one per node. No node runs the dynamic-membership manager.
//
// "member record published" is logged by qrdbd only after the node
// advertiser's synchronous registration succeeded. The generic "registered
// cluster member" line proves nothing here: the feature gate publishes its
// own advertisement through the same cluster.Advertiser and logs it too.
func TestCluster_StaticClusterPublishesMemberRecords(t *testing.T) {
	_, _, _, logs := startThreeNodeClusterLogged(t, "pebble")

	deadline := time.Now().Add(30 * time.Second)
	for i, l := range logs {
		for !l.Contains("member record published") {
			if l.Contains("failed to register self") {
				t.Fatalf("node %d failed to publish its member record", i+1)
			}
			if time.Now().After(deadline) {
				t.Fatalf("node %d never published its member record without --dynamic-membership", i+1)
			}
			time.Sleep(200 * time.Millisecond)
		}
		if l.Contains("dynamic membership manager started") {
			t.Fatalf("node %d started the dynamic-membership manager it was not asked for", i+1)
		}
	}
}
