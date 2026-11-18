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
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// The audit action the docs promise, as a literal.
func TestAuditActionPendingReaped_IsTheDocumentedAction(t *testing.T) {
	if AuditActionPendingReaped != "pending_reaped" {
		t.Fatalf("AuditActionPendingReaped = %q, want %q", AuditActionPendingReaped, "pending_reaped")
	}
}

// crashKV stops the process -- a panic the test recovers, so no deferred
// compensation sees an error to undo -- right after a Delete of a key with
// the crashAfter prefix has landed.
type crashKV struct {
	*memKVStore
	crashAfter string
}

type crashed struct{}

func (s *crashKV) Delete(ctx context.Context, key string) error {
	if err := s.memKVStore.Delete(ctx, key); err != nil {
		return err
	}
	if s.crashAfter != "" && strings.HasPrefix(key, s.crashAfter) {
		s.crashAfter = ""
		panic(crashed{})
	}
	return nil
}

// sweepUntilCrash runs one sweep and reports whether it was stopped by the
// injected crash.
func sweepUntilCrash(t *testing.T, r *PendingReaper, tenantID string) (stopped bool) {
	t.Helper()
	defer func() {
		if v := recover(); v != nil {
			if _, ok := v.(crashed); !ok {
				panic(v)
			}
			stopped = true
		}
	}()
	_, _ = r.Sweep(context.Background(), tenantID)
	return false
}

func countAudits(al *fakeAuditLogger, action string) int {
	n := 0
	for _, ev := range al.snapshot() {
		if ev.Action == action {
			n++
		}
	}
	return n
}

// A reap that stops right after the delete removed the tenant index leaves
// an account ListUsers no longer returns. The reap marker the claim wrote
// still names it, so the next sweep finishes the delete and audits it.
func TestPendingReaper_ResumesDeleteInterruptedAfterTenantIndex(t *testing.T) {
	kv := &crashKV{memKVStore: newMemKVStore()}
	al := &fakeAuditLogger{}
	svc, err := NewStore(kv, newTestIDGen(t), WithAuditLogger(al))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	clock := &fakeClock{now: time.Now().UTC().Add(time.Hour)}
	u := createPending(t, svc, "halted", "acme", time.Minute)
	factors := &fakeFactors{byUser: map[uint64][]*auth.AuthFactor{u.ID: {{ID: "f-h", UserID: u.ID}}}}
	r, err := NewPendingReaper(PendingReaperConfig{Users: svc, Factors: factors, Now: clock.Now})
	if err != nil {
		t.Fatalf("NewPendingReaper: %v", err)
	}

	kv.crashAfter = keyPrefixTenant
	if !sweepUntilCrash(t, r, "acme") {
		t.Fatal("the sweep never reached the tenant index delete")
	}
	resp, err := svc.ListUsers(ctx, &ListUsersRequest{TenantID: "acme"})
	if err != nil || len(resp.Users) != 0 {
		t.Fatalf("ListUsers after the crash = %v, %v; want the account unlisted", resp, err)
	}
	if _, err := svc.GetUser(ctx, u.ID); err != nil {
		t.Fatalf("primary record gone before the crash point: %v", err)
	}
	if ids, err := svc.(PendingExpirer).ListExpiryClaims(ctx, "acme"); err != nil || len(ids) != 1 || ids[0] != u.ID {
		t.Fatalf("ListExpiryClaims = %v, %v; want [%d]", ids, err, u.ID)
	}

	n, err := r.Sweep(ctx, "acme")
	if err != nil || n != 1 {
		t.Fatalf("resuming sweep = %d, %v; want 1, nil", n, err)
	}
	var nf *UserNotFoundError
	if _, err := svc.GetUser(ctx, u.ID); !errors.As(err, &nf) {
		t.Fatalf("account survived the resuming sweep: %v", err)
	}
	if got := countAudits(al, "pending_reaped"); got != 1 {
		t.Fatalf("pending_reaped audits = %d, want 1", got)
	}
	if ids, err := svc.(PendingExpirer).ListExpiryClaims(ctx, "acme"); err != nil || len(ids) != 0 {
		t.Fatalf("reap marker left after the delete finished: %v, %v", ids, err)
	}
	if n, err := r.Sweep(ctx, "acme"); err != nil || n != 0 {
		t.Fatalf("sweep after the finish = %d, %v; want 0, nil", n, err)
	}
}

