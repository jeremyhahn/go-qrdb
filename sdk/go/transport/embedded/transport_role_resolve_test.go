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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// GetRoleByName
// ---------------------------------------------------------------------------

func TestGetRoleByName_Success(t *testing.T) {
	mock := &mockRoleServicer{
		getRoleByNameFn: func(_ context.Context, tenantID, name string) (*role.Role, error) {
			if tenantID != "tenant-a" {
				t.Errorf("expected tenantID 'tenant-a', got %q", tenantID)
			}
			if name != "admin" {
				t.Errorf("expected name 'admin', got %q", name)
			}
			return testRole(), nil
		},
	}
	tr := newAuthTransport(t, WithRoleService(mock))

	rl, err := tr.GetRoleByName(context.Background(), &transport.GetRoleByNameRequest{
		TenantID: "tenant-a",
		Name:     "admin",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rl.ID != "role-001" {
		t.Errorf("expected ID 'role-001', got %q", rl.ID)
	}
	if rl.Name != "admin" {
		t.Errorf("expected Name 'admin', got %q", rl.Name)
	}
	if rl.TenantID != "tenant-a" {
		t.Errorf("expected TenantID 'tenant-a', got %q", rl.TenantID)
	}
}

func TestGetRoleByName_NilService(t *testing.T) {
	tr := newAuthTransport(t)
	_, err := tr.GetRoleByName(context.Background(), &transport.GetRoleByNameRequest{
		TenantID: "tenant-a",
		Name:     "admin",
	})
	assertNotImplementedError(t, err, "GetRoleByName")
}

func TestGetRoleByName_ServiceError(t *testing.T) {
	mock := &mockRoleServicer{
		getRoleByNameFn: func(_ context.Context, _, name string) (*role.Role, error) {
			return nil, &role.RoleNotFoundError{ID: name}
		},
	}
	tr := newAuthTransport(t, WithRoleService(mock))

	_, err := tr.GetRoleByName(context.Background(), &transport.GetRoleByNameRequest{
		TenantID: "tenant-a",
		Name:     "missing",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var rnf *role.RoleNotFoundError
	if !errors.As(err, &rnf) {
		t.Errorf("expected *role.RoleNotFoundError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// ResolvePermissions
// ---------------------------------------------------------------------------

func TestResolvePermissions_Success(t *testing.T) {
	mock := &mockRoleServicer{
		resolvePermsFn: func(_ context.Context, roleIDs []string) ([]permission.Permission, error) {
			if len(roleIDs) != 2 || roleIDs[0] != "role-001" || roleIDs[1] != "role-002" {
				t.Errorf("unexpected roleIDs: %v", roleIDs)
			}
			return []permission.Permission{"kv:put", "kv:get", "kv:delete"}, nil
		},
	}
	tr := newAuthTransport(t, WithRoleService(mock))

	resp, err := tr.ResolvePermissions(context.Background(), &transport.ResolvePermissionsRequest{
		RoleIDs: []string{"role-001", "role-002"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Permissions) != 3 {
		t.Fatalf("expected 3 permissions, got %d: %v", len(resp.Permissions), resp.Permissions)
	}
	want := map[string]bool{"kv:put": true, "kv:get": true, "kv:delete": true}
	for _, p := range resp.Permissions {
		if !want[p] {
			t.Errorf("unexpected permission %q", p)
		}
	}
}

func TestResolvePermissions_NilService(t *testing.T) {
	tr := newAuthTransport(t)
	_, err := tr.ResolvePermissions(context.Background(), &transport.ResolvePermissionsRequest{
		RoleIDs: []string{"role-001"},
	})
	assertNotImplementedError(t, err, "ResolvePermissions")
}

func TestResolvePermissions_ServiceError(t *testing.T) {
	mock := &mockRoleServicer{
		resolvePermsFn: func(_ context.Context, _ []string) ([]permission.Permission, error) {
			return nil, errors.New("backend unavailable")
		},
	}
	tr := newAuthTransport(t, WithRoleService(mock))

	_, err := tr.ResolvePermissions(context.Background(), &transport.ResolvePermissionsRequest{
		RoleIDs: []string{"role-001"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestResolvePermissions_EmptyResult(t *testing.T) {
	mock := &mockRoleServicer{
		resolvePermsFn: func(_ context.Context, _ []string) ([]permission.Permission, error) {
			return nil, nil
		},
	}
	tr := newAuthTransport(t, WithRoleService(mock))

	resp, err := tr.ResolvePermissions(context.Background(), &transport.ResolvePermissionsRequest{
		RoleIDs: []string{},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Permissions) != 0 {
		t.Errorf("expected empty permissions, got %v", resp.Permissions)
	}
}
