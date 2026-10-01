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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// wireToUser tests
// ---------------------------------------------------------------------------

func TestWireToUser_Nil(t *testing.T) {
	result := wireToUser(nil)
	if result != nil {
		t.Errorf("wireToUser(nil) = %v, want nil", result)
	}
}

func TestWireToUser_Valid(t *testing.T) {
	wire := &userWireResponse{
		ID:        123,
		Username:  "admin",
		TenantID:  "tenant-1",
		RoleIDs:   []string{"role-1", "role-2"},
		Lifecycle: "active",
		System:    false,
		CreatedAt: "2026-01-01T00:00:00Z",
		UpdatedAt: "2026-01-15T12:00:00Z",
	}
	result := wireToUser(wire)
	if result == nil {
		t.Fatal("wireToUser() returned nil for valid input")
	}
	if result.ID != 123 {
		t.Errorf("ID = %d, want %d", result.ID, uint64(123))
	}
	if result.Username != "admin" {
		t.Errorf("Username = %q, want %q", result.Username, "admin")
	}
	if result.TenantID != "tenant-1" {
		t.Errorf("TenantID = %q, want %q", result.TenantID, "tenant-1")
	}
	if len(result.RoleIDs) != 2 {
		t.Fatalf("RoleIDs length = %d, want 2", len(result.RoleIDs))
	}
	if result.RoleIDs[0] != "role-1" {
		t.Errorf("RoleIDs[0] = %q, want %q", result.RoleIDs[0], "role-1")
	}
	if result.Lifecycle != "active" {
		t.Errorf("Lifecycle = %q, want %q", result.Lifecycle, "active")
	}
	if result.System {
		t.Error("System = true, want false")
	}
	if result.CreatedAt != "2026-01-01T00:00:00Z" {
		t.Errorf("CreatedAt = %q, want %q", result.CreatedAt, "2026-01-01T00:00:00Z")
	}
}

func TestWireToUser_EmptyFields(t *testing.T) {
	wire := &userWireResponse{}
	result := wireToUser(wire)
	if result == nil {
		t.Fatal("wireToUser() returned nil for empty wire response")
	}
	if result.ID != 0 {
		t.Errorf("ID = %d, want 0", result.ID)
	}
	if result.Lifecycle != "" {
		t.Errorf("Lifecycle = %q, want empty", result.Lifecycle)
	}
}

// ---------------------------------------------------------------------------
// wireToRole tests
// ---------------------------------------------------------------------------

func TestWireToRole_Nil(t *testing.T) {
	result := wireToRole(nil)
	if result != nil {
		t.Errorf("wireToRole(nil) = %v, want nil", result)
	}
}

func TestWireToRole_Valid(t *testing.T) {
	wire := &roleWireResponse{
		ID:          "role-456",
		Name:        "editor",
		TenantID:    "tenant-2",
		Permissions: []string{"kv.read", "kv.write"},
		System:      true,
		CreatedAt:   "2026-02-01T00:00:00Z",
		UpdatedAt:   "2026-02-10T08:00:00Z",
	}
	result := wireToRole(wire)
	if result == nil {
		t.Fatal("wireToRole() returned nil for valid input")
	}
	if result.ID != "role-456" {
		t.Errorf("ID = %q, want %q", result.ID, "role-456")
	}
	if result.Name != "editor" {
		t.Errorf("Name = %q, want %q", result.Name, "editor")
	}
	if result.TenantID != "tenant-2" {
		t.Errorf("TenantID = %q, want %q", result.TenantID, "tenant-2")
	}
	if len(result.Permissions) != 2 {
		t.Fatalf("Permissions length = %d, want 2", len(result.Permissions))
	}
	if result.Permissions[0] != "kv.read" {
		t.Errorf("Permissions[0] = %q, want %q", result.Permissions[0], "kv.read")
	}
	if !result.System {
		t.Error("System = false, want true")
	}
}

func TestWireToRole_EmptyFields(t *testing.T) {
	wire := &roleWireResponse{}
	result := wireToRole(wire)
	if result == nil {
		t.Fatal("wireToRole() returned nil for empty wire response")
	}
	if result.ID != "" {
		t.Errorf("ID = %q, want empty", result.ID)
	}
	if result.System {
		t.Error("System = true, want false")
	}
}

// ---------------------------------------------------------------------------
// Closed state tests for User methods
// ---------------------------------------------------------------------------

func TestTransport_CreateUser_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.CreateUser(ctx, &transport.CreateUserRequest{WithoutEmailLoginKey: true, Username: "test"})
	if err == nil {
		t.Fatal("CreateUser() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_GetUser_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.GetUser(ctx, 1)
	if err == nil {
		t.Fatal("GetUser() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_UpdateUser_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.UpdateUser(ctx, &transport.UpdateUserRequest{ID: 1})
	if err == nil {
		t.Fatal("UpdateUser() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_DeleteUser_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	err = tr.DeleteUser(ctx, 1)
	if err == nil {
		t.Fatal("DeleteUser() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_ListUsers_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.ListUsers(ctx, &transport.ListUsersRequest{TenantID: "t1"})
	if err == nil {
		t.Fatal("ListUsers() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_EnableUser_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	err = tr.EnableUser(ctx, 1)
	if err == nil {
		t.Fatal("EnableUser() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_DisableUser_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	err = tr.DisableUser(ctx, 1)
	if err == nil {
		t.Fatal("DisableUser() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Closed state tests for Role methods
// ---------------------------------------------------------------------------

func TestTransport_CreateRole_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.CreateRole(ctx, &transport.CreateRoleRequest{Name: "admin"})
	if err == nil {
		t.Fatal("CreateRole() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_GetRole_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.GetRole(ctx, "role-1")
	if err == nil {
		t.Fatal("GetRole() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_UpdateRole_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.UpdateRole(ctx, &transport.UpdateRoleRequest{ID: "role-1"})
	if err == nil {
		t.Fatal("UpdateRole() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_DeleteRole_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	err = tr.DeleteRole(ctx, "role-1")
	if err == nil {
		t.Fatal("DeleteRole() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_ListRoles_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.ListRoles(ctx, &transport.ListRolesRequest{TenantID: "t1"})
	if err == nil {
		t.Fatal("ListRoles() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_AssignRole_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	err = tr.AssignRole(ctx, 1, "role-1")
	if err == nil {
		t.Fatal("AssignRole() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_UnassignRole_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	err = tr.UnassignRole(ctx, 1, "role-1")
	if err == nil {
		t.Fatal("UnassignRole() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_ListPermissions_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.ListPermissions(ctx)
	if err == nil {
		t.Fatal("ListPermissions() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Closed state tests for Cert methods
// ---------------------------------------------------------------------------

func TestTransport_GenerateCert_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.GenerateCert(ctx, &transport.GenerateCertRequest{Username: "test"})
	if err == nil {
		t.Fatal("GenerateCert() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_ExportCert_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.ExportCert(ctx, &transport.ExportCertRequest{CertPEM: "cert"})
	if err == nil {
		t.Fatal("ExportCert() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_RevokeCert_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.RevokeCert(ctx, "serial-123")
	if err == nil {
		t.Fatal("RevokeCert() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_ListCerts_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.ListCerts(ctx, "tenant-1")
	if err == nil {
		t.Fatal("ListCerts() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}
