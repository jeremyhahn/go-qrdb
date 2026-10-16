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

const (
	testSettleBudget = 10 * time.Second
	testSettlePoll   = time.Millisecond
)

// settleAttempts returns an attempt func answering errs, one per call, then
// "ok", and a pointer to its call count.
func settleAttempts(errs ...error) (func() (string, error), *int) {
	calls := 0
	return func() (string, error) {
		calls++
		if calls <= len(errs) {
			return "partial", errs[calls-1]
		}
		return "ok", nil
	}, &calls
}

func TestSettleLeadership_WaitsOutElection(t *testing.T) {
	attempt, calls := settleAttempts(
		&ReadError{Err: quicraft.ErrTimeout},
		&NotLeaderError{},
		&ReadError{Err: quicraft.ErrReadIndexNotReady},
	)
	v, err := SettleLeadership(context.Background(), testSettleBudget, testSettlePoll, attempt)
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
	v, err := SettleLeadership(context.Background(), testSettleBudget, testSettlePoll, attempt)
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
	v, err := SettleLeadership(context.Background(), 20*time.Millisecond, testSettlePoll, attempt)
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
	v, err := SettleLeadership(ctx, testSettleBudget, testSettlePoll, attempt)
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
	v, err := SettleLeadership(ctx, testSettleBudget, time.Hour, func() (string, error) {
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
