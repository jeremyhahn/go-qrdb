//go:build !integration

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

package middleware

import (
	"context"
	"errors"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// settlingUserServicer answers GetUserByUsername with errs in order, one per
// call, and with user once they run out: a user store whose shard is electing
// a leader and then has one.
type settlingUserServicer struct {
	mockUserServicer
	errs []error
}

func (s *settlingUserServicer) GetUserByUsername(_ context.Context, _, username string) (*user.User, error) {
	n := int(s.calls.Add(1))
	s.lastUser = username
	if n <= len(s.errs) {
		return nil, s.errs[n-1]
	}
	return s.user, nil
}

// electionTimeout is the error main's CI logged when a request landed while
// the system shard elected: the tenant resolver's metadata read timed out in
// quicraft, wrapped by the service read and the tenant store.
func electionTimeout() error {
	return &tenant.StoreError{Op: "get", Err: &service.ReadError{Err: quicraft.ErrTimeout}}
}

// lostReadUserServicer is the user store run 55455 met after the leader of its
// shard was killed: the index lookup's ReadIndex went to the dead leader, which
// go-quicraft answers with nothing, so the read waits out its context and ends
// in quicraft's timeout. Reads issued from elected on reach the new leader and
// are answered; a zero elected is a shard that never elects.
type lostReadUserServicer struct {
	mockUserServicer
	elected time.Time
}

func (s *lostReadUserServicer) GetUserByUsername(ctx context.Context, _, username string) (*user.User, error) {
	s.calls.Add(1)
	s.lastUser = username
	if s.elected.IsZero() || time.Now().Before(s.elected) {
		<-ctx.Done()
		return nil, &user.StoreError{Op: "get_by_username", Detail: "index lookup", Err: &service.ReadError{Err: quicraft.ErrTimeout}}
	}
	return s.user, nil
}

// TestLookupUser_LostReadRetriedAfterElection: each lookup attempt is bounded,
// so a read lost with the killed leader is re-issued and the one sent after
// the election is answered. With the attempt bound at half the budget, two
// lost reads spent all of it and the request failed with a 503 (run 55455).
func TestLookupUser_LostReadRetriedAfterElection(t *testing.T) {
	want := &user.User{Username: "alice"}
	primary := &lostReadUserServicer{
		mockUserServicer: mockUserServicer{user: want},
		elected:          time.Now().Add(100 * time.Millisecond),
	}
	m := &AuthMiddleware{users: primary, lookupSettle: service.SettlePolicy{Budget: 2 * time.Second, Attempt: 20 * time.Millisecond}}

	got, err := m.lookupUser(context.Background(), "t1", userKey{username: "alice"})
	if err != nil {
		t.Fatalf("lookupUser after an election: %v", err)
	}
	if got != want {
		t.Fatalf("lookupUser = %+v, want %+v", got, want)
	}
	if calls := primary.calls.Load(); calls < 2 {
		t.Fatalf("primary.calls = %d, want lost reads retried before the answer", calls)
	}
}

// TestLookupUser_LostReadNeverAnsweredEndsWithBudget: a store that never
// answers still ends the lookup, with the settle budget and the election's
// error, not one attempt bound past it.
func TestLookupUser_LostReadNeverAnsweredEndsWithBudget(t *testing.T) {
	primary := &lostReadUserServicer{}
	policy := service.SettlePolicy{Budget: 400 * time.Millisecond, Attempt: 300 * time.Millisecond}
	m := &AuthMiddleware{users: primary, lookupSettle: policy}

	start := time.Now()
	_, err := m.lookupUser(context.Background(), "t1", userKey{username: "alice"})
	elapsed := time.Since(start)
	if !errors.Is(err, quicraft.ErrTimeout) {
		t.Fatalf("lookupUser err = %v, want the lost read's %v", err, quicraft.ErrTimeout)
	}
	if elapsed < policy.Budget || elapsed >= policy.Budget+policy.Attempt/2 {
		t.Fatalf("lookupUser took %v, want it to end with its %v budget", elapsed, policy.Budget)
	}
	if calls := primary.calls.Load(); calls != 2 {
		t.Fatalf("primary.calls = %d, want the first attempt's bound and the budget's remainder", calls)
	}
}

// TestLookupUser_WaitsOutElection: a non-system tenant has no fallback store,
// so before the user lookup waited out an election the first timed-out read
// was the request's answer -- a 503 on every transport. The lookup now retries
// the election's errors and answers with the user once a leader serves.
func TestLookupUser_WaitsOutElection(t *testing.T) {
	want := &user.User{Username: "alice"}
	primary := &settlingUserServicer{
		mockUserServicer: mockUserServicer{user: want},
		errs: []error{
			electionTimeout(),
			&service.NotLeaderError{ShardID: 1},
			&tenant.StoreError{Op: "get", Err: &service.ReadError{Err: quicraft.ErrReadIndexNotReady}},
		},
	}
	m := &AuthMiddleware{users: primary}

	got, err := m.lookupUser(context.Background(), "t1", userKey{username: "alice"})
	if err != nil {
		t.Fatalf("lookupUser during an election: %v", err)
	}
	if got != want {
		t.Fatalf("lookupUser = %+v, want %+v", got, want)
	}
	if calls := primary.calls.Load(); calls != 4 {
		t.Fatalf("primary.calls = %d, want 4: three election errors then the answer", calls)
	}
}

// TestLookupUser_NonTransientErrorIsFinal: an error that is not the shard
// electing is the answer at once; waiting would only delay it.
func TestLookupUser_NonTransientErrorIsFinal(t *testing.T) {
	cause := errors.New("tenant record corrupt")
	primary := &settlingUserServicer{
		mockUserServicer: mockUserServicer{user: &user.User{Username: "alice"}},
		errs:             []error{&tenant.StoreError{Op: "get", Err: cause}},
	}
	m := &AuthMiddleware{users: primary}

	_, err := m.lookupUser(context.Background(), "t1", userKey{username: "alice"})
	if !errors.Is(err, cause) {
		t.Fatalf("lookupUser err = %v, want %v", err, cause)
	}
	if calls := primary.calls.Load(); calls != 1 {
		t.Fatalf("primary.calls = %d, want 1: a non-election error is not retried", calls)
	}
}

// TestLookupUser_ElectionOutlastsBudget: an election that outlasts the budget
// leaves its error standing -- the caller's retryable 503 -- rather than
// holding the request open.
func TestLookupUser_ElectionOutlastsBudget(t *testing.T) {
	primary := &settlingUserServicer{errs: []error{
		electionTimeout(), electionTimeout(), electionTimeout(), electionTimeout(),
		electionTimeout(), electionTimeout(), electionTimeout(), electionTimeout(),
	}}
	m := &AuthMiddleware{users: primary, lookupSettle: service.SettlePolicy{Budget: 150 * time.Millisecond}}

	start := time.Now()
	_, err := m.lookupUser(context.Background(), "t1", userKey{username: "alice"})
	if !errors.Is(err, quicraft.ErrTimeout) {
		t.Fatalf("lookupUser err = %v, want the election's %v", err, quicraft.ErrTimeout)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("lookupUser took %v, want it bounded by its 150ms budget", elapsed)
	}
	if calls := primary.calls.Load(); calls < 2 || calls >= int64(len(primary.errs)) {
		t.Fatalf("primary.calls = %d, want a retry and the budget to stop it before %d", calls, len(primary.errs))
	}
}

// TestLookupUser_ContextEndsTheWait: a request whose caller has gone stops
// waiting at once and keeps the election's error.
func TestLookupUser_ContextEndsTheWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	primary := &cancellingUserServicer{cancel: cancel}
	m := &AuthMiddleware{users: primary}

	_, err := m.lookupUser(ctx, "t1", userKey{username: "alice"})
	if !errors.Is(err, quicraft.ErrTimeout) {
		t.Fatalf("lookupUser err = %v, want the election's %v", err, quicraft.ErrTimeout)
	}
	if calls := primary.calls.Load(); calls != 1 {
		t.Fatalf("primary.calls = %d, want 1: an ended context is not retried", calls)
	}
}

