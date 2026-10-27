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

package raft

import (
	"context"
	"errors"
	"strconv"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
)

const (
	// defaultLeaderWaitTimeout bounds how long a request holds through a
	// moment in which this node knows no leader for its shard: an election
	// in progress, or a follower that has not yet heard from a new leader.
	// It covers several elections on a loaded host, whose election timeout
	// can run to seconds, and the caller's context bounds it further.
	defaultLeaderWaitTimeout = 10 * time.Second

	// leaderPollBaseDelay is the first interval between checks for a known
	// leader, doubled on each check up to leaderPollMaxDelay.
	leaderPollBaseDelay = 10 * time.Millisecond

	// leaderPollMaxDelay caps the interval between checks for a known leader.
	leaderPollMaxDelay = 250 * time.Millisecond
)

// leaderReporter is the part of *quicraft.Host the leader wait reads: whether
// this node currently knows a leader for a shard.
type leaderReporter interface {
	LeaderID(shardID uint64) (uint64, bool, error)
}

// LeaderWaitError is returned when go-quicraft refused a request because this
// node knew no leader for ShardID, or, for a read, because the leader had not
// yet committed an entry in its term, and it was still refused after Waited.
// Err is the last refusal, a *quicraft.NotLeaderError or
// quicraft.ErrReadIndexNotReady, so a caller that classifies the condition
// with errors.As or errors.Is still finds it.
type LeaderWaitError struct {
	ShardID uint64
	Waited  time.Duration
	Err     error
}

// Error implements error.
func (e *LeaderWaitError) Error() string {
	var state string
	switch {
	case leaderUnknown(e.Err):
		state = ": no leader known after waiting "
	case firstCommitPending(e.Err):
		state = ": leader had not committed in its term after waiting "
	default:
		state = ": still refused as not leader after waiting "
	}
	return "raft: shard " + strconv.FormatUint(e.ShardID, 10) + state + e.Waited.String() + ": " + e.Err.Error()
}

// Unwrap returns the last refusal.
func (e *LeaderWaitError) Unwrap() error { return e.Err }

// leaderUnknown reports whether err is go-quicraft's refusal of a request on
// a node that knows no leader for the shard: the one not-leader refusal that
// names nowhere to forward the request to. A not-leader error under
// quicraft.MaybeAppliedError is no refusal: the proposal was already in the
// log when its leader stepped down and may still commit, so it is never held
// and proposed again.
func leaderUnknown(err error) bool {
	var nle *quicraft.NotLeaderError
	return !quicraft.MaybeApplied(err) && errors.As(err, &nle) && nle.LeaderID == 0 && nle.LeaderAddress == ""
}

// throughElection runs op and, while go-quicraft refuses it because this node
// knows no leader for shardID, waits for one and runs op again. A follower
// forwards proposals and ReadIndex requests to its leader inside go-quicraft,
// so a node that briefly knows none -- mid-election, or a follower a new
// leader has not reached yet -- would otherwise fail an ordinary request that
// a moment later it could have served; every node serves every endpoint, and
// a client must not have to retry through leadership changing hands.
//
// Re-running op is safe: go-quicraft refuses a request as not-leader before it
// is proposed, so the refused attempt left nothing in the log.
//
// The wait is bounded by timeout and by ctx. At the bound the last refusal is
// returned inside a *LeaderWaitError. Any other outcome of op, including a
// not-leader refusal that names a leader, is returned at once: that one is the
// caller's to forward.
func throughElection[T any](ctx context.Context, h leaderReporter, shardID uint64, timeout time.Duration, op func() (T, error)) (T, error) {
	return holdWhileRefused(ctx, h, shardID, timeout, leaderUnknown, op)
}

// readThroughLeaderChange is throughElection for a read, which a not-leader
// refusal naming a leader also does not end. go-quicraft refuses a ReadIndex
// as not-leader only while this node knows no leader; a refusal that names one
// is failLeaderChangeRequests failing a read the leadership change stranded
// in flight, which never reached a decision. Reissued, the read goes to the
// leader this node now knows -- go-quicraft forwards a follower's ReadIndex
// itself -- and a read has no effect to repeat, so there is nothing for the
// caller to forward: a caller with nowhere to forward it, a SQL session in
// the middle of a transaction, failed the statement instead.
//
// A read is also held while the leader has not yet committed an entry in its
// term: go-quicraft refuses its ReadIndex with quicraft.ErrReadIndexNotReady,
// on the leader and on a follower that forwarded it, from the moment of the
// election until the leader's no-op commits, which a healthy shard does within
// a heartbeat. That is the same election seen one step later, and a
// linearizable read waits it out, as etcd's does, instead of failing the
// caller during a normal leadership change (go-qrdb#668).
//
// A proposal is never reissued this way. A stranded proposal may already be
// in the old leader's log and commit there.
func readThroughLeaderChange[T any](ctx context.Context, h leaderReporter, shardID uint64, timeout time.Duration, op func() (T, error)) (T, error) {
	return holdWhileRefused(ctx, h, shardID, timeout, readRefused, op)
}

// readRefused reports whether err is a refusal readThroughLeaderChange
// reissues a read through: any not-leader refusal, or a leader whose first
// commit in its term is still pending.
func readRefused(err error) bool {
	return notLeader(err) || firstCommitPending(err)
}

// notLeader reports whether err is any go-quicraft not-leader refusal.
func notLeader(err error) bool {
	var nle *quicraft.NotLeaderError
	return errors.As(err, &nle)
}

// firstCommitPending reports whether err is go-quicraft's refusal of a
// ReadIndex because the shard's leader has not yet committed an entry in its
// term.
func firstCommitPending(err error) bool {
	return errors.Is(err, quicraft.ErrReadIndexNotReady)
}

// holdWhileRefused runs op and, while refused reports its error, waits for a
// leader of shardID to be known and runs op again, within timeout and ctx.
func holdWhileRefused[T any](ctx context.Context, h leaderReporter, shardID uint64, timeout time.Duration, refused func(error) bool, op func() (T, error)) (T, error) {
	v, err := op()
	if !refused(err) {
		return v, err
	}

	start := time.Now()
	deadline := start.Add(timeout)
	delay := leaderPollBaseDelay
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		wait := min(delay, time.Until(deadline))
		if wait <= 0 {
			return v, &LeaderWaitError{ShardID: shardID, Waited: time.Since(start), Err: err}
		}
		timer.Reset(wait)
		select {
		case <-ctx.Done():
			return v, &LeaderWaitError{ShardID: shardID, Waited: time.Since(start), Err: err}
		case <-timer.C:
		}
		delay = min(delay*2, leaderPollMaxDelay)

		// A shard this host cannot report on is left to op, which names
		// the failure; only a definite "no leader yet" keeps waiting.
		if _, known, lerr := h.LeaderID(shardID); lerr == nil && !known {
			continue
		}
		v, err = op()
		if !refused(err) {
			return v, err
		}
	}
}
