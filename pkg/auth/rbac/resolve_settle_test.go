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
	"sync/atomic"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// settlingRoleService answers ResolvePermissions with errs, one per call, and
// then with the embedded mock's grants.
type settlingRoleService struct {
	*mockRoleService
	errs  []error
	calls atomic.Int64
}

func (s *settlingRoleService) ResolvePermissions(ctx context.Context, roleIDs []string) ([]permission.Permission, error) {
	if n := s.calls.Add(1); n <= int64(len(s.errs)) {
		return nil, s.errs[n-1]
	}
	return s.mockRoleService.ResolvePermissions(ctx, roleIDs)
}

// roleElectionTimeout is the error kvRoleStore.GetRole returns, through
// ResolvePermissions, when its read times out while the system shard elects
// a leader.
func roleElectionTimeout() error {
	return &role.StoreError{Op: "get", Detail: "primary lookup", Err: &service.ReadError{Err: quicraft.ErrTimeout}}
}

func newSettlingRoleService(errs ...error) *settlingRoleService {
	return &settlingRoleService{
		mockRoleService: newMockRoleService(map[string][]permission.Permission{
			"reader": {"kv:get"},
		}),
		errs: errs,
	}
}

func TestResolvePermissionSet_WaitsOutElectionForANonSystemTenant(t *testing.T) {
	svc := newSettlingRoleService(roleElectionTimeout(), roleElectionTimeout(), roleElectionTimeout())
	identity := &auth.Identity{Subject: "reader-1", TenantID: "acme", Roles: []string{"reader"}}

	if err := New(svc).Authorize(ctxFor("acme"), identity, "kv", "get"); err != nil {
		t.Fatalf("Authorize err = %v, want the resolve to wait out the election and succeed", err)
	}
	if calls := svc.calls.Load(); calls != 4 {
		t.Fatalf("ResolvePermissions calls = %d, want 3 elections and the answer", calls)
	}
}

func TestResolvePermissionSet_SystemTenantWaitsWhenFallbackAlsoElecting(t *testing.T) {
	svc := newSettlingRoleService(roleElectionTimeout(), roleElectionTimeout())
	fallback := newSettlingRoleService(roleElectionTimeout(), roleElectionTimeout(), roleElectionTimeout())
	identity := &auth.Identity{Subject: "reader-1", TenantID: tenantid.System, Roles: []string{"reader"}}

	authz := New(svc, WithRoleServiceFallback(fallback))
	if err := authz.Authorize(ctxFor(tenantid.System), identity, "kv", "get"); err != nil {
		t.Fatalf("Authorize err = %v, want the resolve to wait out the election and succeed", err)
	}
	if calls := svc.calls.Load(); calls != 3 {
		t.Fatalf("primary calls = %d, want 2 elections and the answer", calls)
	}
	if calls := fallback.calls.Load(); calls != 2 {
		t.Fatalf("fallback calls = %d, want one per failed primary attempt", calls)
	}
}

func TestResolvePermissionSet_ElectionOutlastsBudget(t *testing.T) {
	errs := make([]error, 64)
	for i := range errs {
		errs[i] = roleElectionTimeout()
	}
	svc := newSettlingRoleService(errs...)
	identity := &auth.Identity{Subject: "reader-1", TenantID: "acme", Roles: []string{"reader"}}
	authz := New(svc)
	authz.settle = service.SettlePolicy{Budget: 150 * time.Millisecond}

	start := time.Now()
	err := authz.Authorize(ctxFor("acme"), identity, "kv", "get")
	var re *ResolveError
	if !errors.As(err, &re) || !errors.Is(err, quicraft.ErrTimeout) {
		t.Fatalf("Authorize err = %v (%T), want *ResolveError carrying the election's %v", err, err, quicraft.ErrTimeout)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Authorize took %v, want it bounded by its 150ms budget", elapsed)
	}
	if calls := svc.calls.Load(); calls < 2 || calls >= int64(len(errs)) {
		t.Fatalf("ResolvePermissions calls = %d, want a retry and the budget to stop it before %d", calls, len(errs))
	}
}

func TestResolvePermissionSet_ContextEndsTheWait(t *testing.T) {
	errs := make([]error, 64)
	for i := range errs {
		errs[i] = roleElectionTimeout()
	}
	svc := newSettlingRoleService(errs...)
	identity := &auth.Identity{Subject: "reader-1", TenantID: "acme", Roles: []string{"reader"}}

	ctx, cancel := context.WithTimeout(ctxFor("acme"), 2*service.DefaultSettlePolicy.Poll+service.DefaultSettlePolicy.Poll/2)
	defer cancel()
	err := New(svc).Authorize(ctx, identity, "kv", "get")
	var re *ResolveError
	if !errors.As(err, &re) || !errors.Is(err, quicraft.ErrTimeout) {
		t.Fatalf("Authorize err = %v (%T), want *ResolveError carrying the election's %v", err, err, quicraft.ErrTimeout)
	}
	if calls := svc.calls.Load(); calls < 2 || calls >= int64(len(errs)) {
		t.Fatalf("ResolvePermissions calls = %d, want the request's context to end the wait", calls)
	}
}

func TestResolvePermissionSet_RoleNotFoundIsFinal(t *testing.T) {
	svc := newSettlingRoleService(&role.RoleNotFoundError{ID: "reader"})
	identity := &auth.Identity{Subject: "reader-1", TenantID: "acme", Roles: []string{"reader"}}

	err := New(svc).Authorize(ctxFor("acme"), identity, "kv", "get")
	var notFound *role.RoleNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("Authorize err = %v (%T), want the definitive *role.RoleNotFoundError", err, err)
	}
	if calls := svc.calls.Load(); calls != 1 {
		t.Fatalf("ResolvePermissions calls = %d, want a definitive answer never retried", calls)
	}
}
