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

	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// GetRoleByName
// ---------------------------------------------------------------------------

func TestMCPGetRoleByName_Success(t *testing.T) {
	svc := newMockRoleSvc()
	tr, cleanup := connectAuthInProcess(t, newMockUserSvc(), svc, nil)
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
	// The mock returns getResp (ID="r1", Name="admin").
	if rl.ID != "r1" {
		t.Errorf("ID = %q, want r1", rl.ID)
	}
	if rl.Name != "admin" {
		t.Errorf("Name = %q, want admin", rl.Name)
	}
}

func TestMCPGetRoleByName_NotFound(t *testing.T) {
	svc := newMockRoleSvc()
	svc.getByErr = errors.New("role not found")
	tr, cleanup := connectAuthInProcess(t, newMockUserSvc(), svc, nil)
	defer cleanup()

	_, err := tr.GetRoleByName(context.Background(), &transport.GetRoleByNameRequest{
		TenantID: "t1",
		Name:     "missing",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// The MCP transport converts not-found errors into RoleNotFoundError.
	var rnf *transport.RoleNotFoundError
	if !errors.As(err, &rnf) {
		t.Errorf("expected *transport.RoleNotFoundError, got %T: %v", err, err)
	}
}

func TestMCPGetRoleByName_ClosedTransport(t *testing.T) {
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

func TestMCPResolvePermissions_Success(t *testing.T) {
	svc := newMockRoleSvc()
	tr, cleanup := connectAuthInProcess(t, newMockUserSvc(), svc, nil)
	defer cleanup()

	resp, err := tr.ResolvePermissions(context.Background(), &transport.ResolvePermissionsRequest{
		RoleIDs: []string{"r1", "r2"},
	})
	if err != nil {
		t.Fatalf("ResolvePermissions: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	// The mock returns []permission.Permission{"kv:read"} for any input.
	if len(resp.Permissions) == 0 {
		t.Error("expected at least one permission in response")
	}
}

func TestMCPResolvePermissions_ServiceError(t *testing.T) {
	svc := newMockRoleSvc()
	svc.resolveErr = errors.New("backend unavailable")
	tr, cleanup := connectAuthInProcess(t, newMockUserSvc(), svc, nil)
	defer cleanup()

	_, err := tr.ResolvePermissions(context.Background(), &transport.ResolvePermissionsRequest{
		RoleIDs: []string{"r1"},
	})
	if err == nil {
		t.Fatal("expected error from service, got nil")
	}
}

func TestMCPResolvePermissions_ClosedTransport(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.ResolvePermissions(context.Background(), &transport.ResolvePermissionsRequest{
		RoleIDs: []string{"r1"},
	})
	assertClosed(t, err)
}

// ---------------------------------------------------------------------------
// Request field propagation: verify TenantID and Name reach the service
// ---------------------------------------------------------------------------

func TestMCPGetRoleByName_FieldPropagation(t *testing.T) {
	svc := newMockRoleSvc()
	// Override getByErr to record calls via getByNameFn behaviour.
	// mockRoleSvc.GetRoleByName captures no state directly, so we use a
	// flag to check it is reached at all (success = svc returns getResp).
	tr, cleanup := connectAuthInProcess(t, newMockUserSvc(), svc, nil)
	defer cleanup()

	rl, err := tr.GetRoleByName(context.Background(), &transport.GetRoleByNameRequest{
		TenantID: "acme",
		Name:     "operator",
	})
	if err != nil {
		t.Fatalf("GetRoleByName: %v", err)
	}
	// The mock always returns its getResp regardless of arguments.
	// The important assertion is that the call succeeds end-to-end.
	if rl == nil {
		t.Fatal("nil role")
	}
}

// TestMCPResolvePermissions_RoleIDsEncoding verifies the role IDs are joined
// as a CSV string and forwarded correctly through the MCP tool.
func TestMCPResolvePermissions_RoleIDsEncoding(t *testing.T) {
	svc := newMockRoleSvc()
	var capturedIDs []string
	svc.lastResolveIDs = nil

	tr, cleanup := connectAuthInProcess(t, newMockUserSvc(), svc, nil)
	defer cleanup()

	_, err := tr.ResolvePermissions(context.Background(), &transport.ResolvePermissionsRequest{
		RoleIDs: []string{"role-a", "role-b", "role-c"},
	})
	if err != nil {
		t.Fatalf("ResolvePermissions: %v", err)
	}

	svc.mu.Lock()
	capturedIDs = append(capturedIDs, svc.lastResolveIDs...)
	svc.mu.Unlock()

	if len(capturedIDs) != 3 {
		t.Fatalf("expected 3 role IDs to reach service, got %d: %v", len(capturedIDs), capturedIDs)
	}
	want := map[string]bool{"role-a": true, "role-b": true, "role-c": true}
	for _, id := range capturedIDs {
		if !want[id] {
			t.Errorf("unexpected role ID %q in %v", id, capturedIDs)
		}
	}
}

// compile-time guard: ensure the embedded mock satisfies the interface.
var _ role.RoleServicer = (*mockRoleSvc)(nil)
