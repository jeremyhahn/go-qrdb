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

package rest

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// reconcileAllBody is POST /roles/reconcile/all-tenants's body in the field
// names pkg/api/rest writes, spelled out rather than marshalled from the
// transport type so a drift between the two fails here.
const reconcileAllBody = `{
 "tenants": [{"tenant_id": "t1", "roles": [{"role_id": "r-admin", "name": "admin", "created": false,
   "added": ["certs:issue"], "removed": ["certs:generate"]}]}],
 "skipped": [{"tenant_id": "vault", "reason": "sealed", "detail": "barrier sealed"}],
 "failed": [{"tenant_id": "broken", "error": "storage unavailable"}]
}`

// TestRESTReconcileBuiltInRolesAllTenants_RoundTrip proves the call reaches
// POST /roles/reconcile/all-tenants and every list of the report comes back.
func TestRESTReconcileBuiltInRolesAllTenants_RoundTrip(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(
		pathAndQuery(http.MethodPost, "/roles/reconcile/all-tenants", ""),
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(reconcileAllBody))
		},
	)

	resp, err := rt.ReconcileBuiltInRolesAllTenants(context.Background())
	if err != nil {
		t.Fatalf("ReconcileBuiltInRolesAllTenants: %v", err)
	}
	if len(resp.Tenants) != 1 || resp.Tenants[0].TenantID != "t1" || resp.Tenants[0].Roles[0].Removed[0] != "certs:generate" {
		t.Fatalf("tenants = %+v; want t1 with the admin change", resp.Tenants)
	}
	if len(resp.Skipped) != 1 || resp.Skipped[0].Reason != "sealed" || resp.Skipped[0].Detail != "barrier sealed" {
		t.Fatalf("skipped = %+v; want vault sealed", resp.Skipped)
	}
	if len(resp.Failed) != 1 || resp.Failed[0].TenantID != "broken" || resp.Failed[0].Error != "storage unavailable" {
		t.Fatalf("failed = %+v; want broken", resp.Failed)
	}
}

func TestRESTReconcileBuiltInRolesAllTenants_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(
		pathAndQuery(http.MethodPost, "/roles/reconcile/all-tenants", ""),
		func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "forbidden", http.StatusForbidden)
		},
	)

	if resp, err := rt.ReconcileBuiltInRolesAllTenants(context.Background()); err == nil {
		t.Fatalf("succeeded against a refusing server: %+v", resp)
	}
}

func TestRESTReconcileBuiltInRolesAllTenants_ClosedTransport(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()

	_, err := rt.ReconcileBuiltInRolesAllTenants(context.Background())
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}
