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

package main

import (
	"context"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
)

// leaderHandoffTimeout bounds how long handOffLeadership waits for another
// replica to take the shard over. A var so tests can shorten it.
var leaderHandoffTimeout = 5 * time.Second

// leaderHandoffPoll paces handOffLeadership's checks for the new leader.
const leaderHandoffPoll = 50 * time.Millisecond

// leaderHandoffHost is the slice of the Raft host handOffLeadership drives.
type leaderHandoffHost interface {
	leaderIDSource
	ShardMembership(shardID uint64) (*quicraft.Membership, error)
	ShardInfo(shardID uint64) (*quicraft.ShardInfo, error)
	RequestLeaderTransfer(ctx context.Context, shardID, targetReplicaID uint64) error
}

var _ leaderHandoffHost = (*quicraft.Host)(nil)

// handOffLeadership moves leadership of shardID away from replicaID before
// the caller closes the host, and returns once another replica leads it. A
// leader that closes without handing off leaves its followers forwarding
// reads to a replica that no longer answers until one of them times out the
// election: every system-shard read on every peer stalls meanwhile -- the
// TLS handshake's trust resolution and a request's revocation check among
// them, so the peers stop serving authenticated requests.
//
// A no-op when replicaID does not lead shardID or no other voter exists. The
// wait is bounded by leaderHandoffTimeout and by ctx; the caller closes the
// host either way, so a handoff that does not complete costs only the
// election it was meant to spare.
func handOffLeadership(ctx context.Context, host leaderHandoffHost, shardID, replicaID uint64) error {
	if leaderID, ok, err := host.LeaderID(shardID); err != nil || !ok || leaderID != replicaID {
		return nil
	}
	m, err := host.ShardMembership(shardID)
	if err != nil {
		return err
	}
	var progress []quicraft.ReplicaProgress
	if info, ierr := host.ShardInfo(shardID); ierr == nil && info != nil {
		progress = info.Replicas
	}
	target := handoffTarget(m.Nodes, progress, replicaID)
	if target == 0 {
		return nil
	}
	if err := host.RequestLeaderTransfer(ctx, shardID, target); err != nil {
		return err
	}
	timeout := time.NewTimer(leaderHandoffTimeout)
	defer timeout.Stop()
	poll := time.NewTicker(leaderHandoffPoll)
	defer poll.Stop()
	for {
		if leaderID, ok, err := host.LeaderID(shardID); err == nil && ok && leaderID != replicaID {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			return &LeaderHandoffTimeoutError{ShardID: shardID, Timeout: leaderHandoffTimeout}
		case <-poll.C:
		}
	}
}

// handoffTarget picks the voter handOffLeadership transfers to. A transfer
// completes only once the target has the leader's log, so it prefers the
// voter this leader last heard from (Active) that has replicated the most
// (Match): a peer still restarting -- one that just ran its own migration --
// would let the transfer time out. When the leader reports progress and no
// other voter is active, nobody can take over -- the peers are down for their
// own migration -- and it returns zero rather than wait out a transfer that
// cannot complete. Without progress, which a leader publishes only from its
// next tick on, it falls back to the lowest replica ID other than self. Ties
// go to the lower ID, so every attempt names the same voter. Zero when self
// is the only voter.
func handoffTarget(voters map[uint64]string, progress []quicraft.ReplicaProgress, self uint64) uint64 {
	var target, match uint64
	for _, p := range progress {
		if _, voter := voters[p.ReplicaID]; !voter || p.ReplicaID == self || p.IsWitness || !p.Active {
			continue
		}
		if target == 0 || p.Match > match || (p.Match == match && p.ReplicaID < target) {
			target, match = p.ReplicaID, p.Match
		}
	}
	if target != 0 || reportsVoterProgress(voters, progress, self) {
		return target
	}
	for id := range voters {
		if id != self && (target == 0 || id < target) {
			target = id
		}
	}
	return target
}

// reportsVoterProgress reports whether progress covers any voter other than
// self -- whether the leader has published what handoffTarget judges by.
func reportsVoterProgress(voters map[uint64]string, progress []quicraft.ReplicaProgress, self uint64) bool {
	for _, p := range progress {
		if _, voter := voters[p.ReplicaID]; voter && p.ReplicaID != self {
			return true
		}
	}
	return false
}
