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
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// fakeClock is a hand-advanced clock: no test here sleeps.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeFactors records factors per user and which ones were deleted.
type fakeFactors struct {
	mu      sync.Mutex
	byUser  map[uint64][]*auth.AuthFactor
	deleted []string
	// failDelete, while set, makes Purge (and Delete) fail without removing
	// anything.
	failDelete error
	// onPurge, when set, runs at the start of Purge, between the claim and
	// the removal.
	onPurge func(userID uint64)
}

func (f *fakeFactors) Purge(_ context.Context, userID uint64) (int, error) {
	if f.onPurge != nil {
		f.onPurge(userID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failDelete != nil {
		return 0, f.failDelete
	}
	n := len(f.byUser[userID])
	for _, fa := range f.byUser[userID] {
		f.deleted = append(f.deleted, fa.ID)
	}
	delete(f.byUser, userID)
	return n, nil
}

func (f *fakeFactors) List(_ context.Context, userID uint64) ([]*auth.AuthFactor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*auth.AuthFactor(nil), f.byUser[userID]...), nil
}

func (f *fakeFactors) Delete(_ context.Context, factorID string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failDelete != nil {
		return f.failDelete
	}
	for uid, list := range f.byUser {
		for i, fa := range list {
			if fa.ID == factorID {
				f.byUser[uid] = append(list[:i:i], list[i+1:]...)
				f.deleted = append(f.deleted, factorID)
				return nil
			}
		}
	}
	return &auth.FactorNotFoundError{FactorID: factorID}
}

func createPending(t *testing.T, svc UserServicer, name, tenant string, ttl time.Duration) *User {
	t.Helper()
	u, err := svc.CreateUser(context.Background(), &CreateUserRequest{
		Username:   name,
		Email:      name + "@example.com",
		TenantID:   tenant,
		Pending:    true,
		PendingTTL: ttl,
	})
	if err != nil {
		t.Fatalf("CreateUser(%s): %v", name, err)
	}
	return u
}

func TestCreateUser_PendingTTLStampsExpiry(t *testing.T) {
	svc := newTestStore(t)
	before := time.Now().UTC()
	u := createPending(t, svc, "alice", "acme", time.Hour)
	if u.Lifecycle != LifecyclePending {
		t.Fatalf("lifecycle = %q, want pending", u.Lifecycle)
	}
	if u.PendingExpiresAt == nil {
		t.Fatal("PendingExpiresAt not set")
	}
	if d := u.PendingExpiresAt.Sub(u.CreatedAt); d != time.Hour {
		t.Fatalf("expiry - created = %v, want 1h", d)
	}
	if u.PendingExpiresAt.Before(before.Add(time.Hour)) {
		t.Fatalf("expiry %v earlier than create time + TTL", u.PendingExpiresAt)
	}
	got, err := svc.GetUser(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.PendingExpiresAt == nil || !got.PendingExpiresAt.Equal(*u.PendingExpiresAt) {
		t.Fatalf("persisted expiry = %v, want %v", got.PendingExpiresAt, u.PendingExpiresAt)
	}
}

func TestCreateUser_DefaultIsActiveWithoutExpiry(t *testing.T) {
	svc := newTestStore(t)
	u := createTestUser(t, svc, "bob", "acme")
	if u.Lifecycle != LifecycleActive || u.PendingExpiresAt != nil {
		t.Fatalf("default create = %q expiry %v, want active and none", u.Lifecycle, u.PendingExpiresAt)
	}
	p := createPending(t, svc, "carol", "acme", 0)
	if p.PendingExpiresAt != nil {
		t.Fatalf("pending without TTL got expiry %v", p.PendingExpiresAt)
	}
}

func TestCreateUser_PendingTTLRefusals(t *testing.T) {
	svc := newTestStore(t)
	cases := map[string]*CreateUserRequest{
		"negative":  {Username: "n", Email: "n@example.com", Pending: true, PendingTTL: -time.Second},
		"on active": {Username: "a", Email: "a@example.com", PendingTTL: time.Hour},
		"on disabled": {Username: "d", Email: "d@example.com", Lifecycle: LifecycleDisabled,
			PendingTTL: time.Hour},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := svc.CreateUser(context.Background(), req)
			var verr *ValidationError
			if !errors.As(err, &verr) || verr.Field != "pending_ttl" {
				t.Fatalf("err = %v, want ValidationError on pending_ttl", err)
			}
		})
	}
}

