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
	"errors"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
)

// testSettlePolicy is a wait long enough that no test here outlasts it by
// accident, with a poll short enough that retries cost nothing.
var testSettlePolicy = SettlePolicy{Budget: 10 * time.Second, Attempt: time.Second, Poll: time.Millisecond}

// settleAttempts returns an attempt func answering errs, one per call, then
// "ok", and a pointer to its call count.
func settleAttempts(errs ...error) (func(context.Context) (string, error), *int) {
	calls := 0
	return func(context.Context) (string, error) {
		calls++
		if calls <= len(errs) {
			return "partial", errs[calls-1]
		}
		return "ok", nil
	}, &calls
}

// lostRead is a read whose ReadIndex was forwarded to a leader killed before
// it replied: go-quicraft answers it with nothing, so it waits for its context
// and then reports the elapsed deadline as quicraft does.
func lostRead(ctx context.Context) (string, error) {
	<-ctx.Done()
	return "partial", &ReadError{Err: quicraft.ErrTimeout}
}

func TestSettleLeadership_WaitsOutElection(t *testing.T) {
	attempt, calls := settleAttempts(
		&ReadError{Err: quicraft.ErrTimeout},
		&NotLeaderError{},
		&ReadError{Err: quicraft.ErrReadIndexNotReady},
	)
	v, err := SettleLeadership(context.Background(), testSettlePolicy, attempt)
	if err != nil || v != "ok" {
		t.Fatalf("SettleLeadership = (%q, %v), want (\"ok\", nil)", v, err)
	}
	if *calls != 4 {
		t.Fatalf("calls = %d, want 3 elections and the answer", *calls)
	}
}

func TestSettleLeadership_NonTransientErrorIsFinal(t *testing.T) {
	final := errors.New("not found")
	attempt, calls := settleAttempts(final)
	v, err := SettleLeadership(context.Background(), testSettlePolicy, attempt)
	if !errors.Is(err, final) || v != "partial" {
		t.Fatalf("SettleLeadership = (%q, %v), want the attempt's own answer (\"partial\", %v)", v, err, final)
	}
	if *calls != 1 {
		t.Fatalf("calls = %d, want a final error never retried", *calls)
	}
}

func TestSettleLeadership_ElectionOutlastsBudget(t *testing.T) {
	errs := make([]error, 1000)
	for i := range errs {
		errs[i] = &ReadError{Err: quicraft.ErrTimeout}
	}
	attempt, calls := settleAttempts(errs...)
	policy := testSettlePolicy
	policy.Budget = 20 * time.Millisecond
	v, err := SettleLeadership(context.Background(), policy, attempt)
	if !errors.Is(err, quicraft.ErrTimeout) || v != "" {
		t.Fatalf("SettleLeadership = (%q, %v), want the zero value and the election's %v", v, err, quicraft.ErrTimeout)
	}
	if *calls < 2 || *calls >= len(errs) {
		t.Fatalf("calls = %d, want a retry and the budget to stop it before %d", *calls, len(errs))
	}
}

func TestSettleLeadership_CancelledContextIsNotRetried(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attempt, calls := settleAttempts(&ReadError{Err: quicraft.ErrTimeout})
	v, err := SettleLeadership(ctx, testSettlePolicy, attempt)
	if !errors.Is(err, quicraft.ErrTimeout) || v != "partial" {
		t.Fatalf("SettleLeadership = (%q, %v), want the attempt's own answer", v, err)
	}
	if *calls != 1 {
		t.Fatalf("calls = %d, want a cancelled request never retried", *calls)
	}
}

