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

package user

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// getFailKV fails Get while err is set; everything else is the memory store.
type getFailKV struct {
	*memKVStore
	err error
}

func (s *getFailKV) Get(ctx context.Context, key string) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.memKVStore.Get(ctx, key)
}

func TestClaimExpiredPending_ErrorReturns(t *testing.T) {
	kv := &getFailKV{memKVStore: newMemKVStore()}
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	expirer := svc.(PendingExpirer)
	later := time.Now().UTC().Add(time.Hour)
	u := createPending(t, svc, "claimerr", "acme", time.Minute)

	var invalid *ValidationError
	if _, err := expirer.ClaimExpiredPending(ctx, 0, later); !errors.As(err, &invalid) {
		t.Fatalf("claim of id 0 = %v, want ValidationError", err)
	}
	var notFound *UserNotFoundError
	if _, err := expirer.ClaimExpiredPending(ctx, u.ID+1000, later); !errors.As(err, &notFound) {
		t.Fatalf("claim of a missing id = %v, want UserNotFoundError", err)
	}

	boom := errors.New("kv unavailable")
	kv.err = boom
	if _, err := expirer.ClaimExpiredPending(ctx, u.ID, later); !errors.Is(err, boom) {
		t.Fatalf("claim with a failing read = %v, want the read error", err)
	}
	kv.err = nil

	if err := kv.memKVStore.Put(ctx, primaryKey(u.ID), []byte("{not json")); err != nil {
		t.Fatalf("corrupt record: %v", err)
	}
	var storeErr *StoreError
	if _, err := expirer.ClaimExpiredPending(ctx, u.ID, later); !errors.As(err, &storeErr) {
		t.Fatalf("claim of an unreadable record = %v, want StoreError", err)
	}
}

