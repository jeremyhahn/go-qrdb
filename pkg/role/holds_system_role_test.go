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

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// seededStore returns a real role store with the built-in roles reconciled
// into tenantID.
func seededStore(t *testing.T, tenantID string) RoleServicer {
	t.Helper()
	svc, err := NewStore(newMemKVStore())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := svc.ReconcileBuiltInRoles(tenant.WithTenantID(context.Background(), tenantID), tenantID); err != nil {
		t.Fatalf("ReconcileBuiltInRoles: %v", err)
	}
	return svc
}

func TestHoldsSystemRole_PersistedIdentifierHolds(t *testing.T) {
	svc := seededStore(t, "acme")
	so, err := svc.GetRoleByName(tenant.WithTenantID(context.Background(), "acme"), "acme", RoleNameSO)
	if err != nil {
		t.Fatalf("GetRoleByName: %v", err)
	}
	got, err := HoldsSystemRole(context.Background(), svc, "acme", []string{"other", so.ID}, RoleNameSO)
	if err != nil || !got {
		t.Fatalf("HoldsSystemRole = %v, %v; want true for the persisted SO identifier", got, err)
	}
}

// TestHoldsSystemRole_NameNeverHolds is go-qrdb#569: identity.Roles holds
// identifiers, so a role NAME there must not be read as the role.
func TestHoldsSystemRole_NameNeverHolds(t *testing.T) {
	svc := seededStore(t, "acme")
	got, err := HoldsSystemRole(context.Background(), svc, "acme", []string{RoleNameSO}, RoleNameSO)
	if err != nil || got {
		t.Fatalf("HoldsSystemRole(name) = %v, %v; want false", got, err)
	}
}

func TestHoldsSystemRole_OtherRoleDoesNotHold(t *testing.T) {
	svc := seededStore(t, "acme")
	admin, err := svc.GetRoleByName(tenant.WithTenantID(context.Background(), "acme"), "acme", RoleNameAdmin)
	if err != nil {
		t.Fatalf("GetRoleByName: %v", err)
	}
	got, err := HoldsSystemRole(context.Background(), svc, "acme", []string{admin.ID}, RoleNameSO)
	if err != nil || got {
		t.Fatalf("HoldsSystemRole(admin) = %v, %v; want false", got, err)
	}
}

// TestHoldsSystemRole_OtherTenantsRoleDoesNotHold: the SO role of another
// tenant is not this tenant's SO.
func TestHoldsSystemRole_OtherTenantsRoleDoesNotHold(t *testing.T) {
	svc := seededStore(t, "acme")
	if _, err := svc.ReconcileBuiltInRoles(tenant.WithTenantID(context.Background(), "globex"), "globex"); err != nil {
		t.Fatalf("ReconcileBuiltInRoles(globex): %v", err)
	}
	globexSO, err := svc.GetRoleByName(tenant.WithTenantID(context.Background(), "globex"), "globex", RoleNameSO)
	if err != nil {
		t.Fatalf("GetRoleByName: %v", err)
	}
	got, err := HoldsSystemRole(context.Background(), svc, "acme", []string{globexSO.ID}, RoleNameSO)
	if err != nil || got {
		t.Fatalf("HoldsSystemRole(globex SO in acme) = %v, %v; want false", got, err)
	}
}

func TestHoldsSystemRole_UnseededTenantDoesNotHold(t *testing.T) {
	svc, err := NewStore(newMemKVStore())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	got, err := HoldsSystemRole(context.Background(), svc, "", []string{SystemRoleID(tenantid.System, RoleNameSO)}, RoleNameSO)
	if err != nil || got {
		t.Fatalf("HoldsSystemRole(unseeded) = %v, %v; want false", got, err)
	}
}

func TestHoldsSystemRole_NoRolesDoesNotHold(t *testing.T) {
	got, err := HoldsSystemRole(context.Background(), errNameResolver{err: errors.New("must not be called")}, "acme", nil, RoleNameSO)
	if err != nil || got {
		t.Fatalf("HoldsSystemRole(no roles) = %v, %v; want false without a lookup", got, err)
	}
}

func TestHoldsSystemRole_NilResolverIsRefused(t *testing.T) {
	got, err := HoldsSystemRole(context.Background(), nil, "acme", []string{"x"}, RoleNameSO)
	var want *ResolverRequiredError
	if got || !errors.As(err, &want) || want.Name != RoleNameSO {
		t.Fatalf("HoldsSystemRole(nil svc) = %v, %v; want *ResolverRequiredError", got, err)
	}
	if want.Error() == "" {
		t.Fatal("empty error message")
	}
}

func TestHoldsSystemRole_StoreErrorPropagates(t *testing.T) {
	sentinel := errors.New("store unavailable")
	got, err := HoldsSystemRole(context.Background(), errNameResolver{err: sentinel}, "acme", []string{"x"}, RoleNameSO)
	if got || !errors.Is(err, sentinel) {
		t.Fatalf("HoldsSystemRole = %v, %v; want the store error", got, err)
	}
}

func TestHoldsSystemRole_NilRoleWithNilErrorIsRefused(t *testing.T) {
	got, err := HoldsSystemRole(context.Background(), nilNameResolver{}, "acme", []string{"x"}, RoleNameSO)
	var want *InvalidResolverResultError
	if got || !errors.As(err, &want) {
		t.Fatalf("HoldsSystemRole = %v, %v; want *InvalidResolverResultError", got, err)
	}
}
