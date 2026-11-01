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
	"errors"
	"log/slog"
	"testing"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// withMCPTenants is the server option for a tenant service holding tenant t1.
func withMCPTenants(t *testing.T) mcpapi.ServerOption {
	t.Helper()
	svc, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateTenant(context.Background(), "t1", "Tenant t1", tenant.IsolationPrefix); err != nil {
		t.Fatal(err)
	}
	return mcpapi.WithTenantService(svc)
}

// TestMCPReconcileBuiltInRolesAllTenants_RoundTrip proves the report survives
// the tool-result JSON intact. The server marshals role.AllTenantsReconcileReport
// directly, so this pins the domain type's JSON tags against the decoder.
func TestMCPReconcileBuiltInRolesAllTenants_RoundTrip(t *testing.T) {
	svc := newMockRoleSvc()
	svc.reconcileResp = reconcileFixture("t1")
	tr, cleanup := connectAuthInProcess(t, newMockUserSvc(), svc, nil, withMCPTenants(t))
	defer cleanup()

	resp, err := tr.ReconcileBuiltInRolesAllTenants(context.Background())
	if err != nil {
		t.Fatalf("ReconcileBuiltInRolesAllTenants: %v", err)
	}
	if len(resp.Tenants) == 0 {
		t.Fatal("no tenant reported")
	}
	got := resp.Tenants[0]
	if got.TenantID != "t1" || len(got.Roles) != 2 || got.Roles[0].RoleID != "r-admin" ||
		got.Roles[0].Removed[0] != "sdlc:register" || !got.Roles[1].Created {
		t.Fatalf("tenant = %+v; want the fixture", got)
	}
}

// TestMCPReconcileBuiltInRolesAllTenants_FailedTenants: a failing tenant is a
// partial pass carried in Failed, with its tenant id and error decoded.
func TestMCPReconcileBuiltInRolesAllTenants_FailedTenants(t *testing.T) {
	svc := newMockRoleSvc()
	svc.reconcileErr = errors.New("backend unavailable")
	tr, cleanup := connectAuthInProcess(t, newMockUserSvc(), svc, nil, withMCPTenants(t))
	defer cleanup()

	resp, err := tr.ReconcileBuiltInRolesAllTenants(context.Background())
	if err != nil {
		t.Fatalf("ReconcileBuiltInRolesAllTenants: %v", err)
	}
	var t1 bool
	for _, f := range resp.Failed {
		if f.TenantID == "t1" && f.Error != "" {
			t1 = true
		}
	}
	if !t1 {
		t.Fatalf("failed = %+v; want t1 with its error", resp.Failed)
	}
}

// TestMCPReconcileBuiltInRolesAllTenants_NotOffered: a server without a tenant
// service does not offer the tool, and the call fails.
func TestMCPReconcileBuiltInRolesAllTenants_NotOffered(t *testing.T) {
	tr, cleanup := connectAuthInProcess(t, newMockUserSvc(), newMockRoleSvc(), nil)
	defer cleanup()

	if resp, err := tr.ReconcileBuiltInRolesAllTenants(context.Background()); err == nil {
		t.Fatalf("succeeded without the tool: %+v", resp)
	}
}

func TestMCPReconcileBuiltInRolesAllTenants_ClosedTransport(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.ReconcileBuiltInRolesAllTenants(context.Background())
	assertClosed(t, err)
}
