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

package server

import (
	"testing"

	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

func TestLeaderEvents_DropsEventsUntilBound(t *testing.T) {
	events := NewLeaderEvents()
	listener := events.Listener()
	listener.OnLeaderReady(quicraftconfig.LeaderInfo{ShardID: 7, ReplicaID: 1, LeaderID: 1})

	var got []quicraftconfig.LeaderInfo
	events.Bind(func(info quicraftconfig.LeaderInfo) { got = append(got, info) })
	if len(got) != 0 {
		t.Fatalf("binding replayed %d events, want 0", len(got))
	}
	want := quicraftconfig.LeaderInfo{ShardID: 9, ReplicaID: 2, Term: 3, LeaderID: 2}
	listener.OnLeaderReady(want)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("relayed %v, want [%v]", got, want)
	}
}

func TestLeaderEvents_BindReplacesTheEarlierBinding(t *testing.T) {
	events := NewLeaderEvents()
	var first, second int
	events.Bind(func(quicraftconfig.LeaderInfo) { first++ })
	events.Bind(func(quicraftconfig.LeaderInfo) { second++ })
	events.Listener().OnLeaderReady(quicraftconfig.LeaderInfo{ShardID: 1, ReplicaID: 1, LeaderID: 1})
	if first != 0 || second != 1 {
		t.Fatalf("first binding ran %d times, second %d; want 0 and 1", first, second)
	}
}

// homeLeaderFactory returns a factory whose shard tables hold acme's home
// group (shard 11, open), acme's "orders" database (shard 12) and globex's
// home group (shard 13, still starting), and the tenants its home-leader
// handler was called with.
func homeLeaderFactory() (*RaftBackendFactory, *[]string) {
	f := &RaftBackendFactory{
		shards: map[raftShardKey]*raftShard{
			{tenantID: "acme", database: database.TenantHomeName}: {tenantID: "acme", database: database.TenantHomeName, shardID: 11},
			{tenantID: "acme", database: "orders"}:                {tenantID: "acme", database: "orders", shardID: 12},
		},
		starting: map[raftShardKey]*startingShard{
			{tenantID: "globex", database: database.TenantHomeName}: {shardID: 13, ready: make(chan struct{})},
		},
	}
	var called []string
	f.OnHomeLeaderReady(func(tenantID string) { called = append(called, tenantID) })
	return f, &called
}

func TestRaftBackendFactory_LeaderReady_HomeGroupLeaderRunsTheHandler(t *testing.T) {
	f, called := homeLeaderFactory()
	f.LeaderReady(quicraftconfig.LeaderInfo{ShardID: 11, ReplicaID: 2, Term: 4, LeaderID: 2})
	f.LeaderReady(quicraftconfig.LeaderInfo{ShardID: 13, ReplicaID: 1, Term: 1, LeaderID: 1})
	if got := *called; len(got) != 2 || got[0] != "acme" || got[1] != "globex" {
		t.Fatalf("handler called with %v, want [acme globex]", got)
	}
}

func TestRaftBackendFactory_LeaderReady_IgnoresOtherEvents(t *testing.T) {
	f, called := homeLeaderFactory()
	for name, info := range map[string]quicraftconfig.LeaderInfo{
		"another replica leads":  {ShardID: 11, ReplicaID: 2, Term: 4, LeaderID: 3},
		"no leader":              {ShardID: 11, ReplicaID: 2, Term: 4},
		"a data database":        {ShardID: 12, ReplicaID: 1, Term: 1, LeaderID: 1},
		"a shard it never built": {ShardID: 99, ReplicaID: 1, Term: 1, LeaderID: 1},
	} {
		f.LeaderReady(info)
		if len(*called) != 0 {
			t.Fatalf("%s: handler called with %v, want no call", name, *called)
		}
	}
}

func TestRaftBackendFactory_LeaderReady_NoHandlerIsANoOp(t *testing.T) {
	f := &RaftBackendFactory{shards: map[raftShardKey]*raftShard{
		{tenantID: "acme", database: database.TenantHomeName}: {shardID: 11},
	}}
	f.LeaderReady(quicraftconfig.LeaderInfo{ShardID: 11, ReplicaID: 1, LeaderID: 1})
	if _, ok := f.homeTenantOf(11); !ok {
		t.Fatal("homeTenantOf(11) did not find acme's home group")
	}
}