func TestSettleLeadership_ContextEndsDuringPoll(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	calls := 0
	policy := testSettlePolicy
	policy.Poll = time.Hour
	v, err := SettleLeadership(ctx, policy, func(context.Context) (string, error) {
		calls++
		return "partial", &ReadError{Err: quicraft.ErrTimeout}
	})
	if !errors.Is(err, quicraft.ErrTimeout) || v != "" {
		t.Fatalf("SettleLeadership = (%q, %v), want the zero value and the election's error", v, err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want the context to end the poll before a second attempt", calls)
	}
}

// TestSettleLeadership_LostReadRetriedOnNextLeader: reads issued while the
// killed leader is still the one the follower knows are lost, and only a new
// attempt reaches the next leader. Each attempt is bounded, so the election
// costs a few lost attempts and the read is answered within the budget.
func TestSettleLeadership_LostReadRetriedOnNextLeader(t *testing.T) {
	policy := SettlePolicy{Budget: 2 * time.Second, Attempt: 20 * time.Millisecond, Poll: time.Millisecond}
	elected := time.Now().Add(100 * time.Millisecond)
	calls := 0
	v, err := SettleLeadership(context.Background(), policy, func(ctx context.Context) (string, error) {
		calls++
		if time.Now().Before(elected) {
			return lostRead(ctx)
		}
		return "ok", nil
	})
	if err != nil || v != "ok" {
		t.Fatalf("SettleLeadership = (%q, %v), want the next leader's answer", v, err)
	}
	if calls < 2 {
		t.Fatalf("calls = %d, want lost attempts retried before the answer", calls)
	}
}

// TestSettleLeadership_NeverAnsweredEndsWithBudget: a read that is never
// answered still ends, and with the budget -- the last attempt is cut at it,
// not left to run its own bound past it.
func TestSettleLeadership_NeverAnsweredEndsWithBudget(t *testing.T) {
	policy := SettlePolicy{Budget: 400 * time.Millisecond, Attempt: 300 * time.Millisecond, Poll: time.Millisecond}
	calls := 0
	start := time.Now()
	v, err := SettleLeadership(context.Background(), policy, func(ctx context.Context) (string, error) {
		calls++
		return lostRead(ctx)
	})
	elapsed := time.Since(start)
	if !errors.Is(err, quicraft.ErrTimeout) || v != "" {
		t.Fatalf("SettleLeadership = (%q, %v), want the zero value and the lost read's %v", v, err, quicraft.ErrTimeout)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want the first attempt's bound and the budget's remainder", calls)
	}
	if elapsed < policy.Budget || elapsed >= policy.Budget+policy.Attempt/2 {
		t.Fatalf("elapsed = %v, want the wait to end with the %v budget", elapsed, policy.Budget)
	}
}

func TestSettlePolicy_ZeroFieldsReadAsDefault(t *testing.T) {
	if got := (SettlePolicy{}).orDefault(); got != DefaultSettlePolicy {
		t.Fatalf("zero policy = %+v, want %+v", got, DefaultSettlePolicy)
	}
	set := SettlePolicy{Budget: time.Second, Attempt: time.Millisecond, Poll: time.Microsecond}
	if got := set.orDefault(); got != set {
		t.Fatalf("set policy = %+v, want it unchanged", got)
	}
	if got := (SettlePolicy{Attempt: time.Millisecond}).orDefault(); got.Attempt != time.Millisecond || got.Budget != DefaultSettlePolicy.Budget || got.Poll != DefaultSettlePolicy.Poll {
		t.Fatalf("partial policy = %+v, want only the zero fields defaulted", got)
	}
}

// TestSettle_WaitsOutElection: a write proposed into an election -- one a
// stepping-down leader may have accepted among them -- lands once a leader
// takes it.
func TestSettle_WaitsOutElection(t *testing.T) {
	attempt, calls := settleAttempts(
		&ProposeError{Err: &quicraft.MaybeAppliedError{Err: &quicraft.NotLeaderError{ShardID: 1}}},
		&NotLeaderError{},
	)
	err := Settle(context.Background(), testSettlePolicy, func(ctx context.Context) error {
		_, err := attempt(ctx)
		return err
	})
	if err != nil {
		t.Fatalf("Settle = %v, want nil", err)
	}
	if *calls != 3 {
		t.Fatalf("calls = %d, want 2 elections and the answer", *calls)
	}
}

// TestSettle_NonTransientErrorIsFinal: a refusal that is not an election is
// the write's answer, returned at once.
func TestSettle_NonTransientErrorIsFinal(t *testing.T) {
	final := errors.New("statemachine: lease not configured")
	calls := 0
	err := Settle(context.Background(), testSettlePolicy, func(context.Context) error {
		calls++
		return final
	})
	if !errors.Is(err, final) || calls != 1 {
		t.Fatalf("Settle = %v after %d calls, want %v after 1", err, calls, final)
	}
}

// TestSettle_ElectionOutlastsBudget: an election longer than the budget
// leaves its own error standing.
func TestSettle_ElectionOutlastsBudget(t *testing.T) {
	policy := SettlePolicy{Budget: 20 * time.Millisecond, Attempt: time.Second, Poll: time.Millisecond}
	err := Settle(context.Background(), policy, func(context.Context) error {
		return &ProposeError{Err: quicraft.ErrTimeout}
	})
	if !errors.Is(err, quicraft.ErrTimeout) {
		t.Fatalf("Settle = %v, want the election's %v", err, quicraft.ErrTimeout)
	}
}

// TestSettle_AttemptBoundedByPolicy: a write that is never answered gets its
// own attempt deadline, so the next attempt can reach the next leader within
// the budget rather than the first hanging until the caller's context ends.
func TestSettle_AttemptBoundedByPolicy(t *testing.T) {
	policy := SettlePolicy{Budget: time.Second, Attempt: 20 * time.Millisecond, Poll: time.Millisecond}
	calls := 0
	err := Settle(context.Background(), policy, func(ctx context.Context) error {
		calls++
		if calls < 3 {
			_, err := lostRead(ctx)
			return err
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("Settle = %v after %d calls, want nil after 3", err, calls)
	}
}
