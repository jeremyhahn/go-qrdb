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
)

// TestValidateAssignable_SameTenant asserts the common case: a role created
// in tenantID is assignable within tenantID.
func TestValidateAssignable_SameTenant(t *testing.T) {
	svc := newTestStore(t)
	r := createTestRole(t, svc, "acme-admin", "acme")

	if err := ValidateAssignable(context.Background(), svc, r.ID, "acme"); err != nil {
		t.Fatalf("ValidateAssignable(same tenant) = %v, want nil", err)
	}
}

// TestValidateAssignable_WrongTenant asserts that a role created in one
// tenant is refused for a DIFFERENT, non-system tenant with the typed
// NotAssignableError.
func TestValidateAssignable_WrongTenant(t *testing.T) {
	svc := newTestStore(t)
	r := createTestRole(t, svc, "acme-admin", "acme")

	err := ValidateAssignable(context.Background(), svc, r.ID, "globex")
	var na *NotAssignableError
	if !errors.As(err, &na) {
		t.Fatalf("ValidateAssignable(wrong tenant) = %v, want *NotAssignableError", err)
	}
	if na.RoleID != r.ID || na.TenantID != "globex" {
		t.Errorf("NotAssignableError = %+v, want RoleID=%s TenantID=globex", na, r.ID)
	}
}

// TestValidateAssignable_SystemRoleFromSystemTenant asserts the system
// tenant's own copy of a built-in role is NOT assignable in another tenant:
// that tenant seeds its own copy, and the store cannot resolve the system
// tenant's id for one of its principals.
func TestValidateAssignable_SystemRoleFromSystemTenant(t *testing.T) {
	svc := newTestStore(t)
	seeded := seedBuiltIns(t, svc, tenant.SystemTenantID)
	if len(seeded) == 0 {
		t.Fatal("seedBuiltIns returned no roles")
	}
	sysRole := seeded[0]
	if !sysRole.System {
		t.Fatalf("seeded role %+v is not marked System", sysRole)
	}

	var na *NotAssignableError
	if err := ValidateAssignable(context.Background(), svc, sysRole.ID, "acme"); !errors.As(err, &na) {
		t.Fatalf("ValidateAssignable(system role, foreign tenant) = %v, want *NotAssignableError", err)
	}
	if err := ValidateAssignable(context.Background(), svc, sysRole.ID, tenant.SystemTenantID); err != nil {
		t.Fatalf("ValidateAssignable(system role, system tenant) = %v, want nil", err)
	}
}

// TestValidateAssignable_TenantOwnSystemRoleNotGloballyValid asserts a
// TENANT's own copy of a built-in role is assignable in that tenant only.
func TestValidateAssignable_TenantOwnSystemRoleNotGloballyValid(t *testing.T) {
	svc := newTestStore(t)
	seeded := seedBuiltIns(t, svc, "acme")
	if len(seeded) == 0 {
		t.Fatal("seedBuiltIns returned no roles")
	}
	acmeSysRole := seeded[0]
	if !acmeSysRole.System {
		t.Fatalf("seeded role %+v is not marked System", acmeSysRole)
	}

	err := ValidateAssignable(context.Background(), svc, acmeSysRole.ID, "globex")
	var na *NotAssignableError
	if !errors.As(err, &na) {
		t.Fatalf("ValidateAssignable(acme's own system role, globex) = %v, want *NotAssignableError", err)
	}
}

// TestValidateAssignable_RoleNotFound asserts that a non-existent role
// propagates GetRole's own typed not-found error verbatim, rather than being
// reported as merely "not assignable".
func TestValidateAssignable_RoleNotFound(t *testing.T) {
	svc := newTestStore(t)

	err := ValidateAssignable(context.Background(), svc, "does-not-exist", "acme")
	var rnf *RoleNotFoundError
	if !errors.As(err, &rnf) {
		t.Fatalf("ValidateAssignable(missing role) = %v, want *RoleNotFoundError", err)
	}
}

// TestNotAssignableError_Message pins the error string.
func TestNotAssignableError_Message(t *testing.T) {
	err := &NotAssignableError{RoleID: "r1", TenantID: "acme"}
	want := "role: r1 is not assignable within tenant acme"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// nilRoleServicer breaks the RoleServicer contract by answering GetRole with
// neither a role nor an error.
type nilRoleServicer struct {
	RoleServicer
}

func (nilRoleServicer) GetRole(context.Context, string) (*Role, error) { return nil, nil }

// TestValidateAssignable_NilRoleIsNotFound asserts a contract-violating nil
// role is refused as not found rather than dereferenced.
func TestValidateAssignable_NilRoleIsNotFound(t *testing.T) {
	err := ValidateAssignable(context.Background(), nilRoleServicer{}, "r1", "acme")
	var rnf *RoleNotFoundError
	if !errors.As(err, &rnf) || rnf.ID != "r1" {
		t.Fatalf("ValidateAssignable(nil role) = %v, want *RoleNotFoundError for r1", err)
	}
}

// TestValidateAssignable_UnknownRoleIsAReferenceError asserts an unknown role
// is reported as a bad reference in the write, still matching the lookup's
// own not-found error underneath.
func TestValidateAssignable_UnknownRoleIsAReferenceError(t *testing.T) {
	err := ValidateAssignable(context.Background(), newTestStore(t), "no-such-role", "acme")
	var ref *ReferenceError
	if !errors.As(err, &ref) || ref.RoleID != "no-such-role" {
		t.Fatalf("err = %v, want *ReferenceError for no-such-role", err)
	}
	var rnf *RoleNotFoundError
	if !errors.As(err, &rnf) {
		t.Fatalf("err = %v, want it to wrap *RoleNotFoundError", err)
	}
}

func TestReferenceError_Error(t *testing.T) {
	tests := []struct {
		name string
		err  *ReferenceError
		want string
	}{
		{"with cause", &ReferenceError{RoleID: "r", Err: &RoleNotFoundError{ID: "r"}}, "role: reference to unknown role r: " + (&RoleNotFoundError{ID: "r"}).Error()},
		{"without cause", &ReferenceError{RoleID: "r"}, "role: reference to unknown role r"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}
