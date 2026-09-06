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

package unix

import (
	"context"
	"errors"
	"testing"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// reconcileFixture is the report the unix round-trip assertion is written
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

// TestUnixRT_ReconcileBuiltInRoles proves the report survives the unix-socket
// gRPC wire intact. A report that lost its Added/Removed lists would still read
// as success and tell an operator nothing about what changed.
func TestUnixRT_ReconcileBuiltInRoles(t *testing.T) {
	var gotTenant string
	rsvc := &mockRoleSvc{
		reconcileFn: func(_ context.Context, tenantID string) (*role.ReconcileReport, error) {
			gotTenant = tenantID
			return reconcileFixture(tenantID), nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithRoleService(rsvc))
	defer cleanup()

	resp, err := tr.ReconcileBuiltInRoles(context.Background(), &transport.ReconcileRolesRequest{TenantID: "t1"})
	if err != nil {
		t.Fatalf("ReconcileBuiltInRoles: %v", err)
	}
	if gotTenant != "t1" {
		t.Errorf("service saw tenant %q, want t1", gotTenant)
	}
	if resp.TenantID != "t1" {
		t.Errorf("TenantID = %q, want t1", resp.TenantID)
	}
	if len(resp.Roles) != 2 {
		t.Fatalf("Roles len = %d, want 2: %+v", len(resp.Roles), resp.Roles)
	}
	if resp.Roles[0].Name != "admin" ||
		len(resp.Roles[0].Added) != 1 || resp.Roles[0].Added[0] != "sdlc:bind" ||
		len(resp.Roles[0].Removed) != 1 || resp.Roles[0].Removed[0] != "sdlc:register" {
		t.Errorf("admin result = %+v", resp.Roles[0])
	}
	if !resp.Roles[1].Created || resp.Roles[1].Name != "operator" {
		t.Errorf("operator result = %+v", resp.Roles[1])
	}
}

// TestUnixRT_ReconcileBuiltInRoles_ServiceError checks a failing reconcile
// surfaces rather than reading as "already converged".
func TestUnixRT_ReconcileBuiltInRoles_ServiceError(t *testing.T) {
	rsvc := &mockRoleSvc{
		reconcileFn: func(context.Context, string) (*role.ReconcileReport, error) {
			return nil, errors.New("backend unavailable")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithRoleService(rsvc))
	defer cleanup()

	resp, err := tr.ReconcileBuiltInRoles(context.Background(), &transport.ReconcileRolesRequest{TenantID: "t1"})
	if err == nil {
		t.Fatalf("succeeded against a failing service: %+v", resp)
	}
}

func TestUnixRT_ReconcileBuiltInRoles_ClosedTransport(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.ReconcileBuiltInRoles(context.Background(), &transport.ReconcileRolesRequest{TenantID: "t1"})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// TestUnixProtoToReconcileReport_Nil covers the nil guard in the converter.
func TestUnixProtoToReconcileReport_Nil(t *testing.T) {
	if got := protoToReconcileReport(nil); got != nil {
		t.Errorf("protoToReconcileReport(nil) = %+v, want nil", got)
	}
}
