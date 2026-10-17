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

package store

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/importstage"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// TestIsNodeLocalKey_ReplicatedTablesAreNeverNodeLocal is the property a
// snapshot/restore round trip for the lease table, the compaction table and
// the replicated audit chain depends on: a record under any of their
// reserved prefixes must travel in snapshots and be covered by the replica
// hash, exactly like any other applied record, rather than being treated as
// local bookkeeping the recovering replica is expected to regenerate on its
// own (which none of the three can -- a lease binding, a compaction policy
// and a chain head are all durable state with no local source of truth to
// regenerate from).
func TestIsNodeLocalKey_ReplicatedTablesAreNeverNodeLocal(t *testing.T) {
	tests := []struct {
		name string
		key  []byte
		want bool
	}{
		{"the last-applied index is node-local", []byte(metaKeyLastApplied), true},
		{"a lease-table record is replicated, not node-local", []byte(lease.TablePrefix + "l/x"), false},
		{"a compaction-table record is replicated, not node-local", []byte(compaction.TablePrefix + "p"), false},
		{"an audit-table head record is replicated, not node-local", []byte(vlog.TablePrefix + "h/acme"), false},
		{"an audit-table event record is replicated, not node-local", []byte(vlog.TablePrefix + "e/acme/1"), false},
		{"a named-log head record is replicated, not node-local", []byte(vlog.LogTablePrefix + "acme/app/ledger/h"), false},
		{"a named-log tree node is replicated, not node-local", []byte(vlog.LogTablePrefix + "acme/app/ledger/n/\x00/\x00\x00\x00\x00\x00\x00\x00\x01"), false},
		{"an import-staging chunk is replicated, not node-local", importstage.ChunkKey("sess", 3), false},
		{"an import-staging session record is replicated, not node-local", importstage.MetaKey("sess"), false},
		{"an import-staging tenant-index entry is replicated, not node-local", importstage.TenantIndexKey("acme", "sess"), false},
		{"an ordinary user key is not node-local at all", []byte("acme/widget"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNodeLocalKey(tt.key); got != tt.want {
				t.Fatalf("isNodeLocalKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}
