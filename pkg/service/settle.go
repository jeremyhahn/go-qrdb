// Copyright (c) 2026 Jeremy Hahn
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

package service

import (
	"context"
	"time"
)

// SettlePolicy is how long a read waits out its shard electing a leader or
// moving one (LeadershipTransient) before its error stands.
//
// Attempt bounds each try, and is the bound that decides whether the wait can
// outlast an election at all. A follower forwards a ReadIndex to the replica it
// knows as leader, and go-quicraft answers one lost with that leader -- killed
// before it replied -- with nothing: the read waits until its context ends. So
// a try issued just after a leader dies costs its whole bound, and only a new
// try reaches the next leader. A bound longer than an election timeout buys
// nothing for that: a live leader confirms a ReadIndex with one heartbeat
// round, and one that cannot reach a quorum within an election timeout steps
// down. Budget bounds the whole wait, so the last try ends with it rather than
// one Attempt after it.
type SettlePolicy struct {
	// Budget bounds the whole wait, measured from the first attempt.
	Budget time.Duration
	// Attempt bounds each attempt, within what remains of Budget.
	Attempt time.Duration
	// Poll paces the attempts after a transient error.
	Poll time.Duration
}

// DefaultSettlePolicy is the wait every read in front of a request takes: at
// go-quicraft's default tuning an election times out in 2 s, randomized up to
// twice that, and the budget covers a few rounds of it, a split vote and the
// new leader's first commit. A try lost to a dead leader costs one election
// timeout of it, not half of it.
var DefaultSettlePolicy = SettlePolicy{
	Budget:  10 * time.Second,
	Attempt: 2 * time.Second,
	Poll:    100 * time.Millisecond,
}

// orDefault returns p, with each zero field read as DefaultSettlePolicy's.
func (p SettlePolicy) orDefault() SettlePolicy {
	if p.Budget == 0 {
		p.Budget = DefaultSettlePolicy.Budget
	}
	if p.Attempt == 0 {
		p.Attempt = DefaultSettlePolicy.Attempt
	}
	if p.Poll == 0 {
		p.Poll = DefaultSettlePolicy.Poll
	}
	return p
}

// SettleLeadership runs attempt until it answers with anything but a
// LeadershipTransient error, so a read issued while its shard elects a leader
// or moves one waits the election out instead of failing with it.
//
// Each attempt gets a context derived from ctx that ends after policy.Attempt
// or when policy.Budget has passed since the call began, whichever is first;
// attempt must bound its work with it. The first attempt runs at once; each
// retry follows policy.Poll later, or sooner if less of the budget remains.
// The wait ends when an attempt succeeds or fails for any other reason (that
// answer is returned as is), when ctx ends, or when the budget has passed; the
// last attempt's transient error then stands, with the zero T. A zero field
// of policy reads as DefaultSettlePolicy's.
func SettleLeadership[T any](ctx context.Context, policy SettlePolicy, attempt func(context.Context) (T, error)) (T, error) {
	var zero T
	policy = policy.orDefault()
	deadline := time.Now().Add(policy.Budget)
	budgetCtx, cancelBudget := context.WithDeadline(ctx, deadline)
	defer cancelBudget()
	for {
		attemptCtx, cancelAttempt := context.WithTimeout(budgetCtx, policy.Attempt)
		v, err := attempt(attemptCtx)
		cancelAttempt()
		if err == nil || !LeadershipTransient(err) || ctx.Err() != nil {
			return v, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return zero, err
		}
		select {
		case <-ctx.Done():
			return zero, err
		case <-time.After(min(policy.Poll, remaining)):
		}
	}
}

// Settle is SettleLeadership for an attempt that answers with its error alone,
// such as a write: the shape of cluster.Settler, which a composer builds from
// it for the boot-time member registration.
func Settle(ctx context.Context, policy SettlePolicy, attempt func(context.Context) error) error {
	_, err := SettleLeadership(ctx, policy, func(attemptCtx context.Context) (struct{}, error) {
		return struct{}{}, attempt(attemptCtx)
	})
	return err
}