// A system account is never claimed, even with a lapsed TTL on the record.
func TestClaimExpiredPending_RefusesSystemAccount(t *testing.T) {
	kv := newMemKVStore()
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	u := createPending(t, svc, "sysacct", "acme", time.Minute)
	raw, err := kv.Get(ctx, primaryKey(u.ID))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	rec := &User{}
	if err := json.Unmarshal(raw, rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	rec.System = true
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := kv.Put(ctx, primaryKey(u.ID), data); err != nil {
		t.Fatalf("Put: %v", err)
	}
	var notPending *AccountNotPendingError
	if _, err := svc.(PendingExpirer).ClaimExpiredPending(ctx, u.ID, time.Now().UTC().Add(time.Hour)); !errors.As(err, &notPending) {
		t.Fatalf("claim of a system account = %v, want AccountNotPendingError", err)
	}
}

// An activation that commits between the claim's read and its conditional
// write wins: the claim fails, nothing is marked, and the reaper's next
// sweep leaves the now-active account alone. The window is hit by the
// store's afterGet hook, not by timing.
func TestClaimExpiredPending_ActivationInWindowWins(t *testing.T) {
	kv := newMemKVStore()
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	clock := &fakeClock{now: time.Now().UTC().Add(time.Hour)}
	u := createPending(t, svc, "winner", "acme", time.Minute)
	factors := &fakeFactors{byUser: map[uint64][]*auth.AuthFactor{u.ID: {{ID: "f-w", UserID: u.ID}}}}
	r, err := NewPendingReaper(PendingReaperConfig{Users: svc, Factors: factors, Now: clock.Now})
	if err != nil {
		t.Fatalf("NewPendingReaper: %v", err)
	}

	key := primaryKey(u.ID)
	fired := false
	kv.afterGet = func(k string) {
		if k != key || fired {
			return
		}
		fired = true
		kv.afterGet = nil
		if err := svc.EnableUser(ctx, u.ID); err != nil {
			t.Errorf("EnableUser in the claim window: %v", err)
		}
	}
	var lost *ConcurrentUpdateError
	if _, err := svc.(PendingExpirer).ClaimExpiredPending(ctx, u.ID, clock.Now()); !errors.As(err, &lost) {
		t.Fatalf("claim racing an activation = %v; want ConcurrentUpdateError", err)
	}
	if !fired {
		t.Fatal("the claim window was never reached")
	}
	got, err := svc.GetUser(ctx, u.ID)
	if err != nil || got.Lifecycle != LifecycleActive || got.ExpiryClaim != nil {
		t.Fatalf("account after the race = %+v, %v; want active and unclaimed", got, err)
	}
	if left, _ := factors.List(ctx, u.ID); len(left) != 1 {
		t.Fatalf("factors of the activated account purged: %d left", len(left))
	}
	if n, err := r.Sweep(ctx, "acme"); n != 0 || err != nil {
		t.Fatalf("sweep after activation = %d, %v; want 0, nil", n, err)
	}
}

// scriptedExpirer replaces the claim and the delete with scripted results.
type scriptedExpirer struct {
	UserServicer
	PendingExpirer
	claimErr  error
	deleteErr error
	claims    int
	deletes   int
}

func (s *scriptedExpirer) ClaimExpiredPending(ctx context.Context, id uint64, now time.Time) (*User, error) {
	s.claims++
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	return s.PendingExpirer.ClaimExpiredPending(ctx, id, now)
}

func (s *scriptedExpirer) DeleteExpiredPending(ctx context.Context, id uint64, now time.Time) (*User, error) {
	s.deletes++
	if s.deleteErr != nil {
		return nil, s.deleteErr
	}
	return s.PendingExpirer.DeleteExpiredPending(ctx, id, now)
}

// reap's error paths: a failed claim is reported and touches nothing; an
// account gone before the claim or before the delete is skipped quietly.
func TestPendingReaper_ReapErrorPaths(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{now: time.Now().UTC().Add(time.Hour)}
	boom := errors.New("claim write failed")
	for name, tc := range map[string]struct {
		claimErr, deleteErr error
		wantErr             bool
		wantPurged          bool
	}{
		"claim fails":          {claimErr: boom, wantErr: true},
		"gone before claim":    {claimErr: &UserNotFoundError{ID: 1}},
		"not pending at claim": {claimErr: &AccountNotPendingError{ID: 1}},
		"gone before delete":   {deleteErr: &UserNotFoundError{ID: 1}, wantPurged: true},
		"delete fails":         {deleteErr: boom, wantErr: true, wantPurged: true},
	} {
		t.Run(name, func(t *testing.T) {
			svc := newTestStore(t)
			u := createPending(t, svc, "reaperr", "acme", time.Minute)
			factors := &fakeFactors{byUser: map[uint64][]*auth.AuthFactor{u.ID: {{ID: "f-e", UserID: u.ID}}}}
			users := &scriptedExpirer{UserServicer: svc, PendingExpirer: svc.(PendingExpirer), claimErr: tc.claimErr, deleteErr: tc.deleteErr}
			r, err := NewPendingReaper(PendingReaperConfig{Users: users, Factors: factors, Now: clock.Now})
			if err != nil {
				t.Fatalf("NewPendingReaper: %v", err)
			}
			n, err := r.Sweep(ctx, "acme")
			if n != 0 || (err != nil) != tc.wantErr {
				t.Fatalf("sweep = %d, %v; want 0, error %v", n, err, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, boom) {
				t.Fatalf("sweep error %v does not carry the cause", err)
			}
			left, _ := factors.List(ctx, u.ID)
			if purged := len(left) == 0; purged != tc.wantPurged {
				t.Fatalf("factors purged = %v, want %v", purged, tc.wantPurged)
			}
			if tc.claimErr != nil && users.deletes != 0 {
				t.Fatalf("delete attempted %d times after a failed claim", users.deletes)
			}
		})
	}
}

// listFailUsers fails ListUsers, so a sweep fails before reaping anything.
// The switch is atomic: the reaper's goroutine reads it while the test flips it.
type listFailUsers struct {
	UserServicer
	PendingExpirer
	fail atomic.Bool
}

var errListFailed = errors.New("list failed")

func (l *listFailUsers) ListUsers(ctx context.Context, req *ListUsersRequest) (*ListUsersResponse, error) {
	if l.fail.Load() {
		return nil, errListFailed
	}
	return l.UserServicer.ListUsers(ctx, req)
}

// Run skips ticks off the leader, survives a tenant-listing failure and a
// failed sweep, keeps sweeping, and returns when the tick channel closes.
func TestPendingReaper_RunSurvivesFailuresAndStopsOnClose(t *testing.T) {
	svc := newTestStore(t)
	clock := &fakeClock{now: time.Now().UTC().Add(time.Hour)}
	u := createPending(t, svc, "runner", "acme", time.Minute)
	users := &listFailUsers{UserServicer: svc, PendingExpirer: svc.(PendingExpirer)}
	users.fail.Store(true)
	// The reaper's goroutine reads these while the test flips them.
	var leader, failTenants atomic.Bool
	failTenants.Store(true)
	r, err := NewPendingReaper(PendingReaperConfig{Users: users, Now: clock.Now, IsLeader: func() bool { return leader.Load() }})
	if err != nil {
		t.Fatalf("NewPendingReaper: %v", err)
	}

	ticks := make(chan time.Time)
	listed := make(chan struct{}, 8)
	tenantsErr := errors.New("tenant listing failed")
	tenants := func(context.Context) ([]string, error) {
		defer func() { listed <- struct{}{} }()
		if failTenants.Load() {
			return nil, tenantsErr
		}
		return []string{"acme"}, nil
	}
	done := make(chan struct{})
	go func() {
		r.Run(context.Background(), ticks, tenants)
		close(done)
	}()

	// Off the leader: the tick is consumed and nothing is listed. The next
	// unbuffered send only completes once Run is back in its select.
	ticks <- time.Time{}
	leader.Store(true)
	ticks <- time.Time{} // tenants fail
	<-listed
	failTenants.Store(false)
	ticks <- time.Time{} // the sweep fails on ListUsers
	<-listed
	users.fail.Store(false)
	ticks <- time.Time{} // the sweep succeeds
	<-listed
	close(ticks)
	<-done

	if len(listed) != 0 {
		t.Fatalf("tenants listed %d extra times (off-leader tick listed?)", len(listed))
	}
	if _, err := svc.GetUser(context.Background(), u.ID); err == nil {
		t.Fatal("account not reaped by the sweep after the failures")
	}
}

// Run returns when its context ends.
func TestPendingReaper_RunStopsOnContextEnd(t *testing.T) {
	r, err := NewPendingReaper(PendingReaperConfig{Users: newTestStore(t)})
	if err != nil {
		t.Fatalf("NewPendingReaper: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.Run(ctx, make(chan time.Time), func(context.Context) ([]string, error) { return nil, nil })
}

// The tenant router claims and deletes in the tenant that holds the account,
// and a foreign tenant cannot reach it.
func TestRoutingUserService_ClaimAndDeleteExpiredPending(t *testing.T) {
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"acme": newFakeKVStore(), "globex": newFakeKVStore()},
	}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	ctx := tenant.WithTenantID(context.Background(), "acme")
	u, err := svc.CreateUser(ctx, &CreateUserRequest{
		WithoutEmailLoginKey: true, Username: "routed", TenantID: "acme", Pending: true, PendingTTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	expirer, ok := svc.(PendingExpirer)
	if !ok {
		t.Fatal("routing service is not a PendingExpirer")
	}
	later := time.Now().UTC().Add(time.Hour)

	foreign := tenant.WithTenantID(context.Background(), "globex")
	var notFound *UserNotFoundError
	if _, err := expirer.ClaimExpiredPending(foreign, u.ID, later); !errors.As(err, &notFound) {
		t.Fatalf("foreign claim = %v, want UserNotFoundError", err)
	}
	if _, err := expirer.DeleteExpiredPending(foreign, u.ID, later); !errors.As(err, &notFound) {
		t.Fatalf("foreign delete = %v, want UserNotFoundError", err)
	}

	claimed, err := expirer.ClaimExpiredPending(ctx, u.ID, later)
	if err != nil || !ExpiryClaimed(claimed) {
		t.Fatalf("claim = %+v, %v; want a claimed record", claimed, err)
	}
	if _, err := expirer.DeleteExpiredPending(ctx, u.ID, later); err != nil {
		t.Fatalf("delete of the claimed account: %v", err)
	}
	if _, err := svc.GetUser(ctx, u.ID); err == nil {
		t.Fatal("routed account still present after the delete")
	}
}
