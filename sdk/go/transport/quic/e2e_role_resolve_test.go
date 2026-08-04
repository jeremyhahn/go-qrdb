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

// ---------------------------------------------------------------------------
// GetRoleByName
// ---------------------------------------------------------------------------

func TestE2E_GetRoleByName_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)

	srv.SetResponder("role.GetByName", func(req []byte) (uint16, []byte) {
		var wireReq getRoleByNameWireRequest
		_ = json.Unmarshal(req, &wireReq)
		b, _ := json.Marshal(&roleWireResponse{
			ID:          "r-by-name",
			Name:        wireReq.Name,
			TenantID:    wireReq.TenantID,
			Permissions: []string{"kv:read", "kv:write"},
		})
		return 0, b
	})

	rl, err := tr.GetRoleByName(context.Background(), &transport.GetRoleByNameRequest{
		TenantID: "t1",
		Name:     "admin",
	})
	if err != nil {
		t.Fatalf("GetRoleByName: %v", err)
	}
	if rl.ID != "r-by-name" {
		t.Errorf("ID = %q, want r-by-name", rl.ID)
	}
	if rl.Name != "admin" {
		t.Errorf("Name = %q, want admin", rl.Name)
	}

	// Inspect the marshaled wire request through the harness's mutex-protected
	// recorder rather than capturing it in the responder closure: the closure
	// runs in the server goroutine, so a shared capture read here races under
	// the race detector.
	rec := srv.LastRequest("role.GetByName")
	if rec == nil {
		t.Fatal("no recorded role.GetByName request")
	}
	var got getRoleByNameWireRequest
	if err := json.Unmarshal(rec.Payload, &got); err != nil {
		t.Fatalf("unmarshal recorded request: %v", err)
	}
	if got.TenantID != "t1" {
		t.Errorf("wire tenantID = %q, want t1", got.TenantID)
	}
	if got.Name != "admin" {
		t.Errorf("wire name = %q, want admin", got.Name)
	}
}

func TestE2E_GetRoleByName_ClosedTransport(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.GetRoleByName(context.Background(), &transport.GetRoleByNameRequest{
		TenantID: "t1",
		Name:     "admin",
	})
	assertClosed(t, err)
}

// ---------------------------------------------------------------------------
// ResolvePermissions
// ---------------------------------------------------------------------------

func TestE2E_ResolvePermissions_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)

	srv.SetResponder("role.ResolvePermissions", func(_ []byte) (uint16, []byte) {
		b, _ := json.Marshal(&resolvePermissionsWireResponse{
			Permissions: []string{"kv:read", "kv:write", "kv:delete"},
		})
		return wire.StatusOK, b
	})

	resp, err := tr.ResolvePermissions(context.Background(), &transport.ResolvePermissionsRequest{
		RoleIDs: []string{"r1", "r2"},
	})
	if err != nil {
		t.Fatalf("ResolvePermissions: %v", err)
	}
	if len(resp.Permissions) != 3 {
		t.Fatalf("Permissions len = %d, want 3: %v", len(resp.Permissions), resp.Permissions)
	}

	// Inspect the marshaled wire request through the harness's mutex-protected
	// recorder rather than capturing it in the responder closure (which races
	// with this read under -race).
	rec := srv.LastRequest("role.ResolvePermissions")
	if rec == nil {
		t.Fatal("no recorded role.ResolvePermissions request")
	}
	var wr resolvePermissionsWireRequest
	if err := json.Unmarshal(rec.Payload, &wr); err != nil {
		t.Fatalf("unmarshal recorded request: %v", err)
	}
	if len(wr.RoleIDs) != 2 || wr.RoleIDs[0] != "r1" || wr.RoleIDs[1] != "r2" {
		t.Errorf("wire roleIDs = %v, want [r1 r2]", wr.RoleIDs)
	}
}

func TestE2E_ResolvePermissions_ClosedTransport(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.ResolvePermissions(context.Background(), &transport.ResolvePermissionsRequest{
		RoleIDs: []string{"r1"},
	})
	assertClosed(t, err)
}
