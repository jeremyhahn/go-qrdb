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
	"errors"
	"strings"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/clustermig"
)

// progressHost is a replicationHost answering from fixed state, as the Raft
// host publishes it: info for ShardInfo, committed for DiagRaftState.
type progressHost struct {
	info      *quicraft.ShardInfo
	infoErr   error
	committed uint64
	stateErr  error
}

var _ replicationHost = (*progressHost)(nil)

func (h *progressHost) ShardInfo(uint64) (*quicraft.ShardInfo, error) {
	if h.infoErr != nil {
		return nil, h.infoErr
	}
	return h.info, nil
}

func (h *progressHost) DiagRaftState(uint64) (quicraft.RaftStateDiag, error) {
	return quicraft.RaftStateDiag{
		Term:      3,
		Committed: h.committed,
		Processed: h.committed,
		LastIndex: h.committed,
		State:     "leader",
	}, h.stateErr
}

// leaderOf3 is shard 9 led by replica 1 at a:1, with b:2 caught up to 50 and
// c:3 behind at 20.
func leaderOf3(committed uint64) *progressHost {
	return &progressHost{
		committed: committed,
		info: &quicraft.ShardInfo{
			ShardID: 9, ReplicaID: 1, LeaderID: 1, IsLeader: true,
			Nodes: map[uint64]string{1: "a:1", 2: "b:2", 3: "c:3"},
			Replicas: []quicraft.ReplicaProgress{
				{ReplicaID: 2, Role: quicraft.ReplicaRoleVoter, Match: 50},
				{ReplicaID: 3, Role: quicraft.ReplicaRoleVoter, Match: 20},
			},
		},
	}
}

func TestHostReplicationProgress_CommitIndex(t *testing.T) {
	p := newHostReplicationProgress(leaderOf3(50))
	idx, ok, err := p.CommitIndex(9)
	if err != nil || !ok || idx != 50 {
		t.Fatalf("CommitIndex = %d, %v, %v; want 50, true, nil", idx, ok, err)
	}

	// The host publishes Raft state only once asked: the first read is zero.
	idx, ok, err = newHostReplicationProgress(leaderOf3(0)).CommitIndex(9)
	if err != nil || ok || idx != 0 {
		t.Fatalf("CommitIndex(unpublished) = %d, %v, %v; want 0, false, nil", idx, ok, err)
	}
}

func TestHostReplicationProgress_Leads(t *testing.T) {
	if !newHostReplicationProgress(leaderOf3(50)).Leads(9) {
		t.Fatal("Leads(leader) = false, want true")
	}
	follower := leaderOf3(50)
	follower.info.IsLeader, follower.info.LeaderID = false, 2
	if newHostReplicationProgress(follower).Leads(9) {
		t.Fatal("Leads(follower) = true, want false")
	}
	// A shard the host does not hold, or a host that cannot report on it,
	// is not led here: the caller fails closed.
	if newHostReplicationProgress(&progressHost{infoErr: errors.New("shard not found")}).Leads(9) {
		t.Fatal("Leads(unknown shard) = true, want false")
	}
}

func TestHostReplicationProgress_MatchIndex(t *testing.T) {
	p := newHostReplicationProgress(leaderOf3(50))
	for _, tc := range []struct {
		node  string
		match uint64
	}{
		{"b:2", 50},
		{"c:3", 20},
		{"a:1", 50}, // the leader holds everything it committed
	} {
		m, ok, err := p.MatchIndex(9, tc.node)
		if err != nil || !ok || m != tc.match {
			t.Fatalf("MatchIndex(%s) = %d, %v, %v; want %d, true, nil", tc.node, m, ok, err, tc.match)
		}
	}
}

func TestHostReplicationProgress_UnpublishedReplicaIsNotKnown(t *testing.T) {
	h := leaderOf3(50)
	h.info.Replicas = nil
	m, ok, err := newHostReplicationProgress(h).MatchIndex(9, "b:2")
	if err != nil || ok || m != 0 {
		t.Fatalf("MatchIndex(unpublished) = %d, %v, %v; want 0, false, nil", m, ok, err)
	}
}

func TestHostReplicationProgress_NotAMember(t *testing.T) {
	_, _, err := newHostReplicationProgress(leaderOf3(50)).MatchIndex(9, "d:4")
	var e *clustermig.NotShardMemberError
	if !errors.As(err, &e) || e.ShardID != 9 || e.NodeID != "d:4" {
		t.Fatalf("MatchIndex(non-member) error = %v, want NotShardMemberError{9, d:4}", err)
	}
}

func TestHostReplicationProgress_FollowerCannotAnswer(t *testing.T) {
	h := leaderOf3(50)
	h.info.IsLeader, h.info.LeaderID, h.info.Replicas = false, 2, nil
	p := newHostReplicationProgress(h)
	var e *clustermig.NotShardLeaderError
	if _, _, err := p.CommitIndex(9); !errors.As(err, &e) || e.ShardID != 9 {
		t.Fatalf("CommitIndex(follower) error = %v, want NotShardLeaderError{9}", err)
	}
	if _, _, err := p.MatchIndex(9, "b:2"); !errors.As(err, &e) {
		t.Fatalf("MatchIndex(follower) error = %v, want NotShardLeaderError", err)
	}
}

func TestHostReplicationProgress_HostErrorsAreTyped(t *testing.T) {
	boom := errors.New("boom")
	for _, tc := range []struct {
		name string
		host *progressHost
		op   string
		call func(*hostReplicationProgress) error
	}{
		{"shard info", &progressHost{infoErr: boom}, "shard-info",
			func(p *hostReplicationProgress) error { _, _, err := p.CommitIndex(9); return err }},
		{"raft state", func() *progressHost { h := leaderOf3(50); h.stateErr = boom; return h }(), "raft-state",
			func(p *hostReplicationProgress) error { _, _, err := p.CommitIndex(9); return err }},
		{"leader's own match", func() *progressHost { h := leaderOf3(50); h.stateErr = boom; return h }(), "raft-state",
			func(p *hostReplicationProgress) error { _, _, err := p.MatchIndex(9, "a:1"); return err }},
		{"match shard info", &progressHost{infoErr: boom}, "shard-info",
			func(p *hostReplicationProgress) error { _, _, err := p.MatchIndex(9, "b:2"); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call(newHostReplicationProgress(tc.host))
			var e *ReplicationProgressError
			if !errors.As(err, &e) || e.Op != tc.op || e.ShardID != 9 || !errors.Is(err, boom) {
				t.Fatalf("error = %v, want ReplicationProgressError{%s, 9} wrapping boom", err, tc.op)
			}
			if !strings.Contains(err.Error(), tc.op) || !strings.Contains(err.Error(), "shard 9") {
				t.Fatalf("Error() = %q, want the op and shard", err.Error())
			}
		})
	}
}

func TestNewReplicationProgress_NilHost(t *testing.T) {
	if p := NewReplicationProgress(nil); p != nil {
		t.Fatalf("NewReplicationProgress(nil) = %v, want nil", p)
	}
	if p := NewReplicationProgress(&quicraft.Host{}); p == nil {
		t.Fatal("NewReplicationProgress(host) = nil")
	}
}