func TestEnableUser_ActivatesPendingClearsTTLAndIsIdempotent(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	u := createPending(t, svc, "dave", "acme", time.Minute)
	for i := 0; i < 2; i++ {
		if err := svc.EnableUser(ctx, u.ID); err != nil {
			t.Fatalf("EnableUser #%d: %v", i+1, err)
		}
	}
	got, err := svc.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.Lifecycle != LifecycleActive || got.PendingExpiresAt != nil {
		t.Fatalf("after activate = %q expiry %v, want active and none", got.Lifecycle, got.PendingExpiresAt)
	}
	if !got.CanAuthenticate() {
		t.Fatal("activated user cannot authenticate")
	}
}

func TestPendingUser_CannotAuthenticateAndNeverReturnsToPending(t *testing.T) {
	u := &User{ID: 1, Lifecycle: LifecyclePending}
	if u.CanAuthenticate() {
		t.Fatal("pending user can authenticate")
	}
	for _, from := range knownLifecycles() {
		if from == LifecyclePending {
			continue
		}
		if from.CanTransitionTo(LifecyclePending) {
			t.Errorf("%s -> pending allowed", from)
		}
	}
}

func TestDeleteExpiredPending_RefusesAnythingButExpiredPending(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	expirer := svc.(PendingExpirer)
	fresh := createPending(t, svc, "erin", "acme", time.Hour)
	active := createTestUser(t, svc, "frank", "acme")
	now := time.Now().UTC().Add(2 * time.Hour)

	if err := svc.EnableUser(ctx, fresh.ID); err != nil {
		t.Fatalf("EnableUser: %v", err)
	}
	for _, id := range []uint64{fresh.ID, active.ID} {
		_, err := expirer.DeleteExpiredPending(ctx, id, now)
		var np *AccountNotPendingError
		if !errors.As(err, &np) {
			t.Fatalf("DeleteExpiredPending(%d) err = %v, want AccountNotPendingError", id, err)
		}
		if _, gerr := svc.GetUser(ctx, id); gerr != nil {
			t.Fatalf("refused delete removed user %d: %v", id, gerr)
		}
	}
}

