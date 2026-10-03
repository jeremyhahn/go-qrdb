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

package featuregate

import (
	"strings"
	"testing"
)

// The one refusal for a feature-gated operation names the feature, and the
// group when the feature is needed of one group's replicas.
func TestNotActiveError_Message(t *testing.T) {
	cluster := (&NotActiveError{Feature: ClusterShardIDs}).Error()
	if !strings.HasPrefix(cluster, "featuregate: ") || !strings.Contains(cluster, `"cluster-shard-ids"`) || strings.Contains(cluster, "shard 0") {
		t.Fatalf("cluster-wide message = %q", cluster)
	}
	group := (&NotActiveError{Feature: PrefixCompaction, ShardID: 7}).Error()
	if !strings.Contains(group, `"prefix-compaction"`) || !strings.Contains(group, "on shard 7") {
		t.Fatalf("per-group message = %q", group)
	}
}
