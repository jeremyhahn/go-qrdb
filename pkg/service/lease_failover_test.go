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

package service

// A lease keep-alive is renewed in the leader's memory unless the leader has
// checkpointed the lease, when it is a replicated command (pkg/lease:
// Lessor.RenewLocal, Lessor.ApplyCheckpoints); a new leader floors every
// deadline at now + effective TTL + failover grace, where the effective TTL is
// the lease's checkpoint if it has one (Lessor.Promote). Proving the
// cross-replica half needs a REAL multi-node Raft group with one Lessor and one
// expiry sweeper per replica, which is what sdk/go/testutil/raftharness builds.

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"

	raftbackend "github.com/jeremyhahn/go-qrdb/pkg/backend/raft"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/raftharness"
)

// failoverCheckpointInterval keeps the checkpoint, and so every replication
// wait below, short: every lease is checkpointed within it.
const failoverCheckpointInterval = 100 * time.Millisecond

// noPeriodicCheckpoint is a checkpoint interval no test outlives, so the only
// checkpoint is the one a test asks for.
const noPeriodicCheckpoint = time.Hour

// leaseReplica is one member of a lease failover cluster: its Lessor, the
// service over its host, and the sweeper that serves it.
type leaseReplica struct {
	lessor *lease.Lessor
	svc    LeaseServicer
}

// startLeaseFailoverCluster starts a 3-host group on shardID in which every
// replica keeps its leases in a Lessor of its own and runs the production
// sweeper wiring (RunExpiry gated on this replica leading, checkpointing
// through the service every interval, with grace as the failover grace).
func startLeaseFailoverCluster(t *testing.T, shardID uint64, interval, grace time.Duration) (*raftharness.Cluster, []*leaseReplica) {
	t.Helper()
	const hostCount = 3
	c := raftharness.NewCluster(t, hostCount)
	members := make(map[uint64]string, hostCount)
	for i := range c.Hosts {
		members[uint64(i+1)] = c.Addrs[i]
	}

	ctx, cancel := context.WithCancel(context.Background())
	var sweepers sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		sweepers.Wait()
	})

	replicas := make([]*leaseReplica, hostCount)
	for i, host := range c.Hosts {
		lr := lease.NewLessor()
		createFn, err := statemachine.ResolveTypedWithStorage("memory", "", statemachine.StorageConfig{}, statemachine.WithKVLessor(lr))
		if err != nil {
			t.Fatalf("ResolveTypedWithStorage: %v", err)
		}
		if err := host.StartShard(members, false, createFn, quicraftconfig.Config{
			ShardID: shardID, ReplicaID: uint64(i + 1), MemberIdentities: c.Identities,
		}); err != nil {
			t.Fatalf("StartShard(host %d): %v", i, err)
		}
		// Every replica runs this binary, so the lease-checkpoint gate is
		// open (see alwaysEnabledHost).
		adapter := alwaysEnabledHost{raftbackend.New(host)}
		replicas[i] = &leaseReplica{lessor: lr, svc: NewLeaseService(adapter, shardID)}

		replicaID := uint64(i + 1)
		sweepers.Add(1)
		go func() {
			defer sweepers.Done()
			lr.RunExpiry(ctx, 50*time.Millisecond,
				func() bool {
					id, known, err := host.GetLeaderID(shardID)
					return err == nil && known && id == replicaID
				},
				replicas[replicaID-1].svc.Revoke,
				slog.Default(),
				lease.WithCheckpoint(interval, NewLeaseCheckpointer(adapter, shardID)),
				lease.WithFailoverGrace(grace),
			)
		}()
	}
	c.WaitReadable(t, shardID, c.Hosts...)
	return c, replicas
}

// waitForLease polls until cond holds for lease id on every replica.
func waitForLease(t *testing.T, replicas []*leaseReplica, id int64, what string, cond func(*lease.Lease) bool) {
	t.Helper()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	expired := time.After(raftharness.Bound)
	for i, r := range replicas {
		for !cond(r.lessor.Lookup(id)) {
			select {
			case <-poll.C:
			case <-expired:
				t.Fatalf("replica %d: %s did not hold within %s", i+1, what, raftharness.Bound)
			}
		}
	}
}

