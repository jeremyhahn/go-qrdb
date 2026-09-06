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

package grpc

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// This file carries NO build constraint on purpose. mockRoleService is used
// from grpc_auth_test.go (`!integration`) and from service_roles_reconcile_test.go
// (unconstrained), so its constraint has to be the union of the two — which is
// none. It lived under `!integration` until the unconstrained caller arrived,
// and the package then stopped typechecking under `+integration` while `make
// vet`, which asks about one combination, stayed green.
//
// The type and its whole method set live here together: a method left behind
// under a narrower constraint would silently stop satisfying role.RoleServicer
// under the excluded tag, and the failure would surface at a construction site
// in some other file rather than here.

// mockRoleService is the role.RoleServicer test double for this package. Each
// call returns its configured error if set, and its configured result
// otherwise.
type mockRoleService struct {
	createResult    *role.Role
	createErr       error
	getResult       *role.Role
	getErr          error
	getByNameResult *role.Role
	getByNameErr    error
	updateResult    *role.Role
	updateErr       error
	deleteErr       error
	listResult      *role.ListRolesResponse
	listErr         error
	resolveResult   []permission.Permission
	resolveErr      error
	reconcileResult *role.ReconcileReport
	reconcileErr    error
	reconcileTenant string
}

func (m *mockRoleService) CreateRole(_ context.Context, _ *role.CreateRoleRequest) (*role.Role, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	return m.createResult, nil
}

func (m *mockRoleService) GetRole(_ context.Context, _ string) (*role.Role, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.getResult, nil
}

func (m *mockRoleService) GetRoleByName(_ context.Context, _, _ string) (*role.Role, error) {
	if m.getByNameErr != nil {
		return nil, m.getByNameErr
	}
	return m.getByNameResult, nil
}

func (m *mockRoleService) UpdateRole(_ context.Context, _ *role.UpdateRoleRequest) (*role.Role, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	return m.updateResult, nil
}

func (m *mockRoleService) DeleteRole(_ context.Context, _ string) error {
	return m.deleteErr
}

func (m *mockRoleService) ListRoles(_ context.Context, _ *role.ListRolesRequest) (*role.ListRolesResponse, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.listResult, nil
}

func (m *mockRoleService) ResolvePermissions(_ context.Context, _ []string) ([]permission.Permission, error) {
	if m.resolveErr != nil {
		return nil, m.resolveErr
	}
	return m.resolveResult, nil
}

func (m *mockRoleService) ReconcileBuiltInRoles(_ context.Context, tenantID string) (*role.ReconcileReport, error) {
	m.reconcileTenant = tenantID
	if m.reconcileErr != nil {
		return nil, m.reconcileErr
	}
	if m.reconcileResult != nil {
		return m.reconcileResult, nil
	}
	return &role.ReconcileReport{TenantID: tenantID}, nil
}

// The assertion belongs beside the type, so the next omitted method fails here
// rather than at a caller.
var _ role.RoleServicer = (*mockRoleService)(nil)