// A reap that stops right after the primary record is deleted leaves only
// the identity claim and the marker; the next sweep releases the claim,
// audits the reap and drops the marker.
func TestPendingReaper_ResumesDeleteInterruptedAfterPrimary(t *testing.T) {
	kv := &crashOnPrimaryKV{memKVStore: newMemKVStore()}
	idx, err := tenant.NewIdentityIndex(kv)
	if err != nil {
		t.Fatalf("NewIdentityIndex: %v", err)
	}
	al := &fakeAuditLogger{}
	svc, err := NewStore(kv, newTestIDGen(t), WithIdentityIndex(idx), WithAuditLogger(al))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	clock := &fakeClock{now: time.Now().UTC().Add(time.Hour)}
	u := createPending(t, svc, "halfway", "acme", time.Minute)
	r, err := NewPendingReaper(PendingReaperConfig{Users: svc, Now: clock.Now})
	if err != nil {
		t.Fatalf("NewPendingReaper: %v", err)
	}

	kv.primary = primaryKey(u.ID)
	if !sweepUntilCrash(t, r, "acme") {
		t.Fatal("the sweep never reached the primary delete")
	}
	if _, err := svc.GetUser(ctx, u.ID); err == nil {
		t.Fatal("primary record survived the crash point")
	}
	if _, held, err := idx.Lookup(ctx, tenant.IdentityUser, identityID(u.ID)); err != nil || !held {
		t.Fatalf("identity claim after the crash = held %v, %v; want still held", held, err)
	}
	if got := countAudits(al, "pending_reaped"); got != 0 {
		t.Fatalf("pending_reaped audited before the reap finished: %d", got)
	}

	n, err := r.Sweep(ctx, "acme")
	if err != nil || n != 1 {
		t.Fatalf("resuming sweep = %d, %v; want 1, nil", n, err)
	}
	if _, held, err := idx.Lookup(ctx, tenant.IdentityUser, identityID(u.ID)); err != nil || held {
		t.Fatalf("identity claim after the finish = held %v, %v; want released", held, err)
	}
	if got := countAudits(al, "pending_reaped"); got != 1 {
		t.Fatalf("pending_reaped audits = %d, want 1", got)
	}
	if ids, err := svc.(PendingExpirer).ListExpiryClaims(ctx, "acme"); err != nil || len(ids) != 0 {
		t.Fatalf("reap marker left after the delete finished: %v, %v", ids, err)
	}
}

// crashOnPrimaryKV crashes right after the conditional delete of primary
// lands, before anything after it -- the identity release -- runs.
type crashOnPrimaryKV struct {
	*memKVStore
	primary string
}

func (s *crashOnPrimaryKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	ok, err := s.memKVStore.CompareAndDelete(ctx, key, expected)
	if err == nil && ok && s.primary != "" && key == s.primary {
		s.primary = ""
		panic(crashed{})
	}
	return ok, err
}

// forgetFailIndex fails every identity release while fail is set.
type forgetFailIndex struct {
	tenant.IdentityIndex
	fail bool
}

func (f *forgetFailIndex) Forget(ctx context.Context, kind tenant.IdentityKind, id, token string) error {
	if f.fail {
		return errors.New("identity release failed")
	}
	return f.IdentityIndex.Forget(ctx, kind, id, token)
}

// A failed identity release after the primary delete is an error the sweep
// reports, and the marker keeps the account for the next sweep, which
// releases the claim and audits the reap once.
func TestPendingReaper_ResumesFailedIdentityRelease(t *testing.T) {
	kv := newMemKVStore()
	base, err := tenant.NewIdentityIndex(kv)
	if err != nil {
		t.Fatalf("NewIdentityIndex: %v", err)
	}
	idx := &forgetFailIndex{IdentityIndex: base}
	al := &fakeAuditLogger{}
	svc, err := NewStore(kv, newTestIDGen(t), WithIdentityIndex(idx), WithAuditLogger(al))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	clock := &fakeClock{now: time.Now().UTC().Add(time.Hour)}
	u := createPending(t, svc, "unreleased", "acme", time.Minute)
	r, err := NewPendingReaper(PendingReaperConfig{Users: svc, Now: clock.Now})
	if err != nil {
		t.Fatalf("NewPendingReaper: %v", err)
	}

	idx.fail = true
	if n, err := r.Sweep(ctx, "acme"); err == nil || n != 0 {
		t.Fatalf("sweep with a failing release = %d, %v; want 0 and the error", n, err)
	}
	if _, err := svc.GetUser(ctx, u.ID); err == nil {
		t.Fatal("primary record survived the delete")
	}
	if ids, err := svc.(PendingExpirer).ListExpiryClaims(ctx, "acme"); err != nil || len(ids) != 1 {
		t.Fatalf("ListExpiryClaims after the failed release = %v, %v; want the account", ids, err)
	}
	if got := countAudits(al, "pending_reaped"); got != 0 {
		t.Fatalf("pending_reaped audited before the release: %d", got)
	}

	idx.fail = false
	if n, err := r.Sweep(ctx, "acme"); err != nil || n != 1 {
		t.Fatalf("resuming sweep = %d, %v; want 1, nil", n, err)
	}
	if _, held, err := base.Lookup(ctx, tenant.IdentityUser, identityID(u.ID)); err != nil || held {
		t.Fatalf("identity claim after the finish = held %v, %v; want released", held, err)
	}
	if got := countAudits(al, "pending_reaped"); got != 1 {
		t.Fatalf("pending_reaped audits = %d, want 1", got)
	}
	if ids, err := svc.(PendingExpirer).ListExpiryClaims(ctx, "acme"); err != nil || len(ids) != 0 {
		t.Fatalf("reap marker left after the delete finished: %v, %v", ids, err)
	}
}

