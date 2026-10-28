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
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// leaderSourceFunc adapts a function to LeaderSource.
type leaderSourceFunc func(shardID uint64) (uint64, bool, error)

var _ LeaderSource = leaderSourceFunc(nil)

func (f leaderSourceFunc) LeaderID(shardID uint64) (uint64, bool, error) { return f(shardID) }

// TestAwaitShardLeader_LeaderPastThirtyPollsIsFound is the defect the wait
// replaced: a leader elected after the thirtieth poll. The old wait counted
// polls and gave up on the thirtieth however much of its budget remained.
func TestAwaitShardLeader_LeaderPastThirtyPollsIsFound(t *testing.T) {
	var polls atomic.Int64
	src := leaderSourceFunc(func(uint64) (uint64, bool, error) {
		if polls.Add(1) < 40 {
			return 0, false, nil
		}
		return 1, true, nil
	})

	if err := AwaitShardLeader(context.Background(), src, time.Minute, 1); err != nil {
		t.Fatalf("AwaitShardLeader: %v", err)
	}
	if got := polls.Load(); got != 40 {
		t.Fatalf("polls = %d, want 40", got)
	}
}

// TestAwaitShardLeader_ReturnsAtOnceWhenALeaderIsKnown: a shard that already
// has a leader costs one poll and no round of the wait.
func TestAwaitShardLeader_ReturnsAtOnceWhenALeaderIsKnown(t *testing.T) {
	var polls atomic.Int64
	src := leaderSourceFunc(func(uint64) (uint64, bool, error) {
		polls.Add(1)
		return 2, true, nil
	})

	if err := AwaitShardLeader(context.Background(), src, time.Minute, 1); err != nil {
		t.Fatalf("AwaitShardLeader: %v", err)
	}
	if got := polls.Load(); got != 1 {
		t.Fatalf("polls = %d, want 1", got)
	}
}

// TestAwaitShardLeader_CancelEndsTheUnboundedWait cancels mid-wait under a
// round of an hour: the wait must end on the cancel, not on the round.
func TestAwaitShardLeader_CancelEndsTheUnboundedWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var polls atomic.Int64
	src := leaderSourceFunc(func(uint64) (uint64, bool, error) {
		if polls.Add(1) == 3 {
			cancel()
		}
		return 0, false, nil
	})

	start := time.Now()
	err := AwaitShardLeader(ctx, src, time.Hour, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("AwaitShardLeader = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed >= 10*time.Second {
		t.Fatalf("the wait outlived its context: %v", elapsed)
	}
	if got := polls.Load(); got != 3 {
		t.Fatalf("polls = %d, want 3: the wait polled again after its context ended", got)
	}
}

// TestAwaitShardLeader_ExhaustedRoundsDoNotEndTheWait: the leader becomes
// known only after several rounds have run out -- a quorum unsealed long after
// this node. Each exhausted round is logged and the wait goes on, so the
// leader is still found.
func TestAwaitShardLeader_ExhaustedRoundsDoNotEndTheWait(t *testing.T) {
	const round = 50 * time.Millisecond
	const leaderAfter = 4 * round
	start := time.Now()
	src := leaderSourceFunc(func(uint64) (uint64, bool, error) {
		if time.Since(start) < leaderAfter {
			return 0, false, nil
		}
		return 1, true, nil
	})

	if err := AwaitShardLeader(context.Background(), src, round, 7); err != nil {
		t.Fatalf("AwaitShardLeader: %v", err)
	}
	if elapsed := time.Since(start); elapsed < leaderAfter {
		t.Fatalf("returned after %v, before the leader was known at %v", elapsed, leaderAfter)
	}
}

// TestAwaitShardLeader_DeadlineIsNotARound: a ctx deadline ends the wait with
// the context's error, never with the round's ShardLeaderWaitError.
func TestAwaitShardLeader_DeadlineIsNotARound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	src := leaderSourceFunc(func(uint64) (uint64, bool, error) { return 0, false, nil })

	err := AwaitShardLeader(ctx, src, 50*time.Millisecond, 3)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AwaitShardLeader = %v, want context.DeadlineExceeded", err)
	}
	var we *ShardLeaderWaitError
	if errors.As(err, &we) {
		t.Fatalf("an exhausted round must not end the wait: %v", err)
	}
}
