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
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// hangingRoleService never answers on its own: ResolvePermissions blocks
// until the context passed to it is done, then reports the context's own
// error. It is the faithful double for a stalled ReadIndex pipeline -- the
// condition resolveSyncTimeout exists to bound -- rather than one that
// returns an error immediately, which would never exercise the timeout at
// all. The embedded nil role.RoleServicer panics if any other method is
// called, which no path under test should reach.
type hangingRoleService struct {
	role.RoleServicer
}

func (hangingRoleService) ResolvePermissions(ctx context.Context, _ []string) ([]permission.Permission, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestResolvePermissionSet_BoundsThePrimaryResolveForANonSystemTenant is the
// regression for go-qrdb strict-isolation phase 0 review finding BL2, N4:
// resolvePermissionSet used to bound the primary (linearizable) resolve with
// resolveSyncTimeout ONLY when a StaleRead fallback was available for the
// tenant (useFallback), so a non-system tenant -- which never has a fallback
// to consult -- ran the primary resolve against the caller's own context with
// NO added bound. A request that supplied no deadline at all then hung
// forever instead of failing over to a prompt, retryable
// *rbac.ResolveError. The context passed to Authorize below carries no
// deadline of its own, so the test can only pass if resolvePermissionSet
// applies its OWN bound.
func TestResolvePermissionSet_BoundsThePrimaryResolveForANonSystemTenant(t *testing.T) {
	identity := &auth.Identity{Subject: "reader", TenantID: "acme", Roles: []string{"acme-reader-role"}}

	// No fallback configured at all: useFallback is false for every tenant,
	// which is exactly the configuration the stale code left unbounded.
	err := New(hangingRoleService{}).
		Authorize(ctxFor("acme"), identity, "kv", "get")

	if err == nil {
		t.Fatal("expected a retryable error from a primary resolve that never answers; got nil, " +
			"meaning the call either hung (this test would then never finish) or wrongly succeeded")
	}
	var re *ResolveError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v (%T), want *ResolveError wrapping context.DeadlineExceeded", err, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ResolveError should carry context.DeadlineExceeded from resolveSyncTimeout firing, got %v", err)
	}
}

// TestResolvePermissionSet_SystemTenantStillBoundedWithoutFallback pins the
// companion case: the system tenant gets the same unconditional bound even
// when no fallback is configured for it either (useFallback requires
// a.rolesFallback != nil, which is nil here) -- the budget is no longer tied
// to fallback availability at all.
func TestResolvePermissionSet_SystemTenantStillBoundedWithoutFallback(t *testing.T) {
	identity := &auth.Identity{Subject: "admin", TenantID: tenant.SystemTenantID, Roles: []string{"admin"}}

	err := New(hangingRoleService{}).
		Authorize(ctxFor(tenant.SystemTenantID), identity, "admin", "drain")

	if err == nil {
		t.Fatal("expected a retryable error from a primary resolve that never answers")
	}
	var re *ResolveError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v (%T), want *ResolveError wrapping context.DeadlineExceeded", err, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ResolveError should carry context.DeadlineExceeded from resolveSyncTimeout firing, got %v", err)
	}
}
