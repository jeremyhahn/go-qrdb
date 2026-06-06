// Copyright 2026 Jeremy Hahn
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
	"testing"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// GetRoleByName
// ---------------------------------------------------------------------------

func TestUnixRT_GetRoleByName_Success(t *testing.T) {
	rsvc := &mockRoleSvc{
		getByNameFn: func(_ context.Context, tenantID, name string) (*role.Role, error) {
			if tenantID != "t1" {
				t.Errorf("tenantID = %q, want t1", tenantID)
			}
			if name != "admin" {
				t.Errorf("name = %q, want admin", name)
			}
			return &role.Role{
				ID:       "r-by-name",
				Name:     name,
				TenantID: tenantID,
			}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithRoleService(rsvc))
	defer cleanup()

	rl, err := tr.GetRoleByName(context.Background(), &transport.GetRoleByNameRequest{
		TenantID: "t1",
		Name:     "admin",
	})
	if err != nil {
		t.Fatalf("GetRoleByName: %v", err)
	}
	if rl == nil {
		t.Fatal("expected non-nil role")
	}
	if rl.ID != "r-by-name" {
		t.Errorf("ID = %q, want r-by-name", rl.ID)
	}
	if rl.Name != "admin" {
		t.Errorf("Name = %q, want admin", rl.Name)
	}
	if rl.TenantID != "t1" {
		t.Errorf("TenantID = %q, want t1", rl.TenantID)
	}
}

func TestUnixRT_GetRoleByName_ClosedTransport(t *testing.T) {
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

func TestUnixRT_ResolvePermissions_Success(t *testing.T) {
	rsvc := &mockRoleSvc{
		resolveFn: func(_ context.Context, roleIDs []string) ([]permission.Permission, error) {
			if len(roleIDs) != 2 {
				t.Errorf("roleIDs len = %d, want 2", len(roleIDs))
			}
			return []permission.Permission{"kv:read", "kv:write"}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithRoleService(rsvc))
	defer cleanup()

	resp, err := tr.ResolvePermissions(context.Background(), &transport.ResolvePermissionsRequest{
		RoleIDs: []string{"r1", "r2"},
	})
	if err != nil {
		t.Fatalf("ResolvePermissions: %v", err)
	}
	if len(resp.Permissions) != 2 {
		t.Fatalf("Permissions len = %d, want 2: %v", len(resp.Permissions), resp.Permissions)
	}
}

func TestUnixRT_ResolvePermissions_ClosedTransport(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.ResolvePermissions(context.Background(), &transport.ResolvePermissionsRequest{
		RoleIDs: []string{"r1"},
	})
	assertClosed(t, err)
}

func TestUnixRT_ResolvePermissions_ServiceError(t *testing.T) {
	rsvc := &mockRoleSvc{
		resolveFn: func(_ context.Context, _ []string) ([]permission.Permission, error) {
			return nil, errors.New("backend unavailable")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithRoleService(rsvc))
	defer cleanup()

	_, err := tr.ResolvePermissions(context.Background(), &transport.ResolvePermissionsRequest{
		RoleIDs: []string{"r1"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
