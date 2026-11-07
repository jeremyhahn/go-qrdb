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

package unix

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// unixTenants is a tenant service holding tenants t1 and broken.
func unixTenants(t *testing.T) tenant.TenantServicer {
	t.Helper()
	svc, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"t1", "broken"} {
		if _, err := svc.CreateTenant(context.Background(), id, "Tenant "+id, tenant.IsolationPrefix); err != nil {
			t.Fatal(err)
		}
	}
	return svc
}

// TestUnixRT_ReconcileBuiltInRolesAllTenants proves each tenant's report, and
// a failing tenant, survive the unix-socket gRPC wire intact.
func TestUnixRT_ReconcileBuiltInRolesAllTenants(t *testing.T) {
	rsvc := &mockRoleSvc{
		reconcileFn: func(_ context.Context, tenantID string) (*role.ReconcileReport, error) {
			if tenantID == "broken" {
				return nil, errors.New("backend unavailable")
			}
			return reconcileFixture(tenantID), nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithRoleService(rsvc), unixserver.WithTenantService(unixTenants(t)))
	defer cleanup()

	resp, err := tr.ReconcileBuiltInRolesAllTenants(context.Background())
	if err != nil {
		t.Fatalf("ReconcileBuiltInRolesAllTenants: %v", err)
	}
	var t1 *transport.ReconcileRolesResponse
	for i := range resp.Tenants {
		if resp.Tenants[i].TenantID == "t1" {
			t1 = &resp.Tenants[i]
		}
	}
	if t1 == nil || len(t1.Roles) != 2 || t1.Roles[0].Removed[0] != "sdlc:register" || !t1.Roles[1].Created {
		t.Fatalf("tenants = %+v; want t1 with the fixture's changes", resp.Tenants)
	}
	if len(resp.Failed) != 1 || resp.Failed[0].TenantID != "broken" || resp.Failed[0].Error == "" {
		t.Fatalf("failed = %+v; want broken with its error", resp.Failed)
	}
}

// TestUnixRT_ReconcileBuiltInRolesAllTenants_Unconfigured: a server without a
// tenant service answers with an error, not an empty report.
func TestUnixRT_ReconcileBuiltInRolesAllTenants_Unconfigured(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithRoleService(&mockRoleSvc{}))
	defer cleanup()

	if resp, err := tr.ReconcileBuiltInRolesAllTenants(context.Background()); err == nil {
		t.Fatalf("succeeded without a tenant service: %+v", resp)
	}
}

func TestUnixRT_ReconcileBuiltInRolesAllTenants_ClosedTransport(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.ReconcileBuiltInRolesAllTenants(context.Background())
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}
