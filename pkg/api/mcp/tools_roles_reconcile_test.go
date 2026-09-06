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

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// TestRoleReconcileTool_Success covers the whole report crossing the MCP
// tool-result JSON: a result that lost its added/removed lists would still read
// as success and tell an operator nothing about what changed.
func TestRoleReconcileTool_Success(t *testing.T) {
	var gotTenant string
	svc := &mockRoleService{
		reconcileFn: func(_ context.Context, tenantID string) (*role.ReconcileReport, error) {
			gotTenant = tenantID
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
			}, nil
		},
	}
	handler := handleRoleReconcile(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{"tenant_id": "t1"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTenant != "t1" {
		t.Errorf("service saw tenant %q, want t1", gotTenant)
	}

	var report role.ReconcileReport
	if jsonErr := json.Unmarshal([]byte(resultText(t, result)), &report); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if report.TenantID != "t1" {
		t.Errorf("tenantID = %q, want t1", report.TenantID)
	}
	if len(report.Roles) != 2 {
		t.Fatalf("roles length = %d, want 2: %+v", len(report.Roles), report.Roles)
	}
	if len(report.Roles[0].Added) != 1 || report.Roles[0].Added[0] != "sdlc:bind" {
		t.Errorf("admin added = %v, want [sdlc:bind]", report.Roles[0].Added)
	}
	if len(report.Roles[0].Removed) != 1 || report.Roles[0].Removed[0] != "sdlc:register" {
		t.Errorf("admin removed = %v, want [sdlc:register]", report.Roles[0].Removed)
	}
	if !report.Roles[1].Created {
		t.Error("operator not reported as created")
	}
}

// TestRoleReconcileTool_NoTenant: an absent tenant_id means "my own tenant".
func TestRoleReconcileTool_NoTenant(t *testing.T) {
	svc := &mockRoleService{
		reconcileFn: func(_ context.Context, tenantID string) (*role.ReconcileReport, error) {
			if tenantID != "" {
				t.Errorf("tenantID = %q, want empty", tenantID)
			}
			return &role.ReconcileReport{TenantID: "own"}, nil
		},
	}
	handler := handleRoleReconcile(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var report role.ReconcileReport
	if jsonErr := json.Unmarshal([]byte(resultText(t, result)), &report); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if report.TenantID != "own" {
		t.Errorf("tenantID = %q, want own", report.TenantID)
	}
	if len(report.Roles) != 0 {
		t.Errorf("roles = %+v, want empty", report.Roles)
	}
}

func TestRoleReconcileTool_ServiceError(t *testing.T) {
	svc := &mockRoleService{
		reconcileFn: func(context.Context, string) (*role.ReconcileReport, error) {
			return nil, &role.StoreError{Op: "reconcile", Err: errors.New("storage failure")}
		},
	}
	handler := handleRoleReconcile(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{"tenant_id": "t1"}))
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("err = %v, want ToolError", err)
	}
	if toolErr.Tool != "qrdb_role_reconcile_builtin" {
		t.Errorf("Tool = %q, want qrdb_role_reconcile_builtin", toolErr.Tool)
	}
}
