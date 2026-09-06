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

package grpc

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// TestGRPC_ReconcileBuiltInRoles_Success covers the whole report crossing the
// proto boundary: a response that lost its added/removed lists would still read
// as success and tell an operator nothing about what changed.
func TestGRPC_ReconcileBuiltInRoles_Success(t *testing.T) {
	roleSvc := &mockRoleService{
		reconcileResult: &role.ReconcileReport{
			TenantID: "t1",
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
		},
	}
	client, cleanup := startTestServerFull(t, WithRoleService(roleSvc))
	defer cleanup()

	resp, err := client.ReconcileBuiltInRoles(context.Background(), &pb.ReconcileBuiltInRolesRequest{
		TenantId: "t1",
	})
	if err != nil {
		t.Fatalf("ReconcileBuiltInRoles failed: %v", err)
	}
	if roleSvc.reconcileTenant != "t1" {
		t.Errorf("service saw tenant %q, want t1", roleSvc.reconcileTenant)
	}
	if resp.GetTenantId() != "t1" {
		t.Errorf("tenant_id = %q, want t1", resp.GetTenantId())
	}
	if len(resp.GetRoles()) != 2 {
		t.Fatalf("roles length = %d, want 2", len(resp.GetRoles()))
	}

	admin := resp.GetRoles()[0]
	if admin.GetName() != role.RoleNameAdmin || admin.GetRoleId() != "r-admin" {
		t.Errorf("role[0] = %s/%s, want admin/r-admin", admin.GetName(), admin.GetRoleId())
	}
	if len(admin.GetAdded()) != 1 || admin.GetAdded()[0] != "sdlc:bind" {
		t.Errorf("admin added = %v, want [sdlc:bind]", admin.GetAdded())
	}
	if len(admin.GetRemoved()) != 1 || admin.GetRemoved()[0] != "sdlc:register" {
		t.Errorf("admin removed = %v, want [sdlc:register]", admin.GetRemoved())
	}
	if !resp.GetRoles()[1].GetCreated() {
		t.Error("operator not reported as created")
	}
}

// TestGRPC_ReconcileBuiltInRoles_EmptyTenant: an empty tenant_id means "my own
// tenant" and reaches the service unchanged, so the routing layer resolves it.
func TestGRPC_ReconcileBuiltInRoles_EmptyTenant(t *testing.T) {
	roleSvc := &mockRoleService{}
	client, cleanup := startTestServerFull(t, WithRoleService(roleSvc))
	defer cleanup()

	resp, err := client.ReconcileBuiltInRoles(context.Background(), &pb.ReconcileBuiltInRolesRequest{})
	if err != nil {
		t.Fatalf("ReconcileBuiltInRoles failed: %v", err)
	}
	if roleSvc.reconcileTenant != "" {
		t.Errorf("service saw tenant %q, want empty", roleSvc.reconcileTenant)
	}
	if len(resp.GetRoles()) != 0 {
		t.Errorf("roles = %v, want empty", resp.GetRoles())
	}
}

func TestGRPC_ReconcileBuiltInRoles_ServiceError(t *testing.T) {
	roleSvc := &mockRoleService{
		reconcileErr: &role.StoreError{Op: "reconcile", Err: errors.New("storage failure")},
	}
	client, cleanup := startTestServerFull(t, WithRoleService(roleSvc))
	defer cleanup()

	_, err := client.ReconcileBuiltInRoles(context.Background(), &pb.ReconcileBuiltInRolesRequest{
		TenantId: "t1",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

// TestGRPC_ReconcileBuiltInRoles_NoRoleService reports Unimplemented rather
// than a silent empty report when the role service is not wired.
func TestGRPC_ReconcileBuiltInRoles_NoRoleService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.ReconcileBuiltInRoles(context.Background(), &pb.ReconcileBuiltInRolesRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// TestReconcileReportToProto_Nil covers the nil guard in the converter.
func TestReconcileReportToProto_Nil(t *testing.T) {
	if got := reconcileReportToProto(nil); got != nil {
		t.Errorf("reconcileReportToProto(nil) = %+v, want nil", got)
	}
}