// An earlier sweep judges the account not yet expired and pauses before it
// drops the reap marker. A later sweep claims the account, deletes the
// primary and fails to release the identity claim, so its marker is the only
// recovery record. The earlier sweep must leave that marker in place, and the
// next sweep must finish the release from it.
func TestClaimExpiredPending_EarlierSweepKeepsMarkerOfUnreleasedDelete(t *testing.T) {
	ctx := context.Background()
	mem := newMemKVStore()
	base, err := tenant.NewIdentityIndex(mem)
	if err != nil {
		t.Fatalf("NewIdentityIndex: %v", err)
	}
	idx := &forgetFailIndex{IdentityIndex: base}
	kv := &lcfaultKV{memKVStore: mem, prefix: keyPrefixReap}
	svc, err := NewStore(kv, newTestIDGen(t), WithIdentityIndex(idx))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	exp := svc.(PendingExpirer)
	u := createPending(t, svc, "paused", "acme", time.Hour)
	early := time.Now().UTC()
	late := early.Add(2 * time.Hour)

	var lateErr, deleteErr error
	kv.beforeGet = func() {
		if _, lateErr = exp.ClaimExpiredPending(ctx, u.ID, late); lateErr != nil {
			return
		}
		idx.fail = true
		_, deleteErr = exp.DeleteExpiredPending(ctx, u.ID, late)
		idx.fail = false
	}

	var notPending *AccountNotPendingError
	if _, err := exp.ClaimExpiredPending(ctx, u.ID, early); !errors.As(err, &notPending) {
		t.Fatalf("early sweep = %v, want AccountNotPendingError", err)
	}
	if lateErr != nil || deleteErr == nil {
		t.Fatalf("late sweep claim = %v, delete = %v; want a claim and a failed release", lateErr, deleteErr)
	}
	if _, err := svc.GetUser(ctx, u.ID); err == nil {
		t.Fatal("primary record survived the late sweep's delete")
	}
	if ids, err := exp.ListExpiryClaims(ctx, "acme"); err != nil || len(ids) != 1 || ids[0] != u.ID {
		t.Fatalf("ListExpiryClaims after the overlap = %v, %v; want [%d]", ids, err, u.ID)
	}

	if _, err := exp.DeleteExpiredPending(ctx, u.ID, late); err != nil {
		t.Fatalf("next sweep's reap: %v", err)
	}
	if _, held, err := base.Lookup(ctx, tenant.IdentityUser, identityID(u.ID)); err != nil || held {
		t.Fatalf("identity claim after the next sweep = held %v, %v; want released", held, err)
	}
	if ids, err := exp.ListExpiryClaims(ctx, "acme"); err != nil || len(ids) != 0 {
		t.Fatalf("reap marker left after the release: %v, %v", ids, err)
	}
}

// A marker an overlapping claim writes between the earlier sweep's marker
// read and its delete survives: the stale-marker delete compares against the
// bytes the earlier sweep read.
func TestClaimExpiredPending_StaleDropComparesTheMarkerRead(t *testing.T) {
	ctx := context.Background()
	kv, exp, svc := lcnewFaultStore(t)
	u := createPending(t, svc, "window", "acme", time.Hour)
	early := time.Now().UTC()
	late := early.Add(2 * time.Hour)

	kv.prefix = keyPrefixReap
	var lateErr error
	kv.afterGet = func() { _, lateErr = exp.ClaimExpiredPending(ctx, u.ID, late) }

	var notPending *AccountNotPendingError
	if _, err := exp.ClaimExpiredPending(ctx, u.ID, early); !errors.As(err, &notPending) {
		t.Fatalf("early sweep = %v, want AccountNotPendingError", err)
	}
	if lateErr != nil {
		t.Fatalf("late sweep: %v", lateErr)
	}
	if ids, err := exp.ListExpiryClaims(ctx, "acme"); err != nil || len(ids) != 1 || ids[0] != u.ID {
		t.Fatalf("ListExpiryClaims after the overlap = %v, %v; want [%d]", ids, err, u.ID)
	}
}

// A marker an interrupted claim left on an account that was then activated
// is dropped by the next claim, which refuses the account.
func TestClaimExpiredPending_DropsStaleMarkerOfActivatedAccount(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Add(time.Hour)
	u := createPending(t, svc, "revived", "acme", time.Minute)
	s := svc.(*kvUserStore)
	if _, err := s.putReapMarker(ctx, u); err != nil {
		t.Fatalf("putReapMarker: %v", err)
	}
	if err := svc.EnableUser(ctx, u.ID); err != nil {
		t.Fatalf("EnableUser: %v", err)
	}
	var notPending *AccountNotPendingError
	if _, err := s.ClaimExpiredPending(ctx, u.ID, now); !errors.As(err, &notPending) {
		t.Fatalf("claim of an activated account = %v, want AccountNotPendingError", err)
	}
	if ids, err := s.ListExpiryClaims(ctx, "acme"); err != nil || len(ids) != 0 {
		t.Fatalf("stale marker kept: %v, %v", ids, err)
	}
}
