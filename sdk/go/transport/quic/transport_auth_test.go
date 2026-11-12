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

// ---------------------------------------------------------------------------
// Closed state tests for Role methods
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Closed state tests for Cert methods
// ---------------------------------------------------------------------------

func TestTransport_IssueCert_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.IssueCert(ctx, &transport.IssueCertRequest{Username: "test"})
	if err == nil {
		t.Fatal("IssueCert() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_EnrollCert_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.EnrollCert(ctx, &transport.EnrollCertRequest{Username: "test"})
	if err == nil {
		t.Fatal("EnrollCert() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_AddTrustAnchors_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.AddTrustAnchors(ctx, &transport.AddTrustAnchorsRequest{TenantID: "t1"})
	if err == nil {
		t.Fatal("AddTrustAnchors() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_ListTrustAnchors_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.ListTrustAnchors(ctx, "t1")
	if err == nil {
		t.Fatal("ListTrustAnchors() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_RemoveTrustAnchor_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	err = tr.RemoveTrustAnchor(ctx, "t1", "fp1")
	if err == nil {
		t.Fatal("RemoveTrustAnchor() on closed transport should return error")
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
