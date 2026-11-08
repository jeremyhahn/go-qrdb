// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// The compliance-mode gate on the SO-exclusive role operations, roles:create
// and the built-in role reconcile, on the embedded transport.
package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

const (
	soExclusiveSO    = "acme-so"
	soExclusiveAdmin = "acme-admin"
)

// soExclusiveTransport builds an embedded transport whose role service
// grants roles:create to every caller and resolves the acme tenant's so and
// admin roles, so the only refusal left is the separation-of-duties gate.
// calls counts the role-service writes the gate let through.
func soExclusiveTransport(t *testing.T, strict bool, calls *int) *Transport {
	t.Helper()
	roleSvc := &mockRoleServicer{
		resolvePermsFn: func(context.Context, []string) ([]permission.Permission, error) {
			return []permission.Permission{permission.RolesCreate}, nil
		},
		getRoleByNameFn: func(_ context.Context, tenantID, name string) (*role.Role, error) {
			ids := map[string]string{role.RoleNameSO: soExclusiveSO, role.RoleNameAdmin: soExclusiveAdmin}
			id, ok := ids[name]
			if tenantID != "acme" || !ok {
				return nil, &role.RoleNotFoundError{ID: name}
			}
			return &role.Role{ID: id, TenantID: "acme", Name: name}, nil
		},
		createRoleFn: func(_ context.Context, req *role.CreateRoleRequest) (*role.Role, error) {
			*calls++
			return &role.Role{ID: "acme-auditor", TenantID: "acme", Name: req.Name}, nil
		},
		reconcileFn: func(_ context.Context, tenantID string) (*role.ReconcileReport, error) {
			*calls++
			return &role.ReconcileReport{TenantID: tenantID}, nil
		},
	}
	opts := []authmw.Option{authmw.WithRoleResolver(roleSvc)}
	if strict {
		opts = append(opts, authmw.WithStrictSeparation(true))
	}
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithRoleService(roleSvc),
		WithAuthMiddleware(authmw.New(rbac.New(roleSvc), opts...)),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tr
}

func TestEmbeddedSOExclusive_RoleCreateAndReconcile(t *testing.T) {
	ops := map[string]func(*Transport, context.Context) error{
		"create role": func(tr *Transport, ctx context.Context) error {
			_, err := tr.CreateRole(ctx, &transport.CreateRoleRequest{Name: "auditor", TenantID: "acme"})
			return err
		},
		"reconcile built-in roles": func(tr *Transport, ctx context.Context) error {
			_, err := tr.ReconcileBuiltInRoles(ctx, &transport.ReconcileRolesRequest{TenantID: "acme"})
			return err
		},
	}
	tests := []struct {
		name    string
		strict  bool
		roles   []string
		wantErr func(error) bool
	}{
		{name: "strict: separate SO", strict: true, roles: []string{soExclusiveSO}},
		{name: "strict: tenant admin", strict: true, roles: []string{soExclusiveAdmin}, wantErr: errorIs[*authmw.SORoleRequiredError]},
		{name: "strict: SO holding admin", strict: true, roles: []string{soExclusiveAdmin, soExclusiveSO}, wantErr: errorIs[*authmw.SORoleConflictError]},
		{name: "standard: tenant admin", roles: []string{soExclusiveAdmin}},
		{name: "standard: SO holding admin", roles: []string{soExclusiveAdmin, soExclusiveSO}},
	}
	for opName, op := range ops {
		for _, tt := range tests {
			t.Run(opName+"/"+tt.name, func(t *testing.T) {
				calls := 0
				tr := soExclusiveTransport(t, tt.strict, &calls)
				err := op(tr, roleAssignmentIdentityCtx("acme", tt.roles...))
				if tt.wantErr == nil {
					if err != nil {
						t.Fatalf("want success, got %v", err)
					}
					if calls != 1 {
						t.Fatalf("want one role-service call, got %d", calls)
					}
					return
				}
				if !tt.wantErr(err) {
					t.Fatalf("unexpected refusal %T: %v", err, err)
				}
				if calls != 0 {
					t.Fatalf("a refused operation reached the role service %d times", calls)
				}
			})
		}
	}
}

// TestEmbeddedSOExclusive_ReconcileAllTenants proves the all-tenant
// reconcile is SO-exclusive under strict separation: the system Admin, which
// holds roles:reconcile_all, is refused, and so is a system SO still holding
// admin; a separate system SO runs it. In the standard posture the system
// Admin runs it as before.
func TestEmbeddedSOExclusive_ReconcileAllTenants(t *testing.T) {
	admin := role.SystemRoleID(tenantid.System, role.RoleNameAdmin)
	so := role.SystemRoleID(tenantid.System, role.RoleNameSO)
	tests := []struct {
		name    string
		strict  bool
		roles   []string
		wantErr func(error) bool
	}{
		{name: "strict: separate SO", strict: true, roles: []string{so}},
		{name: "strict: system admin", strict: true, roles: []string{admin}, wantErr: errorIs[*authmw.SORoleRequiredError]},
		{name: "strict: SO holding admin", strict: true, roles: []string{admin, so}, wantErr: errorIs[*authmw.SORoleConflictError]},
		{name: "standard: system admin", roles: []string{admin}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roles := authtest.BindingRoles(t)
			opts := []authmw.Option{authmw.WithRoleResolver(roles), authmw.WithStrictSeparation(tt.strict)}
			tr, err := New(newMockKVService(), &mockAdminService{},
				WithRoleService(roles),
				WithTenantService(liveEmbeddedTenants(t, authtest.BindingTenant)),
				WithAuthMiddleware(authmw.New(rbac.New(roles), opts...)),
			)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_, err = tr.ReconcileBuiltInRolesAllTenants(roleAssignmentIdentityCtx(tenantid.System, tt.roles...))
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("want success, got %v", err)
				}
				return
			}
			if !tt.wantErr(err) {
				t.Fatalf("unexpected refusal %T: %v", err, err)
			}
		})
	}
}

func errorIs[E error](err error) bool {
	var target E
	return errors.As(err, &target)
}
