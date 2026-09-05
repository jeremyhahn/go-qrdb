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
	"context"
	"errors"
	"io"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// UserNotFoundError
// ---------------------------------------------------------------------------

func TestUserNotFoundError_Message(t *testing.T) {
	err := &UserNotFoundError{ID: 12345}
	want := "user: not found: 12345"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestUserNotFoundError_LookupMessage(t *testing.T) {
	err := &UserNotFoundError{Lookup: "acme/alice"}
	want := "user: not found: acme/alice"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestUserNotFoundError_As(t *testing.T) {
	var err error = &UserNotFoundError{ID: 42}
	var target *UserNotFoundError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *UserNotFoundError")
	}
	if target.ID != 42 {
		t.Fatalf("ID: got %d, want %d", target.ID, 42)
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
	var err error = &UsernameExistsError{Username: "bob", TenantID: "system"}
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
	err := &UserDisabledError{ID: 1}
	want := "user: disabled: 1"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestUserDisabledError_As(t *testing.T) {
	var err error = &UserDisabledError{ID: 2}
	var target *UserDisabledError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *UserDisabledError")
	}
	if target.ID != 2 {
		t.Fatalf("ID: got %d, want %d", target.ID, 2)
	}
}

// ---------------------------------------------------------------------------
// SystemUserImmutableError
// ---------------------------------------------------------------------------

func TestSystemUserImmutableError_Message(t *testing.T) {
	err := &SystemUserImmutableError{ID: 7}
	want := "user: system user cannot be deleted: 7"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestSystemUserImmutableError_As(t *testing.T) {
	var err error = &SystemUserImmutableError{ID: 8}
	var target *SystemUserImmutableError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *SystemUserImmutableError")
	}
	if target.ID != 8 {
		t.Fatalf("ID: got %d, want %d", target.ID, 8)
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

// TestUserNotFoundError_IsTheSDKTransportType pins go-qrdb#343: the service's
// not-found IS the SDK's, so a servicer consumer that imports only sdk/go
// classifies it with the exported type. The compile-time assignment is the
// contract; the errors.As through a real store is the proof it holds for
// what the store actually returns.
func TestUserNotFoundError_IsTheSDKTransportType(t *testing.T) {
	var _ *transport.UserNotFoundError = (*UserNotFoundError)(nil)

	svc := newTestStore(t)
	_, err := svc.GetUserByEmail(context.Background(), "acme", "nobody@acme.example")
	if err == nil {
		t.Fatal("GetUserByEmail on a missing user returned nil")
	}
	var viaTransport *transport.UserNotFoundError
	if !errors.As(err, &viaTransport) {
		t.Fatalf("errors.As(err, *transport.UserNotFoundError) = false for %T: %v", err, err)
	}
	if viaTransport.Lookup != "acme/nobody@acme.example" {
		t.Errorf("Lookup = %q", viaTransport.Lookup)
	}
}
