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
	"errors"
	"sync"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
)

// fakeHandoffHost is a shard whose leader changes only when a transfer is
// requested and elect is set, the way a Raft host takes a transfer over.
type fakeHandoffHost struct {
	mu          sync.Mutex
	leader      uint64
	voters      map[uint64]string
	elect       bool
	leaderErr   error
	memberErr   error
	transferErr error
	progress    []quicraft.ReplicaProgress
	transfers   []uint64
}

var _ leaderHandoffHost = (*fakeHandoffHost)(nil)

func (h *fakeHandoffHost) LeaderID(uint64) (uint64, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.leader, h.leader != 0, h.leaderErr
}

func (h *fakeHandoffHost) ShardMembership(uint64) (*quicraft.Membership, error) {
	if h.memberErr != nil {
		return nil, h.memberErr
	}
	return &quicraft.Membership{Nodes: h.voters}, nil
}

func (h *fakeHandoffHost) ShardInfo(shardID uint64) (*quicraft.ShardInfo, error) {
	return &quicraft.ShardInfo{ShardID: shardID, Replicas: h.progress}, nil
}

func (h *fakeHandoffHost) RequestLeaderTransfer(_ context.Context, _, target uint64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.transfers = append(h.transfers, target)
	if h.transferErr != nil {
		return h.transferErr
	}
	if h.elect {
		h.leader = target
	}
	return nil
}

func threeVoters() map[uint64]string {
	return map[uint64]string{1: "192.0.2.1:5000", 2: "192.0.2.2:5000", 3: "192.0.2.3:5000"}
}

// The leader hands the shard to another voter and returns once that voter
// leads it -- the leaderless window a closing leader otherwise leaves is the
// one that stalled every peer's system-shard reads.
func TestHandOffLeadership_TransfersAndWaitsForTheNewLeader(t *testing.T) {
	h := &fakeHandoffHost{leader: 1, voters: threeVoters(), elect: true}
	if err := handOffLeadership(context.Background(), h, 1, 1); err != nil {
		t.Fatalf("handOffLeadership: %v", err)
	}
	if len(h.transfers) != 1 || h.transfers[0] != 2 {
		t.Fatalf("transfers = %v, want one to replica 2", h.transfers)
	}
	if h.leader != 2 {
		t.Fatalf("leader = %d after handoff, want 2", h.leader)
	}
}

func TestHandOffLeadership_NoOpWhenNotTheLeader(t *testing.T) {
	for name, h := range map[string]*fakeHandoffHost{
		"another replica leads": {leader: 2, voters: threeVoters()},
		"no leader known":       {voters: threeVoters()},
		"leader unreadable":     {leader: 1, voters: threeVoters(), leaderErr: quicraft.ErrClosed},
		"sole voter":            {leader: 1, voters: map[uint64]string{1: "192.0.2.1:5000"}},
		"every peer down":       {leader: 1, voters: threeVoters(), progress: []quicraft.ReplicaProgress{{ReplicaID: 2}, {ReplicaID: 3}}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := handOffLeadership(context.Background(), h, 1, 1); err != nil {
				t.Fatalf("handOffLeadership: %v, want nil", err)
			}
			if len(h.transfers) != 0 {
				t.Fatalf("transfers = %v, want none", h.transfers)
			}
		})
	}
}

func TestHandOffLeadership_SurfacesHostErrors(t *testing.T) {
	boom := errors.New("boom")
	for name, h := range map[string]*fakeHandoffHost{
		"membership": {leader: 1, voters: threeVoters(), memberErr: boom},
		"transfer":   {leader: 1, voters: threeVoters(), transferErr: boom},
	} {
		t.Run(name, func(t *testing.T) {
			if err := handOffLeadership(context.Background(), h, 1, 1); !errors.Is(err, boom) {
				t.Fatalf("handOffLeadership: %v, want boom", err)
			}
		})
	}
}

