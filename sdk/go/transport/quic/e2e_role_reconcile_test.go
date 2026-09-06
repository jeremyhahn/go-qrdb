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

package quic

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestE2E_ReconcileBuiltInRoles_HappyPath proves the request reaches the
// role.ReconcileBuiltIn method carrying the tenant, and the report comes back
// intact over the QUIC frame codec.
func TestE2E_ReconcileBuiltInRoles_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)

	srv.SetResponder("role.ReconcileBuiltIn", func(req []byte) (uint16, []byte) {
		var wireReq reconcileRolesWireRequest
		_ = json.Unmarshal(req, &wireReq)
		b, _ := json.Marshal(&reconcileRolesWireResponse{
			TenantID: wireReq.TenantID,
			Roles: []roleReconcileResultWire{
				{
					RoleID:  "r-admin",
					Name:    "admin",
					Added:   []string{"sdlc:bind"},
					Removed: []string{"sdlc:register"},
				},
				{
					RoleID:  "r-operator",
					Name:    "operator",
					Created: true,
					Added:   []string{"orchestrator:task_block"},
				},
			},
		})
		return wire.StatusOK, b
	})

	resp, err := tr.ReconcileBuiltInRoles(context.Background(), &transport.ReconcileRolesRequest{TenantID: "t1"})
	if err != nil {
		t.Fatalf("ReconcileBuiltInRoles: %v", err)
	}
	if resp.TenantID != "t1" {
		t.Errorf("TenantID = %q, want t1", resp.TenantID)
	}
	if len(resp.Roles) != 2 {
		t.Fatalf("Roles len = %d, want 2: %+v", len(resp.Roles), resp.Roles)
	}
	if len(resp.Roles[0].Added) != 1 || resp.Roles[0].Added[0] != "sdlc:bind" {
		t.Errorf("admin added = %v, want [sdlc:bind]", resp.Roles[0].Added)
	}
	if len(resp.Roles[0].Removed) != 1 || resp.Roles[0].Removed[0] != "sdlc:register" {
		t.Errorf("admin removed = %v, want [sdlc:register]", resp.Roles[0].Removed)
	}
	if !resp.Roles[1].Created {
		t.Error("operator not reported as created")
	}

	// Inspect the marshaled wire request through the harness's mutex-protected
	// recorder rather than capturing it in the responder closure, which runs in
	// the server goroutine and would race this read under -race.
	rec := srv.LastRequest("role.ReconcileBuiltIn")
	if rec == nil {
		t.Fatal("no recorded role.ReconcileBuiltIn request")
	}
	var got reconcileRolesWireRequest
	if err := json.Unmarshal(rec.Payload, &got); err != nil {
		t.Fatalf("unmarshal recorded request: %v", err)
	}
	if got.TenantID != "t1" {
		t.Errorf("wire tenantID = %q, want t1", got.TenantID)
	}
}

// TestE2E_ReconcileBuiltInRoles_NilRequest covers the "my own tenant" call.
func TestE2E_ReconcileBuiltInRoles_NilRequest(t *testing.T) {
	srv, tr := startE2EServer(t)

	srv.SetResponder("role.ReconcileBuiltIn", func(_ []byte) (uint16, []byte) {
		b, _ := json.Marshal(&reconcileRolesWireResponse{TenantID: "own"})
		return wire.StatusOK, b
	})

	resp, err := tr.ReconcileBuiltInRoles(context.Background(), nil)
	if err != nil {
		t.Fatalf("ReconcileBuiltInRoles(nil): %v", err)
	}
	if resp.TenantID != "own" {
		t.Errorf("TenantID = %q, want own", resp.TenantID)
	}
	if len(resp.Roles) != 0 {
		t.Errorf("Roles = %+v, want empty", resp.Roles)
	}
}

// TestE2E_ReconcileBuiltInRoles_MalformedResponse covers a server answer the
// client cannot decode.
func TestE2E_ReconcileBuiltInRoles_MalformedResponse(t *testing.T) {
	srv, tr := startE2EServer(t)

	srv.SetResponder("role.ReconcileBuiltIn", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte("{not json")
	})

	if _, err := tr.ReconcileBuiltInRoles(context.Background(), &transport.ReconcileRolesRequest{TenantID: "t1"}); err == nil {
		t.Fatal("expected an unmarshal error, got nil")
	}
}

func TestE2E_ReconcileBuiltInRoles_ClosedTransport(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.ReconcileBuiltInRoles(context.Background(), &transport.ReconcileRolesRequest{TenantID: "t1"})
	assertClosed(t, err)
}

// TestQUICWireToReconcileReport_Nil covers the nil guard in the converter.
func TestQUICWireToReconcileReport_Nil(t *testing.T) {
	if got := wireToReconcileReport(nil); got != nil {
		t.Errorf("wireToReconcileReport(nil) = %+v, want nil", got)
	}
}
