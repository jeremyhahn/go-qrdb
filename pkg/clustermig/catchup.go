// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package clustermig

// ReplicationProgress reports how far a shard's Raft log has replicated, as
// the shard's leader knows it. Only the leader tracks its peers' progress, so
// a node that does not lead the shard returns *NotShardLeaderError. The
// composer implements it over the Raft host; this package does not depend on
// the consensus library.
type ReplicationProgress interface {
	// Leads reports whether this node leads the shard now. A node that is
	// not a replica of the shard, or cannot tell, does not.
	Leads(shardID uint64) bool

	// CommitIndex returns the shard's commit index. ok is false while the
	// leader cannot report it yet.
	CommitIndex(shardID uint64) (index uint64, ok bool, err error)

	// MatchIndex returns the highest index of the shard's log the leader
	// knows nodeID holds. ok is false while the leader cannot report it yet.
	// A nodeID that is not a replica of the shard is *NotShardMemberError.
	MatchIndex(shardID uint64, nodeID string) (match uint64, ok bool, err error)
}

// WithCatchUp gates every rolling step's hand-on on its owner's Raft log: the
// next owner's turn starts only once the owner that just recycled holds the
// shard's log up to the commit index the leader saw when the owner acked. A
// node that recycled is back in the cluster when its log has caught up, not
// when its barrier is resealed, so without this two owners' logs could be
// behind at once. Advance checks once per call and never waits; the
// reconciler's tick is the poll.
//
// With p, a migration whose scope has its own Raft shard is advanced by the
// node leading that shard, the one node that knows how far its log has
// replicated, which need not lead the coordination store. While the shard
// has no leader, no node advances it.
func WithCatchUp(p ReplicationProgress) CoordinatorOption {
	return func(c *Coordinator) { c.progress = p }
}

// coordinates reports whether this node advances a migration of the scope on
// shardID: with WithCatchUp, the node leading that shard; without it, and for
// a scope with no single Raft shard (ShardID 0), the coordination store's
// leader.
func (c *Coordinator) coordinates(shardID uint64) bool {
	if c.progress != nil && shardID != 0 {
		return c.progress.Leads(shardID)
	}
	return c.isLeader()
}

// caughtUp reports whether step's owner holds the shard's log up to the
// step's CatchUpIndex, first recording the leader's commit index as that
// target when the step has none; changed reports a newly recorded target,
// which the caller writes even when the owner has not caught up yet.
// Without WithCatchUp, and for a scope with no single Raft shard (ShardID
// 0), the ack alone hands on.
func (c *Coordinator) caughtUp(shardID uint64, step *NodeStep) (done, changed bool, err error) {
	if c.progress == nil || shardID == 0 {
		return true, false, nil
	}
	if step.CatchUpIndex == 0 {
		idx, ok, err := c.progress.CommitIndex(shardID)
		if err != nil || !ok {
			return false, false, err
		}
		step.CatchUpIndex = idx
		changed = true
	}
	match, ok, err := c.progress.MatchIndex(shardID, step.NodeID)
	if err != nil {
		return false, false, err
	}
	return ok && match >= step.CatchUpIndex, changed, nil
}
