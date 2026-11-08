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

package quic

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestE2E_ReconcileBuiltInRolesAllTenants proves every list of the report
// survives the QUIC wire's proto body intact.
func TestE2E_ReconcileBuiltInRolesAllTenants(t *testing.T) {
	srv, tr := startE2EServer(t)

	srv.SetResponder("role.ReconcileBuiltInAllTenants", func(_ []byte) (uint16, []byte) {
		// The server's JSON, key for key (pkg/api/quic
		// reconcileRolesAllTenantsWireResponse).
		return wire.StatusOK, []byte(`{
			"tenants": [{"tenantID": "t1", "roles": [
				{"roleID": "r-admin", "name": "admin", "added": ["certs:issue"], "removed": ["certs:generate"]}
			]}],
			"skipped": [{"tenantID": "vault", "reason": "sealed", "detail": "barrier sealed"}],
			"failed": [{"tenantID": "broken", "error": "storage unavailable"}]
		}`)
	})

	resp, err := tr.ReconcileBuiltInRolesAllTenants(context.Background())
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

func TestE2E_ReconcileBuiltInRolesAllTenants_MalformedResponse(t *testing.T) {
	srv, tr := startE2EServer(t)

	srv.SetResponder("role.ReconcileBuiltInAllTenants", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte{0xff, 0xff, 0xff}
	})

	_, err := tr.ReconcileBuiltInRolesAllTenants(context.Background())
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v; want UnmarshalError", err)
	}
}

func TestE2E_ReconcileBuiltInRolesAllTenants_ClosedTransport(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.ReconcileBuiltInRolesAllTenants(context.Background())
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}
