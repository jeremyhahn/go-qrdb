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
	"log/slog"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

const reconcileAllTool = "qrdb_role_reconcile_builtin_all_tenants"

// liveMCPTenants is a tenant service holding the given live tenants.
func liveMCPTenants(t *testing.T, ids ...string) tenant.TenantServicer {
	t.Helper()
	svc, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if _, err := svc.CreateTenant(context.Background(), id, "Tenant "+id, tenant.IsolationPrefix); err != nil {
			t.Fatal(err)
		}
	}
	return svc
}

// TestRoleReconcileAllTenantsTool_ReportsEveryTenant decodes the tool's JSON
// in the field names the SDK's MCP client reads: every live tenant is
// converged and the failing one is listed without failing the call.
func TestRoleReconcileAllTenantsTool_ReportsEveryTenant(t *testing.T) {
	svc := &mockRoleService{
		reconcileFn: func(_ context.Context, tenantID string) (*role.ReconcileReport, error) {
			if tenantID == "broken" {
				return nil, &role.StoreError{Op: "reconcile", Err: errors.New("storage failure")}
			}
			return &role.ReconcileReport{TenantID: tenantID, Roles: []role.RoleReconcileResult{{
				RoleID: "r-admin", Name: role.RoleNameAdmin,
				Added:   []permission.Permission{"certs:issue"},
				Removed: []permission.Permission{"certs:generate"},
			}}}, nil
		},
	}
	tools := roleReconcileAllTools(svc, liveMCPTenants(t, "acme", "broken"))
	if len(tools) != 1 || tools[0].Tool.Name != reconcileAllTool || tools[0].Perm != permission.RolesReconcileAll {
		t.Fatalf("tools = %+v; want the one %s tool gated by roles:reconcile_all", tools, reconcileAllTool)
	}

	ctx := tenant.WithTenantID(context.Background(), tenantid.System)
	result, err := tools[0].Handler(ctx, mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var body struct {
		Tenants []struct {
			TenantID string `json:"tenantID"`
			Roles    []struct {
				Removed []string `json:"removed"`
			} `json:"roles"`
		} `json:"tenants"`
		Failed []struct {
			TenantID string `json:"tenantID"`
			Error    string `json:"error"`
		} `json:"failed"`
	}
	if err := json.Unmarshal([]byte(resultText(t, result)), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var acme bool
	for _, tr := range body.Tenants {
		if tr.TenantID == "acme" && len(tr.Roles) == 1 && tr.Roles[0].Removed[0] == "certs:generate" {
			acme = true
		}
	}
	if !acme {
		t.Fatalf("tenants = %+v; want acme with the admin change", body.Tenants)
	}
	if len(body.Failed) != 1 || body.Failed[0].TenantID != "broken" || body.Failed[0].Error == "" {
		t.Fatalf("failed = %+v; want broken with its error", body.Failed)
	}
}

// TestRoleReconcileAllTenantsTool_RefusesTenantCaller: a tenant's caller gets
// a ToolError carrying the platform-operator refusal.
func TestRoleReconcileAllTenantsTool_RefusesTenantCaller(t *testing.T) {
	handler := handleRoleReconcileAllTenants(&mockRoleService{}, liveMCPTenants(t, "acme"))

	_, err := handler(tenant.WithTenantID(context.Background(), "acme"), mcp.CallToolRequest{})
	var toolErr *ToolError
	if !errors.As(err, &toolErr) || toolErr.Tool != reconcileAllTool {
		t.Fatalf("err = %v; want a %s ToolError", err, reconcileAllTool)
	}
	var refused *tenant.PlatformOperatorRequiredError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v; want PlatformOperatorRequiredError", err)
	}
}

// TestRoleReconcileAllTenantsTool_RegisteredOnlyWithBothServices: the tool
// needs the tenant service, so a server without it does not offer it.
func TestRoleReconcileAllTenantsTool_RegisteredOnlyWithBothServices(t *testing.T) {
	with := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithRoleService(&mockRoleService{}), WithTenantService(liveMCPTenants(t)))
	without := NewServer(newMockKVService(), newMockAdminService(), ":0", WithRoleService(&mockRoleService{}))
	if _, ok := with.MCPServer().ListTools()[reconcileAllTool]; !ok {
		t.Fatalf("%s not registered with role and tenant services", reconcileAllTool)
	}
	if _, ok := without.MCPServer().ListTools()[reconcileAllTool]; ok {
		t.Fatalf("%s registered without a tenant service", reconcileAllTool)
	}
}
