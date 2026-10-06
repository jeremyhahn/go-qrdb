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

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Round-trip tests for the tenant non-barrier CRUD methods. Uses the
// mockTenantSvc fixture defined in transport_tenant_test.go.

// ---------------------------------------------------------------------------
// CreateTenant
// ---------------------------------------------------------------------------

func TestCreateTenant_Success(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc, mcpapi.WithTenantOnboarding(onboardingFromTenant(svc)))
	defer cleanup()

	resp, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{
		ID:            "acme",
		Name:          "Acme Corp",
		IsolationType: transport.TenantIsolationBarrier,
		AdminUsername: "admin",
	})
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if resp.Tenant.ID != "acme" {
		t.Errorf("id: got %q, want %q", resp.Tenant.ID, "acme")
	}
	if resp.Tenant.Name != "Acme Corp" {
		t.Errorf("name: got %q, want %q", resp.Tenant.Name, "Acme Corp")
	}
}

func TestCreateTenant_ServiceError(t *testing.T) {
	svc := newMockTenantSvc()
	svc.createErr = errors.New("already exists")
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	_, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{
		ID: "x", Name: "y",
	})
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected *transport.RequestError, got %T", err)
	}
}

func TestCreateTenant_Closed(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	cleanup()

	_, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{ID: "x", Name: "y"})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// GetTenant
// ---------------------------------------------------------------------------

func TestGetTenant_Success(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	resp, err := tr.GetTenant(context.Background(), "acme")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if resp.Tenant.ID != "acme" {
		t.Errorf("id: got %q, want %q", resp.Tenant.ID, "acme")
	}
}

func TestGetTenant_ServiceError(t *testing.T) {
	svc := newMockTenantSvc()
	svc.getErr = errors.New("not found")
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	_, err := tr.GetTenant(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected *transport.RequestError, got %T", err)
	}
}

func TestGetTenant_Closed(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	cleanup()

	_, err := tr.GetTenant(context.Background(), "acme")
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// ListTenants
// ---------------------------------------------------------------------------

func TestListTenants_Success(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	resp, err := tr.ListTenants(context.Background())
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if len(resp.Tenants) == 0 {
		t.Error("expected at least one tenant")
	}
}

func TestListTenants_ServiceError(t *testing.T) {
	svc := newMockTenantSvc()
	svc.listErr = errors.New("list failed")
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	_, err := tr.ListTenants(context.Background())
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected *transport.RequestError, got %T", err)
	}
}

