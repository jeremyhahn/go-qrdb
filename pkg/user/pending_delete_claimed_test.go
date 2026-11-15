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
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// A sweep claims an expired account and its factor purge fails, leaving the
// account deleting. An ordinary DeleteUser then removes the record. It purges
// nothing, so it must leave the reap marker: the next sweep purges the
// factors, finishes the identity and audits pending_reaped exactly once.
func TestDeleteUser_ClaimedAccountKeepsMarkerForTheReaper(t *testing.T) {
	kv := newMemKVStore()
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
	u := createPending(t, svc, "stranded", "acme", time.Minute)
	factors := &fakeFactors{
		byUser:     map[uint64][]*auth.AuthFactor{u.ID: {{ID: "f-s", UserID: u.ID}}},
		failDelete: errors.New("factor store down"),
	}
	r, err := NewPendingReaper(PendingReaperConfig{Users: svc, Factors: factors, Now: clock.Now})
	if err != nil {
		t.Fatalf("NewPendingReaper: %v", err)
	}

	if _, err := r.Sweep(ctx, "acme"); err == nil {
		t.Fatal("sweep with a failing purge reported success")
	}
	got, err := svc.GetUser(ctx, u.ID)
	if err != nil || !ExpiryClaimed(got) {
		t.Fatalf("after the failed purge = %+v, %v; want a claimed, deleting account", got, err)
	}

	if err := svc.DeleteUser(ctx, u.ID); err != nil {
		t.Fatalf("DeleteUser of the claimed account: %v", err)
	}
	if ids, err := svc.(PendingExpirer).ListExpiryClaims(ctx, "acme"); err != nil || len(ids) != 1 || ids[0] != u.ID {
		t.Fatalf("reap marker after DeleteUser = %v, %v; want [%d]", ids, err, u.ID)
	}
	if n := countAudits(al, AuditActionPendingReaped); n != 0 {
		t.Fatalf("pending_reaped audited before the factors were purged: %d", n)
	}

	factors.mu.Lock()
	factors.failDelete = nil
	factors.mu.Unlock()
	if n, err := r.Sweep(ctx, "acme"); err != nil || n != 1 {
		t.Fatalf("finishing sweep = %d, %v; want 1, nil", n, err)
	}
	factors.mu.Lock()
	left := len(factors.byUser[u.ID])
	factors.mu.Unlock()
	if left != 0 {
		t.Fatalf("factors left after the finishing sweep: %d", left)
	}
	if _, held, err := idx.Lookup(ctx, tenant.IdentityUser, identityID(u.ID)); err != nil || held {
		t.Fatalf("identity claim after the finish = held %v, %v; want released", held, err)
	}
	if ids, err := svc.(PendingExpirer).ListExpiryClaims(ctx, "acme"); err != nil || len(ids) != 0 {
		t.Fatalf("reap marker after the finishing sweep = %v, %v; want none", ids, err)
	}

	// A further sweep finds nothing and audits nothing again.
	if n, err := r.Sweep(ctx, "acme"); err != nil || n != 0 {
		t.Fatalf("idle sweep = %d, %v; want 0, nil", n, err)
	}
	if n := countAudits(al, AuditActionPendingReaped); n != 1 {
		t.Fatalf("pending_reaped audits = %d, want 1", n)
	}
}