// TestLookupUser_ContextEndsDuringPoll: a caller that goes while the lookup
// waits between attempts ends the wait with the election's error.
func TestLookupUser_ContextEndsDuringPoll(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*service.DefaultSettlePolicy.Poll+service.DefaultSettlePolicy.Poll/2)
	defer cancel()
	primary := &settlingUserServicer{errs: []error{
		electionTimeout(), electionTimeout(), electionTimeout(), electionTimeout(),
		electionTimeout(), electionTimeout(), electionTimeout(), electionTimeout(),
	}}
	m := &AuthMiddleware{users: primary}

	_, err := m.lookupUser(ctx, "t1", userKey{username: "alice"})
	if !errors.Is(err, quicraft.ErrTimeout) {
		t.Fatalf("lookupUser err = %v, want the election's %v", err, quicraft.ErrTimeout)
	}
	if calls := primary.calls.Load(); calls < 2 || calls >= int64(len(primary.errs)) {
		t.Fatalf("primary.calls = %d, want a retry and the context to stop it before %d", calls, len(primary.errs))
	}
}

// TestLookupUser_SystemTenantWaitsWhenFallbackAlsoElecting: the system
// tenant's fallback answers a primary election at once; when the fallback is
// electing too, the lookup waits for the primary like any other tenant.
func TestLookupUser_SystemTenantWaitsWhenFallbackAlsoElecting(t *testing.T) {
	want := &user.User{Username: "bob"}
	primary := &settlingUserServicer{
		mockUserServicer: mockUserServicer{user: want},
		errs:             []error{electionTimeout()},
	}
	fallback := &mockUserServicer{err: &service.ReadError{Err: quicraft.ErrNotLeader}}
	m := &AuthMiddleware{users: primary, usersFallback: fallback}

	got, err := m.lookupUser(context.Background(), tenantid.System, userKey{username: "bob"})
	if err != nil {
		t.Fatalf("lookupUser during an election: %v", err)
	}
	if got != want {
		t.Fatalf("lookupUser = %+v, want %+v", got, want)
	}
	if calls := fallback.calls.Load(); calls != 1 {
		t.Fatalf("fallback.calls = %d, want 1: consulted on the first attempt only", calls)
	}
}

// cancellingUserServicer fails with an election timeout and ends its caller's
// context, as a client that gives up mid-election does.
type cancellingUserServicer struct {
	mockUserServicer
	cancel context.CancelFunc
}

func (c *cancellingUserServicer) GetUserByUsername(context.Context, string, string) (*user.User, error) {
	c.calls.Add(1)
	c.cancel()
	return nil, electionTimeout()
}