func TestPendingReaper_ReapsOnlyExpiredPending(t *testing.T) {
	al := &fakeAuditLogger{}
	svc, err := NewStore(newMemKVStore(), newTestIDGen(t), WithAuditLogger(al))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	clock := &fakeClock{now: time.Now().UTC()}

	expired := createPending(t, svc, "gina", "acme", time.Minute)
	later := createPending(t, svc, "hank", "acme", 3*time.Hour)
	forever := createPending(t, svc, "ivan", "acme", 0)
	active := createTestUser(t, svc, "judy", "acme")
	activated := createPending(t, svc, "kate", "acme", time.Minute)
	other := createPending(t, svc, "liam", "globex", time.Minute)
	if err := svc.EnableUser(ctx, activated.ID); err != nil {
		t.Fatalf("EnableUser: %v", err)
	}

	factors := &fakeFactors{byUser: map[uint64][]*auth.AuthFactor{
		expired.ID: {{ID: "f-gina"}},
		active.ID:  {{ID: "f-judy"}},
	}}
	r, err := NewPendingReaper(PendingReaperConfig{Users: svc, Factors: factors, Now: clock.Now, PageSize: 2})
	if err != nil {
		t.Fatalf("NewPendingReaper: %v", err)
	}

	if n, err := r.Sweep(ctx, "acme"); err != nil || n != 0 {
		t.Fatalf("sweep before expiry = %d, %v; want 0, nil", n, err)
	}

	clock.Advance(time.Hour)
	n, err := r.Sweep(ctx, "acme")
	if err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v; want 1, nil", n, err)
	}
	var nf *UserNotFoundError
	if _, err := svc.GetUser(ctx, expired.ID); !errors.As(err, &nf) {
		t.Fatalf("expired pending user survived: %v", err)
	}
	for _, id := range []uint64{later.ID, forever.ID, active.ID, activated.ID, other.ID} {
		if _, err := svc.GetUser(ctx, id); err != nil {
			t.Errorf("user %d reaped: %v", id, err)
		}
	}
	if len(factors.deleted) != 1 || factors.deleted[0] != "f-gina" {
		t.Fatalf("deleted factors = %v, want [f-gina]", factors.deleted)
	}

	var reapAudits int
	for _, ev := range al.snapshot() {
		if ev.Action == AuditActionPendingReaped {
			reapAudits++
			if ev.Attributes["username"] != "gina" || ev.Attributes["pendingExpiresAt"] == nil {
				t.Errorf("reap audit attributes = %v", ev.Attributes)
			}
		}
	}
	if reapAudits != 1 {
		t.Fatalf("reap audits = %d, want 1", reapAudits)
	}

	// The other tenant's account is reaped only by a sweep of that tenant.
	if n, err := r.Sweep(ctx, "globex"); err != nil || n != 1 {
		t.Fatalf("globex sweep = %d, %v; want 1, nil", n, err)
	}
}

func TestPendingReaper_DoesNothingOffLeader(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	clock := &fakeClock{now: time.Now().UTC().Add(time.Hour)}
	u := createPending(t, svc, "mona", "acme", time.Minute)

	leader := false
	r, err := NewPendingReaper(PendingReaperConfig{Users: svc, Now: clock.Now, IsLeader: func(context.Context, string) bool { return leader }})
	if err != nil {
		t.Fatalf("NewPendingReaper: %v", err)
	}
	if n, err := r.Sweep(ctx, "acme"); err != nil || n != 0 {
		t.Fatalf("follower sweep = %d, %v; want 0, nil", n, err)
	}
	if _, err := svc.GetUser(ctx, u.ID); err != nil {
		t.Fatalf("follower reaped: %v", err)
	}
	leader = true
	if n, err := r.Sweep(ctx, "acme"); err != nil || n != 1 {
		t.Fatalf("leader sweep = %d, %v; want 1, nil", n, err)
	}
}

// claimState re-reads the account's claim: ClaimExpiredPending returns a
// claimed record unchanged, so it doubles as a read of the claim.
func claimState(t *testing.T, svc UserServicer, id uint64, now time.Time) *User {
	t.Helper()
	u, err := svc.(PendingExpirer).ClaimExpiredPending(context.Background(), id, now)
	if err != nil {
		t.Fatalf("re-read claim of %d: %v", id, err)
	}
	return u
}

