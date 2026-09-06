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

package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// reconcileFixture is the report the MCP round-trip assertion is written
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

// TestMCPReconcileBuiltInRoles_RoundTrip proves the report survives the MCP
// tool-result JSON intact. The server marshals role.ReconcileReport directly,
// so this also pins the domain type's JSON tags against the client's decoder.
func TestMCPReconcileBuiltInRoles_RoundTrip(t *testing.T) {
	svc := newMockRoleSvc()
	svc.reconcileResp = reconcileFixture("t1")
	tr, cleanup := connectAuthInProcess(t, newMockUserSvc(), svc, nil)
	defer cleanup()

	resp, err := tr.ReconcileBuiltInRoles(context.Background(), &transport.ReconcileRolesRequest{TenantID: "t1"})
	if err != nil {
		t.Fatalf("ReconcileBuiltInRoles: %v", err)
	}
	if resp.TenantID != "t1" {
		t.Errorf("TenantID = %q, want t1", resp.TenantID)
	}
	if len(resp.Roles) != 2 {
		t.Fatalf("Roles len = %d, want 2: %+v", len(resp.Roles), resp.Roles)
	}
	if resp.Roles[0].RoleID != "r-admin" || resp.Roles[0].Name != "admin" {
		t.Errorf("role[0] = %+v", resp.Roles[0])
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
}

// TestMCPReconcileBuiltInRoles_NilRequest covers the "my own tenant" call, for
// which the tool receives no tenant_id argument at all.
func TestMCPReconcileBuiltInRoles_NilRequest(t *testing.T) {
	svc := newMockRoleSvc()
	tr, cleanup := connectAuthInProcess(t, newMockUserSvc(), svc, nil)
	defer cleanup()

	resp, err := tr.ReconcileBuiltInRoles(context.Background(), nil)
	if err != nil {
		t.Fatalf("ReconcileBuiltInRoles(nil): %v", err)
	}
	if len(resp.Roles) != 0 {
		t.Errorf("Roles = %+v, want empty", resp.Roles)
	}
}

func TestMCPReconcileBuiltInRoles_ServiceError(t *testing.T) {
	svc := newMockRoleSvc()
	svc.reconcileErr = errors.New("backend unavailable")
	tr, cleanup := connectAuthInProcess(t, newMockUserSvc(), svc, nil)
	defer cleanup()

	resp, err := tr.ReconcileBuiltInRoles(context.Background(), &transport.ReconcileRolesRequest{TenantID: "t1"})
	if err == nil {
		t.Fatalf("succeeded against a failing service: %+v", resp)
	}
}

func TestMCPReconcileBuiltInRoles_ClosedTransport(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.ReconcileBuiltInRoles(context.Background(), &transport.ReconcileRolesRequest{TenantID: "t1"})
	assertClosed(t, err)
}