func TestListTenants_Closed(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	cleanup()

	_, err := tr.ListTenants(context.Background())
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// DeleteTenant
// ---------------------------------------------------------------------------

func TestDeleteTenant_Success(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	if err := tr.DeleteTenant(context.Background(), "acme"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
}

func TestDeleteTenant_ServiceError(t *testing.T) {
	svc := newMockTenantSvc()
	svc.deleteErr = errors.New("not found")
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	err := tr.DeleteTenant(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected *transport.RequestError, got %T", err)
	}
}

func TestDeleteTenant_Closed(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	cleanup()

	err := tr.DeleteTenant(context.Background(), "acme")
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// UpdateTenant
// ---------------------------------------------------------------------------

func TestUpdateTenant_Success(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	resp, err := tr.UpdateTenant(context.Background(), &transport.UpdateTenantRequest{
		TenantID:      "acme",
		Name:          "Acme Corp Updated",
		IsolationType: transport.TenantIsolationPrefix,
	})
	if err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}
	if resp.Tenant.ID != "acme" {
		t.Errorf("id: got %q, want %q", resp.Tenant.ID, "acme")
	}
}

func TestUpdateTenant_ServiceError(t *testing.T) {
	svc := newMockTenantSvc()
	svc.updateErr = errors.New("update failed")
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	_, err := tr.UpdateTenant(context.Background(), &transport.UpdateTenantRequest{TenantID: "x"})
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected *transport.RequestError, got %T", err)
	}
}

func TestUpdateTenant_Closed(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	cleanup()

	_, err := tr.UpdateTenant(context.Background(), &transport.UpdateTenantRequest{TenantID: "x"})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// UpdateTenantQuotas
// ---------------------------------------------------------------------------

func TestUpdateTenantQuotas_Success(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	resp, err := tr.UpdateTenantQuotas(context.Background(), &transport.UpdateTenantQuotasRequest{
		TenantID: "acme",
		Quotas: transport.TenantQuotas{
			MaxDatabases:    10,
			MaxKeys:         1000,
			MaxStorageBytes: 1 << 30,
		},
	})
	if err != nil {
		t.Fatalf("UpdateTenantQuotas: %v", err)
	}
	if resp.Tenant.ID != "acme" {
		t.Errorf("id: got %q, want %q", resp.Tenant.ID, "acme")
	}
}

func TestUpdateTenantQuotas_ServiceError(t *testing.T) {
	svc := newMockTenantSvc()
	svc.updateQuotasErr = errors.New("invalid quotas")
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	_, err := tr.UpdateTenantQuotas(context.Background(), &transport.UpdateTenantQuotasRequest{TenantID: "x"})
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected *transport.RequestError, got %T", err)
	}
}

func TestUpdateTenantQuotas_Closed(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	cleanup()

	_, err := tr.UpdateTenantQuotas(context.Background(), &transport.UpdateTenantQuotasRequest{TenantID: "x"})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// UpdateTenantEntitlements
// ---------------------------------------------------------------------------

// TestUpdateTenantEntitlements_BarrierModesAndWALRoundTrip is a regression
// test for the MCP entitlements round-trip bug: the SDK client encoder never
// sent the allowed_barrier_modes/allow_dedicated_wal tool arguments, so a
// `tenant set-entitlement --allowed-barrier-modes ...` call over the MCP
// transport silently cleared those fields even though the request carried
// them. mockTenantSvc.UpdateEntitlements echoes back exactly the
// *quota.Entitlements it receives, so this exercises the full SDK encode ->
// tool call -> server decode -> service -> server encode -> SDK decode path.
func TestUpdateTenantEntitlements_BarrierModesAndWALRoundTrip(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	resp, err := tr.UpdateTenantEntitlements(context.Background(), &transport.UpdateTenantEntitlementsRequest{
		TenantID: "acme",
		Entitlements: transport.TenantEntitlements{
			AllowedBarrierModes: []string{"none", "passphrase", "shamir"},
			AllowDedicatedWAL:   true,
		},
	})
	if err != nil {
		t.Fatalf("UpdateTenantEntitlements: %v", err)
	}
	if resp.Tenant.Entitlements == nil {
		t.Fatal("expected non-nil entitlements in response")
	}
	got := resp.Tenant.Entitlements.AllowedBarrierModes
	want := []string{"none", "passphrase", "shamir"}
	if len(got) != len(want) {
		t.Fatalf("AllowedBarrierModes: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AllowedBarrierModes: got %v, want %v", got, want)
		}
	}
	if !resp.Tenant.Entitlements.AllowDedicatedWAL {
		t.Error("expected AllowDedicatedWAL true in response")
	}
}

func TestUpdateTenantEntitlements_ServiceError(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	_, err := tr.UpdateTenantEntitlements(context.Background(), &transport.UpdateTenantEntitlementsRequest{TenantID: "acme"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpdateTenantEntitlements_Closed(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	cleanup()

	_, err := tr.UpdateTenantEntitlements(context.Background(), &transport.UpdateTenantEntitlementsRequest{TenantID: "acme"})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// mcpTenantInfoToTransport
// ---------------------------------------------------------------------------

func TestMcpTenantInfoToTransport_Nil(t *testing.T) {
	out := mcpTenantInfoToTransport(nil)
	if out.ID != "" || out.Name != "" {
		t.Errorf("expected zero value, got %+v", out)
	}
}

func TestMcpTenantInfoToTransport_Populated(t *testing.T) {
	in := &mcpTenantInfo{
		ID:            "t1",
		Name:          "tenant 1",
		IsolationType: transport.TenantIsolationBarrier,
		Quotas:        &transport.TenantQuotas{MaxKeys: 100},
	}
	out := mcpTenantInfoToTransport(in)
	if out.ID != "t1" || out.Name != "tenant 1" {
		t.Errorf("identity: got %+v", out)
	}
	if out.IsolationType != transport.TenantIsolationBarrier {
		t.Errorf("isolation: got %q", out.IsolationType)
	}
	if out.Quotas == nil || out.Quotas.MaxKeys != 100 {
		t.Errorf("quotas: got %+v", out.Quotas)
	}
}

// TestMcpTenantInfoToTransport_Timestamps is a regression test: the
// conversion previously dropped CreatedAt/UpdatedAt entirely, leaving them
// at the zero time regardless of what the server reported.
func TestMcpTenantInfoToTransport_Timestamps(t *testing.T) {
	in := &mcpTenantInfo{
		ID:        "t1",
		CreatedAt: "2026-01-02T15:04:05Z",
		UpdatedAt: "2026-03-04T05:06:07Z",
	}
	out := mcpTenantInfoToTransport(in)
	if out.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
	if out.UpdatedAt.IsZero() {
		t.Error("expected non-zero UpdatedAt")
	}
	if !out.CreatedAt.Equal(out.CreatedAt.UTC()) {
		t.Errorf("expected UTC CreatedAt, got %v", out.CreatedAt)
	}
	wantCreated := "2026-01-02T15:04:05Z"
	if got := out.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"); got != wantCreated {
		t.Errorf("CreatedAt: got %q, want %q", got, wantCreated)
	}
}

// TestMcpTenantInfoToTransport_EmptyTimestamps confirms empty timestamp
// strings leave CreatedAt/UpdatedAt at their zero value instead of erroring.
func TestMcpTenantInfoToTransport_EmptyTimestamps(t *testing.T) {
	out := mcpTenantInfoToTransport(&mcpTenantInfo{ID: "t1"})
	if !out.CreatedAt.IsZero() || !out.UpdatedAt.IsZero() {
		t.Errorf("expected zero timestamps, got %+v", out)
	}
}