// waitForDeadlineFrom polls until every replica holds lease id with a deadline
// at or after bound. Lookup truncates the remaining TTL to whole seconds, so a
// read taken at t that reports n > 0 seconds proves a deadline no earlier than
// t + n. That lower bound stays at or after bound for nearly the whole of a
// deadline that is, where a threshold on RemainingTTL alone one second below
// the TTL holds for only the second after the replica applied the renewal: the
// replicas are read in turn, so a slow first replica could spend the next
// one's second before it was ever read.
func waitForDeadlineFrom(t *testing.T, replicas []*leaseReplica, id int64, bound time.Time) {
	t.Helper()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	expired := time.After(raftharness.Bound)
	for i, r := range replicas {
		for {
			at := time.Now()
			if l := r.lessor.Lookup(id); l != nil && l.RemainingTTL > 0 &&
				!at.Add(time.Duration(l.RemainingTTL)*time.Second).Before(bound) {
				break
			}
			select {
			case <-poll.C:
			case <-expired:
				t.Fatalf("replica %d: no deadline at or after %s within %s", i+1, bound.Format(time.RFC3339Nano), raftharness.Bound)
			}
		}
	}
}

// TestLeaseKeepAlive_CheckpointedLeaseRenewsOnEveryReplica proves a keep-alive
// of a lease the leader has checkpointed is not kept in the leader's memory:
// it goes through the log, so it extends the lease on the followers too.
// Without that a follower's deadline would stay at the one the grant set, and
// the checkpoint would claim less time than the lease has.
func TestLeaseKeepAlive_CheckpointedLeaseRenewsOnEveryReplica(t *testing.T) {
	const shardID = uint64(9101)
	const ttl = int64(6)

	// No failover grace: the first election's promotion may land just after
	// the grant, and a grace would then lift the deadline this test measures.
	c, replicas := startLeaseFailoverCluster(t, shardID, failoverCheckpointInterval, 0)
	leaderIdx, _ := waitForLeaderExcept(t, c.Hosts, shardID, nil)
	ctx := context.Background()

	granted, err := replicas[leaderIdx].svc.Grant(ctx, ttl)
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	waitForLease(t, replicas, granted.ID, "the grant applied", func(l *lease.Lease) bool { return l != nil })

	// Let the grant's deadline run down well below the TTL. RemainingTTL
	// truncates, so a replica that reads ttl-2 or less holds a deadline before
	// that read plus ttl-1 seconds: once every replica has, none holds one at
	// or after grantBound, and a deadline there can only be the renewal's.
	waitForLease(t, replicas, granted.ID, "the grant's deadline running down", func(l *lease.Lease) bool {
		return l != nil && l.RemainingTTL <= ttl-2
	})
	grantBound := time.Now().Add(time.Duration(ttl-1) * time.Second)

	if _, err := replicas[leaderIdx].svc.KeepAlive(ctx, granted.ID); err != nil {
		t.Fatalf("KeepAlive: %v", err)
	}
	waitForDeadlineFrom(t, replicas, granted.ID, grantBound)
}

