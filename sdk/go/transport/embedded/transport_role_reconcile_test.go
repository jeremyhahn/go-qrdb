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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// reconcileFixture is the report the embedded round-trip assertion is written
// against: one repaired role carrying an add AND a removal (the go-qrdb#354
// shape) and one seeded role.
func reconcileFixture(tenantID string) *role.ReconcileReport {
	return &role.ReconcileReport{
		TenantID: tenantID,
		Roles: []role.RoleReconcileResult{
			{
				RoleID:  "r-admin",
				Name:    role.RoleNameAdmin,
				Added:   []permission.Permission{"sdlc:bind"},
				Removed: []permission.Permission{"sdlc:register"},
			},
			{
				RoleID:  "r-operator",
				Name:    role.RoleNameOperator,
				Created: true,
				Added:   []permission.Permission{"orchestrator:task_block"},
			},
		},
	}
}

func TestReconcileBuiltInRoles_Success(t *testing.T) {
	var gotTenant string
	mock := &mockRoleServicer{
		reconcileFn: func(_ context.Context, tenantID string) (*role.ReconcileReport, error) {
			gotTenant = tenantID
			return reconcileFixture(tenantID), nil
		},
	}
	tr := newAuthTransport(t, WithRoleService(mock))

	resp, err := tr.ReconcileBuiltInRoles(context.Background(), &transport.ReconcileRolesRequest{TenantID: "tenant-a"})
	if err != nil {
		t.Fatalf("ReconcileBuiltInRoles: %v", err)
	}
	if gotTenant != "tenant-a" {
		t.Errorf("service saw tenant %q, want tenant-a", gotTenant)
	}
	if resp.TenantID != "tenant-a" {
		t.Errorf("TenantID = %q, want tenant-a", resp.TenantID)
	}
	if len(resp.Roles) != 2 {
		t.Fatalf("Roles len = %d, want 2: %+v", len(resp.Roles), resp.Roles)
	}
	if len(resp.Roles[0].Added) != 1 || resp.Roles[0].Added[0] != "sdlc:bind" {
		t.Errorf("admin added = %v, want [sdlc:bind]", resp.Roles[0].Added)
	}
	if len(resp.Roles[0].Removed) != 1 || resp.Roles[0].Removed[0] != "sdlc:register" {
		t.Errorf("admin removed = %v, want [sdlc:register]", resp.Roles[0].Removed)
	}
	if !resp.Roles[1].Created {
		t.Error("operator not reported as created")
	}
	if len(resp.Roles[1].Removed) != 0 {
		t.Errorf("operator removed = %v, want none", resp.Roles[1].Removed)
	}
}

// TestReconcileBuiltInRoles_NilRequest covers the "my own tenant" call.
func TestReconcileBuiltInRoles_NilRequest(t *testing.T) {
	mock := &mockRoleServicer{
		reconcileFn: func(_ context.Context, tenantID string) (*role.ReconcileReport, error) {
			return &role.ReconcileReport{TenantID: tenantID}, nil
		},
	}
	tr := newAuthTransport(t, WithRoleService(mock))

	resp, err := tr.ReconcileBuiltInRoles(context.Background(), nil)
	if err != nil {
		t.Fatalf("ReconcileBuiltInRoles(nil): %v", err)
	}
	if len(resp.Roles) != 0 {
		t.Errorf("Roles = %+v, want empty", resp.Roles)
	}
}

// TestReconcileBuiltInRoles_NilReport covers a servicer that answers with no
// report at all: the transport must still return a usable response rather than
// dereference nil.
func TestReconcileBuiltInRoles_NilReport(t *testing.T) {
	mock := &mockRoleServicer{
		reconcileFn: func(context.Context, string) (*role.ReconcileReport, error) {
			return nil, nil
		},
	}
	tr := newAuthTransport(t, WithRoleService(mock))

	resp, err := tr.ReconcileBuiltInRoles(context.Background(), &transport.ReconcileRolesRequest{TenantID: "tenant-a"})
	if err != nil {
		t.Fatalf("ReconcileBuiltInRoles: %v", err)
	}
	if resp == nil || resp.TenantID != "tenant-a" || len(resp.Roles) != 0 {
		t.Errorf("resp = %+v, want an empty report for tenant-a", resp)
	}
}

func TestReconcileBuiltInRoles_NilService(t *testing.T) {
	tr := newAuthTransport(t)
	_, err := tr.ReconcileBuiltInRoles(context.Background(), &transport.ReconcileRolesRequest{TenantID: "tenant-a"})
	assertNotImplementedError(t, err, "ReconcileBuiltInRoles")
}

func TestReconcileBuiltInRoles_ServiceError(t *testing.T) {
	mock := &mockRoleServicer{
		reconcileFn: func(context.Context, string) (*role.ReconcileReport, error) {
			return nil, errors.New("backend unavailable")
		},
	}
	tr := newAuthTransport(t, WithRoleService(mock))

	resp, err := tr.ReconcileBuiltInRoles(context.Background(), &transport.ReconcileRolesRequest{TenantID: "tenant-a"})
	if err == nil {
		t.Fatalf("succeeded against a failing service: %+v", resp)
	}
}
