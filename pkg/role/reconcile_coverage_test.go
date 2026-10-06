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

package role

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// ---------------------------------------------------------------------------
// awaitMatchingRole
//
// reconcileRole/seedMissingRole only call this after a CreateRole collision,
// which these tests bypass by calling it directly -- the behavior under
// test is the poll loop's own two exit conditions (window elapsed, context
// cancelled) rather than how a collision is produced.
// ---------------------------------------------------------------------------

// TestAwaitMatchingRole_DeadlineExpiresWithoutAMatch covers the case
// awaitMatchingRole's own doc calls "a genuine, unexplained conflict": the
// wanted role never appears, so the poll loop must run out its window and
// report false rather than block forever.
func TestAwaitMatchingRole_DeadlineExpiresWithoutAMatch(t *testing.T) {
	rs := newTestStore(t).(*kvRoleStore)
	rs.reconcilePollInterval = time.Millisecond
	rs.reconcileWindow = 5 * time.Millisecond

	want := &Role{ID: "nonexistent-id", TenantID: "acme", Name: "ghost-role"}
	if rs.awaitMatchingRole(context.Background(), "acme", want) {
		t.Fatal("awaitMatchingRole = true for a role that was never created, want false once the window elapses")
	}
}

// TestAwaitMatchingRole_ContextCancellationStopsWaiting covers the other
// early exit: a caller whose context is done must not wait out the rest of
// the window.
func TestAwaitMatchingRole_ContextCancellationStopsWaiting(t *testing.T) {
	rs := newTestStore(t).(*kvRoleStore)
	// Long enough that only the context, never the window or a poll tick,
	// can be what stops this call inside the test's deadline.
	rs.reconcilePollInterval = time.Second
	rs.reconcileWindow = time.Second

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	want := &Role{ID: "nonexistent-id", TenantID: "acme", Name: "ghost-role"}
	if rs.awaitMatchingRole(ctx, "acme", want) {
		t.Fatal("awaitMatchingRole = true with an already-cancelled context, want false")
	}
}

// ---------------------------------------------------------------------------
// putRole / reconcileRole
// ---------------------------------------------------------------------------

// TestReconcileRole_PutRoleFailurePropagates covers putRole's "put primary"
// error branch: reconcileRole has already decided a write is needed (the
// stored and canonical permission sets differ) and the underlying store's
// final Put fails.
func TestReconcileRole_PutRoleFailurePropagates(t *testing.T) {
	ctx := context.Background()
	fkv := &faultKVStore{inner: newMemKVStore()}
	svc, err := NewStore(fkv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	rs := svc.(*kvRoleStore)

	stored, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:        "custom-role",
		TenantID:    "acme",
		Permissions: []permission.Permission{permission.KVGet},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	// reconcileRole itself does not enforce requireCanonicalSystemRole --
	// that guard lives in CreateRole, for names a caller may not reuse. A
	// plain, non-system "canonical" definition is enough to drive it
	// through the permission-diff/update path under test.
	canonical := &Role{ID: stored.ID, Name: "custom-role", TenantID: "acme",
		Permissions: []permission.Permission{permission.KVGet, permission.KVPut}}

	fkv.putFailAt = fkv.putCount + 1
	fkv.putErr = errors.New("disk full")

	_, err = rs.reconcileRole(ctx, "acme", canonical)
	if err == nil {
		t.Fatal("reconcileRole = nil error when the store's Put fails, want an error")
	}
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("reconcileRole error = %T (%v), want *StoreError", err, err)
	}
}

// ---------------------------------------------------------------------------
// OwnerRole / canonicalSystemRole / requireCanonicalSystemRole
// ---------------------------------------------------------------------------

// TestOwnerRole_EmptyTenantDefaultsToSystemTenant covers OwnerRole's only
// branch: an empty tenant id falls back to the system tenant, the same
// default every other role constructor in this package applies.
func TestOwnerRole_EmptyTenantDefaultsToSystemTenant(t *testing.T) {
	owner := OwnerRole("")
	want := OwnerRole(tenantid.System)
	if owner.TenantID != tenantid.System {
		t.Fatalf("TenantID = %q, want %q", owner.TenantID, tenantid.System)
	}
	if owner.ID != want.ID {
		t.Fatalf("ID = %q, want %q (same as OwnerRole(tenantid.System))", owner.ID, want.ID)
	}
}

// TestRequireCanonicalSystemRole_UnreachableNameRefused exercises
// canonicalSystemRole's nil-return branch directly: a name that denotes no
// built-in and no Owner role. Production never reaches this with the
// current systemRoleNames derivation (see the doc comment on
// requireCanonicalSystemRole), but the function's own contract -- refuse
// rather than fall through permissively -- is exactly what this proves.
func TestRequireCanonicalSystemRole_UnreachableNameRefused(t *testing.T) {
	err := requireCanonicalSystemRole("acme", &CreateRoleRequest{Name: "not-a-system-role-name"})
	var reserved *ReservedRoleNameError
	if !errors.As(err, &reserved) || reserved.Detail != "reserved name denotes no canonical role" {
		t.Fatalf("requireCanonicalSystemRole = %v, want *ReservedRoleNameError{Detail: %q}",
			err, "reserved name denotes no canonical role")
	}
}
