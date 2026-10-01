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

package lifecycle

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// DatabaseServices projects every engine of the graph, named and on the given
// shard.
func TestNode_DatabaseServices(t *testing.T) {
	n := &Node{}
	got := n.DatabaseServices(SystemDatabase, 7)
	if got.Name != SystemDatabase || got.ShardID != 7 {
		t.Fatalf("DatabaseServices = %+v, want name %q on shard 7", got, SystemDatabase)
	}
}

// Each engine is the graph's own servicer, not a copy or a wrapper.
func TestNode_DatabaseServices_SharesTheGraph(t *testing.T) {
	kv := service.NewKVService(nil, 1)
	n := &Node{KV: kv}
	got := n.DatabaseServices("ledger", 0)
	if got.KV != kv || got.Name != "ledger" || got.ShardID != 0 {
		t.Fatalf("DatabaseServices = %+v, want the graph's KV under ledger", got)
	}
	if got.MVCC != nil || got.Vector != nil {
		t.Fatalf("an engine the graph does not have was projected: %+v", got)
	}
}
