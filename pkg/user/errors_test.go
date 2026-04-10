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

package user

import (
	"errors"
	"io"
	"testing"
)

// ---------------------------------------------------------------------------
// UserNotFoundError
// ---------------------------------------------------------------------------

func TestUserNotFoundError_Message(t *testing.T) {
	err := &UserNotFoundError{ID: "abc-123"}
	want := "user: not found: abc-123"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestUserNotFoundError_As(t *testing.T) {
	var err error = &UserNotFoundError{ID: "xyz"}
	var target *UserNotFoundError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *UserNotFoundError")
	}
	if target.ID != "xyz" {
		t.Fatalf("ID: got %q, want %q", target.ID, "xyz")
	}
}

// ---------------------------------------------------------------------------
// UsernameExistsError
// ---------------------------------------------------------------------------

func TestUsernameExistsError_Message(t *testing.T) {
	err := &UsernameExistsError{Username: "alice", TenantID: "acme"}
	want := "user: username already exists: alice in tenant acme"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestUsernameExistsError_As(t *testing.T) {
	var err error = &UsernameExistsError{Username: "bob", TenantID: "default"}
	var target *UsernameExistsError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *UsernameExistsError")
	}
	if target.Username != "bob" {
		t.Fatalf("Username: got %q, want %q", target.Username, "bob")
	}
}

// ---------------------------------------------------------------------------
// UserDisabledError
// ---------------------------------------------------------------------------

func TestUserDisabledError_Message(t *testing.T) {
	err := &UserDisabledError{ID: "user-1"}
	want := "user: disabled: user-1"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestUserDisabledError_As(t *testing.T) {
	var err error = &UserDisabledError{ID: "user-2"}
	var target *UserDisabledError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *UserDisabledError")
	}
	if target.ID != "user-2" {
		t.Fatalf("ID: got %q, want %q", target.ID, "user-2")
	}
}

// ---------------------------------------------------------------------------
// SystemUserImmutableError
// ---------------------------------------------------------------------------

func TestSystemUserImmutableError_Message(t *testing.T) {
	err := &SystemUserImmutableError{ID: "root"}
	want := "user: system user cannot be deleted: root"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestSystemUserImmutableError_As(t *testing.T) {
	var err error = &SystemUserImmutableError{ID: "admin"}
	var target *SystemUserImmutableError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *SystemUserImmutableError")
	}
	if target.ID != "admin" {
		t.Fatalf("ID: got %q, want %q", target.ID, "admin")
	}
}

// ---------------------------------------------------------------------------
// StoreError
// ---------------------------------------------------------------------------

func TestStoreError_MessageFull(t *testing.T) {
	err := &StoreError{Op: "create", Detail: "primary key", Err: io.EOF}
	want := "user: store create failed: primary key: EOF"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestStoreError_MessageNoDetail(t *testing.T) {
	err := &StoreError{Op: "get", Err: io.ErrUnexpectedEOF}
	want := "user: store get failed: unexpected EOF"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestStoreError_MessageNoErr(t *testing.T) {
	err := &StoreError{Op: "new", Detail: "kvstore must not be nil"}
	want := "user: store new failed: kvstore must not be nil"
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
	err := &ValidationError{Field: "username", Reason: "must not be empty"}
	want := "user: validation failed: username: must not be empty"
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
