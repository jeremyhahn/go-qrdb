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

package server

import (
	"strconv"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/clustermig"
)

// replicationHost is the slice of the Raft host hostReplicationProgress
// reads: the leader's view of each replica's match index, and its commit
// index.
type replicationHost interface {
	ShardInfo(shardID uint64) (*quicraft.ShardInfo, error)
	DiagRaftState(shardID uint64) (quicraft.RaftStateDiag, error)
}

// ReplicationProgressError is a Raft host query that failed while answering a
// clustermig.ReplicationProgress question: the shard is not on this host, or
// the host is closing.
type ReplicationProgressError struct {
	Op      string
	ShardID uint64
	Err     error
}

func (e *ReplicationProgressError) Error() string {
	return "server: replication progress: " + e.Op + " shard " + strconv.FormatUint(e.ShardID, 10) + ": " + e.Err.Error()
}

func (e *ReplicationProgressError) Unwrap() error { return e.Err }

// hostReplicationProgress is clustermig.ReplicationProgress over the Raft
// host. Both answers are the leader's: a follower tracks no other replica's
// progress, so on a node that does not lead the shard every query is
// *clustermig.NotShardLeaderError. Node IDs are the members' Raft addresses,
// as the owner resolver reports them.
type hostReplicationProgress struct {
	host replicationHost
}

var _ clustermig.ReplicationProgress = (*hostReplicationProgress)(nil)

// NewReplicationProgress returns the Raft host's clustermig.ReplicationProgress,
// the seam clustermig.WithCatchUp gates a rolling migration's hand-on on. A nil
// host (a standalone node, which has no replicas to catch up) returns nil.
func NewReplicationProgress(host *quicraft.Host) clustermig.ReplicationProgress {
	if host == nil {
		return nil
	}
	return newHostReplicationProgress(host)
}

func newHostReplicationProgress(host replicationHost) *hostReplicationProgress {
	return &hostReplicationProgress{host: host}
}

// CommitIndex returns the shard's commit index. ok is false while the host has
// not published it yet: the host publishes Raft state only once asked, so the
// first query reads zero and a later tick fills it in.
// Leads reports whether this node leads the shard. A shard the host does not
// hold, or cannot report on, is not led here.
func (p *hostReplicationProgress) Leads(shardID uint64) bool {
	_, err := p.leaderInfo(shardID)
	return err == nil
}

func (p *hostReplicationProgress) CommitIndex(shardID uint64) (uint64, bool, error) {
	if _, err := p.leaderInfo(shardID); err != nil {
		return 0, false, err
	}
	return p.committed(shardID)
}

// MatchIndex returns the highest index of the shard's log the leader knows
// nodeID holds. The leader itself holds everything it committed. ok is false
// while the leader has not published the replica's progress yet.
func (p *hostReplicationProgress) MatchIndex(shardID uint64, nodeID string) (uint64, bool, error) {
	info, err := p.leaderInfo(shardID)
	if err != nil {
		return 0, false, err
	}
	replicaID, member := replicaIDOf(info.Nodes, nodeID)
	if !member {
		return 0, false, &clustermig.NotShardMemberError{ShardID: shardID, NodeID: nodeID}
	}
	if replicaID == info.ReplicaID {
		return p.committed(shardID)
	}
	for _, r := range info.Replicas {
		if r.ReplicaID == replicaID {
			return r.Match, true, nil
		}
	}
	return 0, false, nil
}

// leaderInfo returns the shard's info if this host leads it.
func (p *hostReplicationProgress) leaderInfo(shardID uint64) (*quicraft.ShardInfo, error) {
	info, err := p.host.ShardInfo(shardID)
	if err != nil {
		return nil, &ReplicationProgressError{Op: "shard-info", ShardID: shardID, Err: err}
	}
	if !info.IsLeader {
		return nil, &clustermig.NotShardLeaderError{ShardID: shardID}
	}
	return info, nil
}

func (p *hostReplicationProgress) committed(shardID uint64) (uint64, bool, error) {
	diag, err := p.host.DiagRaftState(shardID)
	if err != nil {
		return 0, false, &ReplicationProgressError{Op: "raft-state", ShardID: shardID, Err: err}
	}
	return diag.Committed, diag.Committed > 0, nil
}

// replicaIDOf returns the replica ID whose address is nodeID.
func replicaIDOf(nodes map[uint64]string, nodeID string) (uint64, bool) {
	for id, addr := range nodes {
		if addr == nodeID {
			return id, true
		}
	}
	return 0, false
}
