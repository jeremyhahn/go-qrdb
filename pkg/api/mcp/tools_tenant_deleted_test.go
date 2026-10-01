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
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// retiredMCPTenants returns the REAL seal-disabled tenant service with the
// named tenants created and deleted through it.
func retiredMCPTenants(t *testing.T, ids ...string) tenant.TenantServicer {
	t.Helper()
	svc, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, id := range ids {
		if _, err := svc.CreateTenant(ctx, id, "Tenant "+id, tenant.IsolationPrefix); err != nil {
			t.Fatal(err)
		}
		if err := svc.DeleteTenant(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	return svc
}

type failingMCPDeletedSvc struct {
	tenant.TenantServicer
	report *tenant.ReconcileReport
	err    error
}

func (f failingMCPDeletedSvc) ListDeletedTenants(context.Context, string, int) (*tenant.DeletedTenantsPage, error) {
	return nil, f.err
}

func (f failingMCPDeletedSvc) ReconcileDeletedTenants(context.Context) (*tenant.ReconcileReport, error) {
	return f.report, f.err
}

func TestHandleTenantListDeleted_PagesThroughRetiredIds(t *testing.T) {
	handler := handleTenantListDeleted(retiredMCPTenants(t, "bravo", "alpha", "charlie"))
	ctx := context.Background()

	result, err := handler(ctx, buildCallToolRequest(t, "qrdb_tenant_list_deleted", map[string]any{"page_size": 2}))
	if err != nil || result.IsError {
		t.Fatalf("page 1 = %v, %v", result, err)
	}
	var first tenant.DeletedTenantsPage
	if err := json.Unmarshal([]byte(resultText(t, result)), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Tombstones) != 2 || first.Tombstones[0].ID != "alpha" || first.NextPageToken == "" {
		t.Fatalf("first page = %+v", first)
	}

	result, err = handler(ctx, buildCallToolRequest(t, "qrdb_tenant_list_deleted",
		map[string]any{"page_size": 2, "page_token": first.NextPageToken}))
	if err != nil || result.IsError {
		t.Fatalf("page 2 = %v, %v", result, err)
	}
	var second tenant.DeletedTenantsPage
	if err := json.Unmarshal([]byte(resultText(t, result)), &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Tombstones) != 1 || second.Tombstones[0].ID != "charlie" {
		t.Fatalf("second page = %+v", second)
	}
}

func TestHandleTenantReconcileDeleted_ReportsWhatItInspected(t *testing.T) {
	handler := handleTenantReconcileDeleted(retiredMCPTenants(t, "alpha"))
	result, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_tenant_reconcile_deleted", nil))
	if err != nil || result.IsError {
		t.Fatalf("reconcile = %v, %v", result, err)
	}
	var report tenant.ReconcileReport
	if err := json.Unmarshal([]byte(resultText(t, result)), &report); err != nil {
		t.Fatal(err)
	}
	if report.Inspected != 1 {
		t.Fatalf("inspected = %d; want 1", report.Inspected)
	}
}

func TestHandleTenantDeletedTools_ServiceErrorsAreToolErrors(t *testing.T) {
	var pr *tenant.InvalidPageRequestError
	_, err := handleTenantListDeleted(retiredMCPTenants(t))(context.Background(),
		buildCallToolRequest(t, "qrdb_tenant_list_deleted", map[string]any{"page_size": -1}))
	var te *ToolError
	if !errors.As(err, &te) || !errors.As(err, &pr) {
		t.Fatalf("negative page size = %v; want a ToolError wrapping *InvalidPageRequestError", err)
	}

	boom := errors.New("boom")
	svc := failingMCPDeletedSvc{err: boom}
	if _, err := handleTenantListDeleted(svc)(context.Background(), buildCallToolRequest(t, "qrdb_tenant_list_deleted", nil)); !errors.Is(err, boom) {
		t.Fatalf("list error = %v", err)
	}
	if _, err := handleTenantReconcileDeleted(svc)(context.Background(), buildCallToolRequest(t, "qrdb_tenant_reconcile_deleted", nil)); !errors.Is(err, boom) {
		t.Fatalf("reconcile error = %v", err)
	}
}