// TestLeaseFailover_NewLeaderFloorsTheDeadlineAtTTLPlusGrace proves what a
// leader change does to a lease: the new leader never expires it early -- it
// outlives its own TTL -- and revokes it, on every replica, only once the
// failover grace has run out.
func TestLeaseFailover_NewLeaderFloorsTheDeadlineAtTTLPlusGrace(t *testing.T) {
	const shardID = uint64(9102)
	const ttl = int64(1)
	const grace = 3 * time.Second

	c, replicas := startLeaseFailoverCluster(t, shardID, failoverCheckpointInterval, grace)
	leaderIdx, leaderReplicaID := waitForLeaderExcept(t, c.Hosts, shardID, nil)
	ctx := context.Background()

	granted, err := replicas[leaderIdx].svc.Grant(ctx, ttl)
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	waitForLease(t, replicas, granted.ID, "the grant applied", func(l *lease.Lease) bool { return l != nil })

	target := leaderReplicaID%uint64(len(c.Hosts)) + 1
	if err := c.Hosts[leaderIdx].RequestLeaderTransfer(ctx, shardID, target); err != nil {
		t.Fatalf("RequestLeaderTransfer: %v", err)
	}
	newLeaderIdx, newLeaderReplicaID := waitForLeaderExcept(t, c.Hosts, shardID, map[uint64]bool{leaderReplicaID: true})
	newLeader := replicas[newLeaderIdx].lessor

	// Promotion floors the deadline at now + effective TTL + grace, however
	// little of the original TTL was left when leadership moved. A live lease's
	// checkpoint is rounded up to a whole second, so with a one-second TTL the
	// effective TTL is the TTL either way.
	floor := ttl + int64(grace/time.Second) - 1
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	expired := time.After(raftharness.Bound)
	for l := newLeader.Lookup(granted.ID); l == nil || l.RemainingTTL < floor; l = newLeader.Lookup(granted.ID) {
		select {
		case <-poll.C:
		case <-expired:
			t.Fatalf("new leader (replica %d) never floored the deadline at TTL+grace", newLeaderReplicaID)
		}
	}
	promoted := time.Now()

	// The lease outlives its own TTL: nothing revokes it inside the grace.
	// Every replica is checked throughout the window, not only at its end.
	outlived := time.After(time.Duration(ttl)*time.Second + 500*time.Millisecond)
	for inWindow := true; ; {
		for i, r := range replicas {
			if !r.lessor.Has(granted.ID) {
				t.Fatalf("replica %d lost the lease %s after promotion, inside the grace", i+1, time.Since(promoted))
			}
		}
		if !inWindow {
			break
		}
		select {
		case <-poll.C:
		case <-outlived:
			inWindow = false
		}
	}

	// Once the grace is spent the new leader revokes it on every replica.
	waitForLease(t, replicas, granted.ID, "the revocation", func(l *lease.Lease) bool { return l == nil })
	if elapsed := time.Since(promoted); elapsed < grace-time.Second {
		t.Fatalf("lease revoked %s after promotion, want it held for about the %s grace", elapsed, grace)
	}
}

// TestLeaseFailover_NewLeaderCountsFromTheOnDemandCheckpoint proves the
// guarantee the checkpoints exist for: a leader that checkpoints before it
// hands leadership over (LeaseService.CheckpointAll) leaves the next leader
// counting each lease from what it had left, not from a full TTL. No periodic
// checkpoint runs, so the only one is the one asked for.
func TestLeaseFailover_NewLeaderCountsFromTheOnDemandCheckpoint(t *testing.T) {
	const shardID = uint64(9103)
	const ttl = int64(10)

	c, replicas := startLeaseFailoverCluster(t, shardID, noPeriodicCheckpoint, 0)
	leaderIdx, leaderReplicaID := waitForLeaderExcept(t, c.Hosts, shardID, nil)
	ctx := context.Background()

	granted, err := replicas[leaderIdx].svc.Grant(ctx, ttl)
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	waitForLease(t, replicas, granted.ID, "the grant's deadline running down", func(l *lease.Lease) bool {
		return l != nil && l.RemainingTTL <= ttl-3
	})

	if err := replicas[leaderIdx].svc.(*LeaseService).CheckpointAll(ctx); err != nil {
		t.Fatalf("CheckpointAll: %v", err)
	}
	target := leaderReplicaID%uint64(len(c.Hosts)) + 1
	if err := c.Hosts[leaderIdx].RequestLeaderTransfer(ctx, shardID, target); err != nil {
		t.Fatalf("RequestLeaderTransfer: %v", err)
	}
	newLeaderIdx, newLeaderReplicaID := waitForLeaderExcept(t, c.Hosts, shardID, map[uint64]bool{leaderReplicaID: true})

	// The new leader promotes on its next sweep tick. Promotion that counted
	// from the full TTL would put the lease back at ttl; counting from the
	// checkpoint keeps it at or below the ttl-2 the checkpoint rounded up to.
	newLeader := replicas[newLeaderIdx].lessor
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	settled := time.After(500 * time.Millisecond)
	for watching := true; watching; {
		l := newLeader.Lookup(granted.ID)
		if l == nil {
			t.Fatalf("new leader (replica %d) dropped the lease", newLeaderReplicaID)
		}
		if l.RemainingTTL > ttl-2 {
			t.Fatalf("new leader (replica %d) counts the lease from %ds, want at most the %ds checkpointed",
				newLeaderReplicaID, l.RemainingTTL, ttl-2)
		}
		select {
		case <-poll.C:
		case <-settled:
			watching = false
		}
	}
}
