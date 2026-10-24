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
	"context"
	"testing"
)

// raftKeepAliveRenewals is how many renewals the keep-alive tests issue. Any
// one of them going through the log moves the applied index.
const raftKeepAliveRenewals = 32

// startLeaseShard starts a database's own Raft group through the factory and
// returns its services with a granted lease.
func startLeaseShard(t *testing.T) (*RaftBackendFactory, *DatabaseServices, int64) {
	t.Helper()
	f, reg, src := newRaftFactory(t)
	root, _ := registerVolumeTenant(t, reg, "tl")
	src.add("tl", "leases", 1000, root)
	svcs, err := f.Services(context.Background(), "tl", "leases")
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	l, err := svcs.Lease.Grant(context.Background(), 60)
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	return f, svcs, l.ID
}

// appliedIndex returns the shard's applied index once every entry committed
// before the call is applied: the linearizable read returns only after the
// applied index reaches its read index.
func appliedIndex(t *testing.T, f *RaftBackendFactory, svcs *DatabaseServices, leaseID int64) uint64 {
	t.Helper()
	if _, err := svcs.Lease.TimeToLive(context.Background(), leaseID); err != nil {
		t.Fatalf("TimeToLive: %v", err)
	}
	info, err := f.host.GetShardInfo(svcs.ShardID)
	if err != nil {
		t.Fatalf("GetShardInfo(%d): %v", svcs.ShardID, err)
	}
	return info.LastApplied
}

// TestRaftBackendFactory_LeaseKeepAliveAppendsNoLogEntry pins the property
// the keep-alive latency rests on, on a real Raft group: the leader answers a
// renewal from memory behind a quorum read, so a renewal waits on no log
// append, fsync or apply. Were renewals proposed, each would add an entry and
// the applied index would move.
func TestRaftBackendFactory_LeaseKeepAliveAppendsNoLogEntry(t *testing.T) {
	f, svcs, leaseID := startLeaseShard(t)
	ctx := context.Background()

	before := appliedIndex(t, f, svcs, leaseID)
	for i := range raftKeepAliveRenewals {
		l, err := svcs.Lease.KeepAlive(ctx, leaseID)
		if err != nil {
			t.Fatalf("KeepAlive %d: %v", i, err)
		}
		if l.ID != leaseID {
			t.Fatalf("KeepAlive %d renewed lease %d, want %d", i, l.ID, leaseID)
		}
	}
	if after := appliedIndex(t, f, svcs, leaseID); after != before {
		t.Fatalf("applied index moved %d -> %d across %d renewals: a renewal went through the log",
			before, after, raftKeepAliveRenewals)
	}
}

// TestRaftBackendFactory_AppliedIndexCountsProposals is the control for the
// test above: the same instrument sees a proposal, so an unmoved applied
// index there means no entry, not a blind reading.
func TestRaftBackendFactory_AppliedIndexCountsProposals(t *testing.T) {
	f, svcs, leaseID := startLeaseShard(t)
	ctx := context.Background()

	before := appliedIndex(t, f, svcs, leaseID)
	for i := range raftKeepAliveRenewals {
		if err := svcs.KV.Put(ctx, "k", []byte{byte(i)}); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}
	if after := appliedIndex(t, f, svcs, leaseID); after < before+raftKeepAliveRenewals {
		t.Fatalf("applied index moved %d -> %d across %d proposals, want at least %d entries",
			before, after, raftKeepAliveRenewals, raftKeepAliveRenewals)
	}
}
