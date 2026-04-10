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

package role

import (
	"errors"
	"io"
	"testing"
)

// ---------------------------------------------------------------------------
// RoleNotFoundError
// ---------------------------------------------------------------------------

func TestRoleNotFoundError_Message(t *testing.T) {
	err := &RoleNotFoundError{ID: "abc-123"}
	want := "role: not found: abc-123"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestRoleNotFoundError_As(t *testing.T) {
	var err error = &RoleNotFoundError{ID: "xyz"}
	var target *RoleNotFoundError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *RoleNotFoundError")
	}
	if target.ID != "xyz" {
		t.Fatalf("ID: got %q, want %q", target.ID, "xyz")
	}
}

// ---------------------------------------------------------------------------
// RoleNameExistsError
// ---------------------------------------------------------------------------

func TestRoleNameExistsError_Message(t *testing.T) {
	err := &RoleNameExistsError{Name: "admin", TenantID: "acme"}
	want := "role: name already exists: admin in tenant acme"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestRoleNameExistsError_As(t *testing.T) {
	var err error = &RoleNameExistsError{Name: "editor", TenantID: "default"}
	var target *RoleNameExistsError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *RoleNameExistsError")
	}
	if target.Name != "editor" {
		t.Fatalf("Name: got %q, want %q", target.Name, "editor")
	}
}

// ---------------------------------------------------------------------------
// SystemRoleImmutableError
// ---------------------------------------------------------------------------

func TestSystemRoleImmutableError_Message(t *testing.T) {
	err := &SystemRoleImmutableError{ID: "superadmin-id"}
	want := "role: system role cannot be deleted: superadmin-id"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestSystemRoleImmutableError_As(t *testing.T) {
	var err error = &SystemRoleImmutableError{ID: "admin-id"}
	var target *SystemRoleImmutableError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *SystemRoleImmutableError")
	}
	if target.ID != "admin-id" {
		t.Fatalf("ID: got %q, want %q", target.ID, "admin-id")
	}
}

// ---------------------------------------------------------------------------
// StoreError
// ---------------------------------------------------------------------------

func TestStoreError_MessageFull(t *testing.T) {
	err := &StoreError{Op: "create", Detail: "primary key", Err: io.EOF}
	want := "role: store create failed: primary key: EOF"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestStoreError_MessageNoDetail(t *testing.T) {
	err := &StoreError{Op: "get", Err: io.ErrUnexpectedEOF}
	want := "role: store get failed: unexpected EOF"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestStoreError_MessageNoErr(t *testing.T) {
	err := &StoreError{Op: "new", Detail: "kvstore must not be nil"}
	want := "role: store new failed: kvstore must not be nil"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestStoreError_Unwrap(t *testing.T) {
	inner := io.ErrClosedPipe
	err := &StoreError{Op: "put", Err: inner}
	if !errors.Is(err, inner) {
		t.Fatal("Unwrap should allow errors.Is to match inner error")
	}
}

func TestStoreError_UnwrapNil(t *testing.T) {
	err := &StoreError{Op: "put"}
	if err.Unwrap() != nil {
		t.Fatal("Unwrap should return nil when Err is nil")
	}
}

func TestStoreError_As(t *testing.T) {
	var err error = &StoreError{Op: "delete", Detail: "index", Err: io.EOF}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *StoreError")
	}
	if target.Op != "delete" {
		t.Fatalf("Op: got %q, want %q", target.Op, "delete")
	}
}

// ---------------------------------------------------------------------------
// ValidationError
// ---------------------------------------------------------------------------

func TestValidationError_Message(t *testing.T) {
	err := &ValidationError{Field: "name", Reason: "must not be empty"}
	want := "role: validation failed: name: must not be empty"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestValidationError_As(t *testing.T) {
	var err error = &ValidationError{Field: "id", Reason: "required"}
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *ValidationError")
	}
	if target.Field != "id" {
		t.Fatalf("Field: got %q, want %q", target.Field, "id")
	}
}

// ---------------------------------------------------------------------------
// InvalidPermissionError
// ---------------------------------------------------------------------------

func TestInvalidPermissionError_Message(t *testing.T) {
	err := &InvalidPermissionError{Permission: "fake:perm"}
	want := "role: invalid permission: fake:perm"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestInvalidPermissionError_As(t *testing.T) {
	var err error = &InvalidPermissionError{Permission: "bad:action"}
	var target *InvalidPermissionError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *InvalidPermissionError")
	}
	if target.Permission != "bad:action" {
		t.Fatalf("Permission: got %q, want %q", target.Permission, "bad:action")
	}
}
