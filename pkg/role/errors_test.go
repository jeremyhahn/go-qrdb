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
	"fmt"
	"io"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
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

// ---------------------------------------------------------------------------
// SystemRoleImmutableError
// ---------------------------------------------------------------------------

func TestSystemRoleImmutableError_Message(t *testing.T) {
	tests := []struct {
		name string
		err  *SystemRoleImmutableError
		want string
	}{
		{
			name: "delete",
			err:  &SystemRoleImmutableError{ID: "admin-role-id", Op: "deleted"},
			want: "role: system role cannot be deleted: admin-role-id",
		},
		{
			name: "rename",
			err:  &SystemRoleImmutableError{ID: "admin-role-id", Op: "renamed"},
			want: "role: system role cannot be renamed: admin-role-id",
		},
		{
			// A zero Op must still read as a sentence rather than losing the
			// verb: the type is constructed in two places today and a third
			// that forgets Op should degrade, not produce "cannot be : id".
			name: "unset op falls back",
			err:  &SystemRoleImmutableError{ID: "admin-role-id"},
			want: "role: system role cannot be modified: admin-role-id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Fatalf("Error(): got %q, want %q", got, tt.want)
			}
		})
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

func TestStoreError_UnwrapNil(t *testing.T) {
	err := &StoreError{Op: "put"}
	if err.Unwrap() != nil {
		t.Fatal("Unwrap should return nil when Err is nil")
	}
}

// ---------------------------------------------------------------------------
// StoreError.ReadTransient / newStoreError / isTransientCause (BL2's
// producer-declares-transience marker -- see the doc comments on StoreError,
// newStoreError and isTransientCause).
// ---------------------------------------------------------------------------

// TestStoreError_ReadTransient_DefaultsFalse pins the zero value: a
// composite literal built outside newStoreError -- the only shape anything
// outside this package can construct, since transient is unexported -- can
// never assert its own transience.
func TestStoreError_ReadTransient_DefaultsFalse(t *testing.T) {
	err := &StoreError{Op: "get", Detail: "unmarshal", Err: io.EOF}
	if err.ReadTransient() {
		t.Fatal("a bare composite literal must read ReadTransient() = false; only newStoreError sets it true")
	}
}

// TestNewStoreError_NonDecryptCauseIsTransient covers an ordinary read
// failure (ReadIndex not ready, leader lost, timeout) -- not a decrypt
// refusal -- which newStoreError must mark transient.
func TestNewStoreError_NonDecryptCauseIsTransient(t *testing.T) {
	cause := errors.New("read index not ready")
	err := newStoreError("get", "primary lookup", cause)
	if !err.ReadTransient() {
		t.Fatal("a non-decrypt cause must classify as transient")
	}
	if !errors.Is(err, cause) {
		t.Fatal("newStoreError must still wrap cause for errors.Is")
	}
}

// TestNewStoreError_DecryptCauseIsNotTransient is N1: a sealed-barrier-backed
// store's decrypt/authentication refusal is definitive, because no retry
// decrypts a tampered ciphertext or authenticates with the wrong key.
func TestNewStoreError_DecryptCauseIsNotTransient(t *testing.T) {
	err := newStoreError("get", "primary lookup", seal.ErrDecryptionFailed)
	if err.ReadTransient() {
		t.Fatal("a decrypt-failure cause must classify as definitive (ReadTransient() = false)")
	}
}

// TestNewStoreError_WrappedDecryptCauseIsNotTransient: isTransientCause must
// find seal.ErrDecryptionFailed through a wrapper, not only when it is the
// cause verbatim -- errors.Is, not ==.
func TestNewStoreError_WrappedDecryptCauseIsNotTransient(t *testing.T) {
	wrapped := fmt.Errorf("decrypt tenant record: %w", seal.ErrDecryptionFailed)
	err := newStoreError("get", "primary lookup", wrapped)
	if err.ReadTransient() {
		t.Fatal("a wrapped decrypt-failure cause must still classify as definitive")
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

// ---------------------------------------------------------------------------
// ServiceRequiredError
// ---------------------------------------------------------------------------

func TestServiceRequiredError_Message(t *testing.T) {
	err := &ServiceRequiredError{}
	want := "role: a role service is required to validate the referenced role(s), and none was configured"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// RoleIDExistsError
// ---------------------------------------------------------------------------

func TestRoleIDExistsError_Message(t *testing.T) {
	err := &RoleIDExistsError{ID: "role-123", TenantID: "acme"}
	want := "role: id already exists: role-123 in tenant acme"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// ReservedRoleNameError
// ---------------------------------------------------------------------------

func TestReservedRoleNameError_Message(t *testing.T) {
	tests := []struct {
		name string
		err  *ReservedRoleNameError
		want string
	}{
		{
			name: "with detail",
			err:  &ReservedRoleNameError{Name: "admin", TenantID: "acme", Detail: "wrong id"},
			want: "role: reserved system role name: admin in tenant acme: wrong id",
		},
		{
			name: "without detail",
			err:  &ReservedRoleNameError{Name: "admin", TenantID: "acme"},
			want: "role: reserved system role name: admin in tenant acme",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Fatalf("Error(): got %q, want %q", got, tt.want)
			}
		})
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
