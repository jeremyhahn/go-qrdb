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

	var gotTenantID, gotName string
	srv.SetResponder("role.GetByName", func(req []byte) (uint16, []byte) {
		var wire getRoleByNameWireRequest
		_ = json.Unmarshal(req, &wire)
		gotTenantID = wire.TenantID
		gotName = wire.Name
		b, _ := json.Marshal(&roleWireResponse{
			ID:          "r-by-name",
			Name:        wire.Name,
			TenantID:    wire.TenantID,
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
	if gotTenantID != "t1" {
		t.Errorf("wire tenantID = %q, want t1", gotTenantID)
	}
	if gotName != "admin" {
		t.Errorf("wire name = %q, want admin", gotName)
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

	var gotRoleIDs []string
	srv.SetResponder("role.ResolvePermissions", func(req []byte) (uint16, []byte) {
		var wr resolvePermissionsWireRequest
		_ = json.Unmarshal(req, &wr)
		gotRoleIDs = wr.RoleIDs
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
	if len(gotRoleIDs) != 2 || gotRoleIDs[0] != "r1" || gotRoleIDs[1] != "r2" {
		t.Errorf("wire roleIDs = %v, want [r1 r2]", gotRoleIDs)
	}
}

func TestE2E_ResolvePermissions_ClosedTransport(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.ResolvePermissions(context.Background(), &transport.ResolvePermissionsRequest{
		RoleIDs: []string{"r1"},
	})
	assertClosed(t, err)
}
