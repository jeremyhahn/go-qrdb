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

package rbac

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// ---------------------------------------------------------------------------
// Path 1: resolvePermissionSet — the fallback must not answer for a grant.
// ---------------------------------------------------------------------------

// TestResolvePermissionSet_NeverConsultsFallbackForAnotherTenant is the
// regression for go-qrdb strict-isolation phase 0 review finding B1 (path 1):
// resolvePermissionSet consulted the StaleRead role fallback for every
// tenant's role ids. qrdbd's fallback is confined to the system tenant's
// identity namespace, so for any other tenant it cannot answer about what a
// role grants at all. The primary's transient error must stand — modelled on
// TestFallbackUserStore_NeverAnswersForAnotherTenant
// (pkg/auth/middleware/user_fallback_scope_test.go).
func TestResolvePermissionSet_NeverConsultsFallbackForAnotherTenant(t *testing.T) {
	primary := newMockRoleService(nil)
	wantErr := errors.New("read index not ready")
	primary.err = wantErr
	fallback := newMockRoleService(map[string][]permission.Permission{
		"acme-reader-role": {"kv:get"},
	})

	identity := &auth.Identity{Subject: "reader", TenantID: "acme", Roles: []string{"acme-reader-role"}}

	err := New(primary, WithRoleServiceFallback(fallback)).
		Authorize(ctxFor("acme"), identity, "kv", "get")

	if fallback.callCount.Load() != 0 {
		t.Fatalf("fallback.ResolvePermissions was called %d times for tenant acme; the fallback is "+
			"confined to the system tenant and must not be consulted for any other", fallback.callCount.Load())
	}
	var re *ResolveError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v (%T), want *ResolveError: a fallback confined to the system tenant cannot "+
			"say that tenant acme's roles grant nothing", err, err)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("ResolveError should carry the primary's transient error, got %v", err)
	}
}

// TestResolvePermissionSet_StillConsultsFallbackForTheSystemTenant pins the
// half of the contract that must not move: TestAuthorize_RoleResolveFallbackOnTransientError
// (rbac_test.go) already proves the system tenant is still authorized through
// the fallback; this proves the fallback is actually the thing that answered,
// not an accident of the primary succeeding.
func TestResolvePermissionSet_StillConsultsFallbackForTheSystemTenant(t *testing.T) {
	primary := newMockRoleService(nil)
	primary.err = errors.New("read index not ready")
	fallback := newMockRoleService(map[string][]permission.Permission{
		"admin": {"admin:drain"},
	})

	identity := &auth.Identity{Subject: "admin", TenantID: tenantid.System, Roles: []string{"admin"}}

	err := New(primary, WithRoleServiceFallback(fallback)).
		Authorize(ctxFor(tenantid.System), identity, "admin", "drain")
	if err != nil {
		t.Fatalf("expected the fallback to authorize the system tenant, got %v", err)
	}
	if fallback.callCount.Load() != 1 {
		t.Fatalf("fallback.ResolvePermissions was called %d times for the system tenant, want 1", fallback.callCount.Load())
	}
}

// TestResolvePermissionSet_DefinitiveRoleNotFound_NeverConsultsFallback is the
// regression for review finding X1: a DEFINITIVE answer from the primary
// (linearizable) store -- the role was deleted, or never existed -- is not a
// transient condition the StaleRead fallback exists for. Before this fix,
// resolvePermissionSet retried EVERY primary error against the fallback when
// one was configured, including this one; for the SYSTEM tenant (the one the
// fallback actually holds) a replica that had not yet caught up to the
// deletion could still answer with the just-deleted role's permissions,
// resurrecting a revoked grant. This holds even for the system tenant --
// there is no tenant for which a RoleNotFoundError is worth retrying.
func TestResolvePermissionSet_DefinitiveRoleNotFound_NeverConsultsFallback(t *testing.T) {
	primary := newMockRoleService(nil)
	notFound := &role.RoleNotFoundError{ID: "deleted-role"}
	primary.err = notFound
	fallback := newMockRoleService(map[string][]permission.Permission{
		"deleted-role": {"admin:drain"}, // stale: the deletion has not replicated here yet
	})

	identity := &auth.Identity{Subject: "admin", TenantID: tenantid.System, Roles: []string{"deleted-role"}}

	err := New(primary, WithRoleServiceFallback(fallback)).
		Authorize(ctxFor(tenantid.System), identity, "admin", "drain")

	if fallback.callCount.Load() != 0 {
		t.Fatalf("fallback.ResolvePermissions was called %d times for a DEFINITIVE RoleNotFoundError; "+
			"it must never be consulted, even for the system tenant -- a stale replica could still "+
			"grant the just-deleted role's permissions", fallback.callCount.Load())
	}
	var re *ResolveError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v (%T), want *ResolveError", err, err)
	}
	if !errors.Is(err, notFound) {
		t.Fatalf("ResolveError should carry the primary's RoleNotFoundError, got %v", err)
	}
}

// TestResolvePermissionSet_DefinitiveValidationError_NeverConsultsFallback is
// X1's companion for a malformed request (an empty role id) rather than an
// absent role: also definitive, also never worth asking a replica about.
func TestResolvePermissionSet_DefinitiveValidationError_NeverConsultsFallback(t *testing.T) {
	primary := newMockRoleService(nil)
	validationErr := &role.ValidationError{Field: "id", Reason: "must not be empty"}
	primary.err = validationErr
	fallback := newMockRoleService(map[string][]permission.Permission{
		"bad-role": {"admin:drain"},
	})

	identity := &auth.Identity{Subject: "admin", TenantID: tenantid.System, Roles: []string{"bad-role"}}

	err := New(primary, WithRoleServiceFallback(fallback)).
		Authorize(ctxFor(tenantid.System), identity, "admin", "drain")

	if fallback.callCount.Load() != 0 {
		t.Fatalf("fallback.ResolvePermissions was called %d times for a DEFINITIVE ValidationError; "+
			"it must never be consulted", fallback.callCount.Load())
	}
	var re *ResolveError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v (%T), want *ResolveError", err, err)
	}
	if !errors.Is(err, validationErr) {
		t.Fatalf("ResolveError should carry the primary's ValidationError, got %v", err)
	}
}
