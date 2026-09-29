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

package raftharness

import (
	"context"
	"maps"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// TestNewCluster_CommitsEveryHostIdentity pins what callers rely on: every
// host is a system-shard voter at its address, with the identity of its own
// certificate committed, and the membership is readable once NewCluster and
// WaitReadable return.
func TestNewCluster_CommitsEveryHostIdentity(t *testing.T) {
	c := NewCluster(t, 2)
	if len(c.Hosts) != 2 || len(c.Addrs) != 2 || len(c.Identities) != 2 || c.CreateFn() == nil {
		t.Fatalf("cluster = %d hosts, %d addrs, %d identities", len(c.Hosts), len(c.Addrs), len(c.Identities))
	}
	c.WaitReadable(t, shard.ShardKV, c.Hosts...)

	ctx, cancel := context.WithTimeout(context.Background(), Bound)
	defer cancel()
	m, err := c.Hosts[0].SyncGetShardMembership(ctx, shard.ShardKV)
	if err != nil {
		t.Fatalf("SyncGetShardMembership: %v", err)
	}
	if !maps.Equal(m.Identities, c.Identities) {
		t.Fatalf("committed identities %v, want %v", m.Identities, c.Identities)
	}
	for i, host := range c.Hosts {
		id := uint64(i + 1)
		if m.Nodes[id] != c.Addrs[i] || host.NodeIdentity() != c.Identities[id] {
			t.Fatalf("host %d: address %q identity %q, membership says %q %q",
				i, c.Addrs[i], host.NodeIdentity(), m.Nodes[id], c.Identities[id])
		}
	}
}