// A purge that fails after the claim leaves the account claimed in the
// store -- unable to authenticate or be activated -- and the next sweep
// resumes it: purge, then delete.
func TestPendingReaper_FailedPurgeLeavesClaimNextSweepResumes(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	clock := &fakeClock{now: time.Now().UTC().Add(time.Hour)}
	u := createPending(t, svc, "orphan", "acme", time.Minute)
	factors := &fakeFactors{
		byUser:     map[uint64][]*auth.AuthFactor{u.ID: {{ID: "f-1", UserID: u.ID}, {ID: "f-2", UserID: u.ID}}},
		failDelete: errors.New("factor store unavailable"),
	}
	r, err := NewPendingReaper(PendingReaperConfig{Users: svc, Factors: factors, Now: clock.Now})
	if err != nil {
		t.Fatalf("NewPendingReaper: %v", err)
	}

	for i := 0; i < 2; i++ {
		if n, err := r.Sweep(ctx, "acme"); n != 0 || err == nil {
			t.Fatalf("sweep %d with failing purge = %d, %v; want 0 and the purge error", i, n, err)
		}
		claimed := claimState(t, svc, u.ID, clock.Now())
		if !ExpiryClaimed(claimed) || claimed.CanAuthenticate() {
			t.Fatalf("after failed purge: lifecycle %q claim %v; want a claimed, unauthenticable account", claimed.Lifecycle, claimed.ExpiryClaim)
		}
		if err := svc.EnableUser(ctx, u.ID); err == nil {
			t.Fatal("a claimed account was activated")
		}
	}

	factors.mu.Lock()
	factors.failDelete = nil
	factors.mu.Unlock()
	if n, err := r.Sweep(ctx, "acme"); n != 1 || err != nil {
		t.Fatalf("resume sweep = %d, %v; want 1, nil", n, err)
	}
	if left, _ := factors.List(ctx, u.ID); len(left) != 0 {
		t.Fatalf("factors left after resume: %d", len(left))
	}
	if _, err := svc.GetUser(ctx, u.ID); err == nil {
		t.Fatal("claimed account still present after the resume sweep")
	}
}

// failingDelete fails DeleteExpiredPending, the step after the claim and the
// purge, while fail is set.
type failingDelete struct {
	UserServicer
	PendingExpirer
	fail bool
}

func (f *failingDelete) DeleteExpiredPending(ctx context.Context, id uint64, now time.Time) (*User, error) {
	if f.fail {
		return nil, errors.New("store unavailable")
	}
	return f.PendingExpirer.DeleteExpiredPending(ctx, id, now)
}

// A reaper that stops after the claim and the purge (here the delete fails;
// a crash or a leadership change is the same) holds nothing in memory: a
// fresh reaper over the same store, as on a new leader, finds the claimed
// account and finishes it, with the expiry audit still naming its TTL.
func TestPendingReaper_FreshReaperFinishesClaimedAccount(t *testing.T) {
	al := &fakeAuditLogger{}
	svc, err := NewStore(newMemKVStore(), newTestIDGen(t), WithAuditLogger(al))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	clock := &fakeClock{now: time.Now().UTC().Add(time.Hour)}
	u := createPending(t, svc, "restart", "acme", time.Minute)
	factors := &fakeFactors{byUser: map[uint64][]*auth.AuthFactor{u.ID: {{ID: "f-r", UserID: u.ID}}}}

	broken := &failingDelete{UserServicer: svc, PendingExpirer: svc.(PendingExpirer), fail: true}
	first, err := NewPendingReaper(PendingReaperConfig{Users: broken, Factors: factors, Now: clock.Now})
	if err != nil {
		t.Fatalf("NewPendingReaper: %v", err)
	}
	if n, err := first.Sweep(ctx, "acme"); n != 0 || err == nil {
		t.Fatalf("sweep with failing delete = %d, %v; want 0 and the error", n, err)
	}
	if !ExpiryClaimed(claimState(t, svc, u.ID, clock.Now())) {
		t.Fatal("account not left claimed after the failed delete")
	}

	// The TTL is no longer on the record (claiming cleared it); the claim
	// alone makes the account a candidate.
	fresh, err := NewPendingReaper(PendingReaperConfig{Users: svc, Factors: factors, Now: clock.Now})
	if err != nil {
		t.Fatalf("NewPendingReaper: %v", err)
	}
	if n, err := fresh.Sweep(ctx, "acme"); n != 1 || err != nil {
		t.Fatalf("fresh reaper sweep = %d, %v; want 1, nil", n, err)
	}
	if _, err := svc.GetUser(ctx, u.ID); err == nil {
		t.Fatal("claimed account survived the fresh reaper")
	}
	var expired int
	for _, ev := range al.snapshot() {
		if ev.Action == AuditActionPendingReaped {
			expired++
		}
	}
	if expired != 1 {
		t.Fatalf("pending-expired audit events = %d, want 1", expired)
	}
}

