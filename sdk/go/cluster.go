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

package sdk

import (
	"context"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// DefaultMembershipPollInterval is the polling cadence WatchMembership uses
// to detect shard membership and leadership changes. Polling keeps the
// facade transport-agnostic: it works over every transport, including the
// unary-only REST transport, without a dedicated streaming RPC.
const DefaultMembershipPollInterval = time.Second

// ClusterClient is a thin facade over the control-plane AdminClient for
// cluster membership and leadership operations. It adds no new wire RPCs:
// AddReplica/RemoveReplica delegate to the existing AddMember/RemoveMember,
// Members/Leader read from Peers()/GetShardState(), and WatchMembership
// polls those same calls and emits deltas.
type ClusterClient struct {
	admin        transport.AdminClient
	pollInterval time.Duration
}

// Cluster returns a ClusterClient for shard membership and leadership
// operations. It requires a transport that implements AdminClient.
func (c *Client) Cluster() *ClusterClient {
	return &ClusterClient{
		admin:        c.transport.(transport.AdminClient),
		pollInterval: DefaultMembershipPollInterval,
	}
}

// WithPollInterval overrides the WatchMembership polling cadence. A larger
// interval reduces control-plane load and allocation churn at the cost of
// slower change detection. Non-positive values are ignored. Returns the
// receiver for chaining.
func (cc *ClusterClient) WithPollInterval(d time.Duration) *ClusterClient {
	if d > 0 {
		cc.pollInterval = d
	}
	return cc
}

// Member describes a single Raft replica within a shard.
type Member struct {
	// ReplicaID is the replica's identifier within the shard.
	ReplicaID uint64

	// Address is the replica's Raft network address.
	Address string

	// IsLeader is true when this replica is the shard's current leader.
	IsLeader bool
}

// MembershipEvent is emitted by WatchMembership when a shard's membership or
// leadership changes. It carries the full current membership plus the delta
// since the previous event.
type MembershipEvent struct {
	// ShardID is the shard the event pertains to.
	ShardID uint64

	// Members is the full current membership snapshot.
	Members []Member

	// LeaderID is the current leader's replica ID. 0 if unknown.
	LeaderID uint64

	// Added lists replicas present now but not in the prior snapshot.
	Added []Member

	// Removed lists replicas present in the prior snapshot but not now.
	Removed []Member

	// LeaderChanged is true when the leader differs from the prior snapshot.
	LeaderChanged bool
}

// Members returns the current membership of the given shard. It filters the
// node's Peers() view to the requested shard.
func (cc *ClusterClient) Members(ctx context.Context, shardID uint64) ([]Member, error) {
	resp, err := cc.admin.Peers(ctx)
	if err != nil {
		return nil, err
	}
	for _, sp := range resp.Shards {
		if sp.ShardID != shardID {
			continue
		}
		members := make([]Member, len(sp.Peers))
		for i, p := range sp.Peers {
			members[i] = Member{
				ReplicaID: p.ReplicaID,
				Address:   p.Address,
				IsLeader:  p.IsLeader,
			}
		}
		return members, nil
	}
	return nil, &ShardNotFoundError{ShardID: shardID}
}

// Leader returns the current leader replica ID for the given shard and
// whether a leader is currently known.
func (cc *ClusterClient) Leader(ctx context.Context, shardID uint64) (replicaID uint64, hasLeader bool, err error) {
	st, err := cc.admin.GetShardState(ctx, shardID)
	if err != nil {
		return 0, false, err
	}
	return st.LeaderID, st.HasLeader, nil
}

// AddReplica adds a replica to the shard. It delegates to the already-wired
// AddMember control-plane operation.
func (cc *ClusterClient) AddReplica(ctx context.Context, shardID, replicaID uint64, raftAddr string) error {
	return cc.admin.AddMember(ctx, shardID, replicaID, raftAddr)
}

// RemoveReplica removes a replica from the shard. It delegates to the
// already-wired RemoveMember control-plane operation.
func (cc *ClusterClient) RemoveReplica(ctx context.Context, shardID, replicaID uint64) error {
	return cc.admin.RemoveMember(ctx, shardID, replicaID)
}

// WatchMembership returns a channel that emits a MembershipEvent whenever the
// shard's membership or leadership changes. The first event delivers the
// initial snapshot. The watcher polls at DefaultMembershipPollInterval and
// stops when ctx is cancelled, at which point the channel is closed.
//
// Polling (rather than a streaming RPC) keeps this method usable over every
// transport, including the unary-only REST transport.
func (cc *ClusterClient) WatchMembership(ctx context.Context, shardID uint64) (<-chan MembershipEvent, error) {
	// Validate the shard exists before starting the watcher so callers get
	// an immediate error for an unknown shard rather than a silent channel.
	initial, err := cc.Members(ctx, shardID)
	if err != nil {
		return nil, err
	}

	out := make(chan MembershipEvent, 1)
	prev := membershipSet(initial)
	prevLeader := leaderOf(initial)

	out <- MembershipEvent{
		ShardID:  shardID,
		Members:  initial,
		LeaderID: prevLeader,
		Added:    initial,
	}

	go func() {
		defer close(out)
		ticker := time.NewTicker(cc.pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}

			current, err := cc.Members(ctx, shardID)
			if err != nil {
				// A transient read error (or a shard that briefly drops out)
				// is skipped; the next tick retries. Cancellation exits above.
				continue
			}

			curSet := membershipSet(current)
			curLeader := leaderOf(current)
			added := diffMembers(current, prev)
			removed := diffMembers(prevSlice(prev), curSet)
			leaderChanged := curLeader != prevLeader

			if len(added) == 0 && len(removed) == 0 && !leaderChanged {
				continue
			}

			select {
			case <-ctx.Done():
				return
			case out <- MembershipEvent{
				ShardID:       shardID,
				Members:       current,
				LeaderID:      curLeader,
				Added:         added,
				Removed:       removed,
				LeaderChanged: leaderChanged,
			}:
			}

			prev = curSet
			prevLeader = curLeader
		}
	}()

	return out, nil
}

// membershipSet indexes members by replica ID.
func membershipSet(members []Member) map[uint64]Member {
	set := make(map[uint64]Member, len(members))
	for _, m := range members {
		set[m.ReplicaID] = m
	}
	return set
}

// prevSlice materializes a member set back into a slice for diffing.
func prevSlice(set map[uint64]Member) []Member {
	members := make([]Member, 0, len(set))
	for _, m := range set {
		members = append(members, m)
	}
	return members
}

// diffMembers returns members in the slice that are absent from the set.
func diffMembers(members []Member, set map[uint64]Member) []Member {
	var diff []Member
	for _, m := range members {
		if _, ok := set[m.ReplicaID]; !ok {
			diff = append(diff, m)
		}
	}
	return diff
}

// leaderOf returns the replica ID of the leader in members, or 0 if none.
func leaderOf(members []Member) uint64 {
	for _, m := range members {
		if m.IsLeader {
			return m.ReplicaID
		}
	}
	return 0
}