func TestHandOffLeadership_BoundedWhenNoOneTakesOver(t *testing.T) {
	saved := leaderHandoffTimeout
	leaderHandoffTimeout = 100 * time.Millisecond
	t.Cleanup(func() { leaderHandoffTimeout = saved })

	h := &fakeHandoffHost{leader: 1, voters: threeVoters()}
	err := handOffLeadership(context.Background(), h, 7, 1)
	var timeout *LeaderHandoffTimeoutError
	if !errors.As(err, &timeout) || timeout.ShardID != 7 || timeout.Timeout != leaderHandoffTimeout {
		t.Fatalf("handOffLeadership: %v, want *LeaderHandoffTimeoutError for shard 7", err)
	}
	if timeout.Error() != "leadership of shard 7 not handed off within 100ms" {
		t.Fatalf("Error() = %q", timeout.Error())
	}
}

func TestHandOffLeadership_StopsWithTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := &fakeHandoffHost{leader: 1, voters: threeVoters()}
	if err := handOffLeadership(ctx, h, 1, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("handOffLeadership: %v, want context.Canceled", err)
	}
}

// The leader's own progress picks the target: a voter still restarting from
// its own migration is inactive, and the one that is caught up takes over.
func TestHandOffLeadership_TransfersToTheCaughtUpVoter(t *testing.T) {
	h := &fakeHandoffHost{leader: 1, voters: threeVoters(), elect: true, progress: []quicraft.ReplicaProgress{
		{ReplicaID: 2, Match: 40, Active: false},
		{ReplicaID: 3, Match: 90, Active: true},
	}}
	if err := handOffLeadership(context.Background(), h, 1, 1); err != nil {
		t.Fatalf("handOffLeadership: %v", err)
	}
	if len(h.transfers) != 1 || h.transfers[0] != 3 {
		t.Fatalf("transfers = %v, want one to replica 3", h.transfers)
	}
}

func TestHandoffTarget_FallsBackToTheLowestOtherVoter(t *testing.T) {
	for name, tc := range map[string]struct {
		voters   map[uint64]string
		progress []quicraft.ReplicaProgress
		self     uint64
		want     uint64
	}{
		"no progress, self lowest":  {voters: threeVoters(), self: 1, want: 2},
		"no progress, self middle":  {voters: threeVoters(), self: 2, want: 1},
		"sole voter":                {voters: map[uint64]string{3: ""}, self: 3, want: 0},
		"no voters":                 {self: 1, want: 0},
		"only a non-voter reported": {voters: threeVoters(), self: 1, want: 2, progress: []quicraft.ReplicaProgress{{ReplicaID: 4, Match: 9, Active: true}}},
		"only self reported":        {voters: threeVoters(), self: 1, want: 2, progress: []quicraft.ReplicaProgress{{ReplicaID: 1, Match: 9, Active: true}}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := handoffTarget(tc.voters, tc.progress, tc.self); got != tc.want {
				t.Fatalf("handoffTarget = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestHandoffTarget_PrefersTheActiveVoterWithTheMostLog(t *testing.T) {
	for name, tc := range map[string]struct {
		progress []quicraft.ReplicaProgress
		want     uint64
	}{
		"highest match wins": {want: 3, progress: []quicraft.ReplicaProgress{
			{ReplicaID: 2, Match: 10, Active: true}, {ReplicaID: 3, Match: 20, Active: true}}},
		"inactive skipped": {want: 2, progress: []quicraft.ReplicaProgress{
			{ReplicaID: 2, Match: 10, Active: true}, {ReplicaID: 3, Match: 20}}},
		"witness skipped": {want: 2, progress: []quicraft.ReplicaProgress{
			{ReplicaID: 2, Match: 10, Active: true}, {ReplicaID: 3, Match: 20, Active: true, IsWitness: true}}},
		"self skipped": {want: 3, progress: []quicraft.ReplicaProgress{
			{ReplicaID: 1, Match: 99, Active: true}, {ReplicaID: 3, Match: 20, Active: true}}},
		"tie goes to the lower id": {want: 2, progress: []quicraft.ReplicaProgress{
			{ReplicaID: 3, Match: 20, Active: true}, {ReplicaID: 2, Match: 20, Active: true}}},
		"no voter active": {want: 0, progress: []quicraft.ReplicaProgress{
			{ReplicaID: 2, Match: 10}, {ReplicaID: 3, Match: 20, Active: true, IsWitness: true}}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := handoffTarget(threeVoters(), tc.progress, 1); got != tc.want {
				t.Fatalf("handoffTarget = %d, want %d", got, tc.want)
			}
		})
	}
}