// The account is claimed before its factors are purged: inside the purge
// (driven by the hook, no sleeps) the account is already terminal, so an
// activation racing the reaper is refused and cannot resurrect it.
func TestPendingReaper_ClaimPrecedesPurge(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	clock := &fakeClock{now: time.Now().UTC().Add(time.Hour)}
	createPending(t, svc, "racer", "acme", time.Minute)
	var during *User
	var enableErr error
	factors := &fakeFactors{onPurge: func(id uint64) {
		during = claimState(t, svc, id, clock.Now())
		enableErr = svc.EnableUser(ctx, id)
	}}
	r, err := NewPendingReaper(PendingReaperConfig{Users: svc, Factors: factors, Now: clock.Now})
	if err != nil {
		t.Fatalf("NewPendingReaper: %v", err)
	}
	if n, err := r.Sweep(ctx, "acme"); n != 1 || err != nil {
		t.Fatalf("sweep = %d, %v; want 1, nil", n, err)
	}
	if during == nil || !ExpiryClaimed(during) {
		t.Fatalf("account during the purge = %+v; want claimed", during)
	}
	if enableErr == nil {
		t.Fatal("activation between claim and purge succeeded")
	}
}

// An account activated before the claim is never claimed or purged.
func TestClaimExpiredPending_RefusesActivatedAndFresh(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	expirer := svc.(PendingExpirer)
	later := time.Now().UTC().Add(time.Hour)
	activated := createPending(t, svc, "act", "acme", time.Minute)
	if err := svc.EnableUser(ctx, activated.ID); err != nil {
		t.Fatalf("EnableUser: %v", err)
	}
	fresh := createPending(t, svc, "fresh", "acme", 2*time.Hour)
	for name, id := range map[string]uint64{"activated": activated.ID, "fresh": fresh.ID} {
		var notPending *AccountNotPendingError
		if _, err := expirer.ClaimExpiredPending(ctx, id, later); !errors.As(err, &notPending) {
			t.Fatalf("%s: claim err = %v, want AccountNotPendingError", name, err)
		}
	}
	if got, err := svc.GetUser(ctx, activated.ID); err != nil || got.Lifecycle != LifecycleActive {
		t.Fatalf("activated account after refused claim = %v, %v", got, err)
	}
}

func TestPendingReaper_RunSweepsTenantsOnEachTick(t *testing.T) {
	svc := newTestStore(t)
	clock := &fakeClock{now: time.Now().UTC().Add(time.Hour)}
	u := createPending(t, svc, "nora", "acme", time.Minute)
	r, err := NewPendingReaper(PendingReaperConfig{Users: svc, Now: clock.Now})
	if err != nil {
		t.Fatalf("NewPendingReaper: %v", err)
	}
	ticks := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(context.Background(), ticks, func(context.Context) ([]string, error) { return []string{"acme"}, nil })
	}()
	ticks <- clock.Now()
	close(ticks) // Run returns only after finishing the tick it received.
	<-done
	var nf *UserNotFoundError
	if _, err := svc.GetUser(context.Background(), u.ID); !errors.As(err, &nf) {
		t.Fatalf("Run did not reap: %v", err)
	}
}

func TestNewPendingReaper_RefusesServiceWithoutExpirer(t *testing.T) {
	if _, err := NewPendingReaper(PendingReaperConfig{}); err == nil {
		t.Fatal("nil users accepted")
	}
	type plain struct{ UserServicer }
	if _, err := NewPendingReaper(PendingReaperConfig{Users: plain{}}); !errors.Is(err, ErrPendingExpiryUnsupported) {
		t.Fatalf("err = %v, want ErrPendingExpiryUnsupported", err)
	}
}
