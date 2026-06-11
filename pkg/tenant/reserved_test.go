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

package tenant

import (
	"context"
	"errors"
	"testing"
)

// TestValidateReservedTenantID_System verifies the system tenant id is
// rejected for external callers.
func TestValidateReservedTenantID_System(t *testing.T) {
	err := validateReservedTenantID(context.Background(), SystemTenantID)
	var rerr *ReservedTenantIDError
	if !errors.As(err, &rerr) {
		t.Fatalf("expected *ReservedTenantIDError, got %T: %v", err, err)
	}
	if rerr.TenantID != SystemTenantID {
		t.Errorf("TenantID = %q, want %q", rerr.TenantID, SystemTenantID)
	}
}

// TestValidateReservedTenantID_LegacyDefault verifies the legacy "default"
// tenant id is permanently reserved.
func TestValidateReservedTenantID_LegacyDefault(t *testing.T) {
	err := validateReservedTenantID(context.Background(), "default")
	var rerr *ReservedTenantIDError
	if !errors.As(err, &rerr) {
		t.Fatalf("expected *ReservedTenantIDError, got %T: %v", err, err)
	}
}

// TestValidateReservedTenantID_UnderscorePrefix verifies ids starting with
// "_" are rejected.
func TestValidateReservedTenantID_UnderscorePrefix(t *testing.T) {
	cases := []string{"_internal", "_acme", "_"}
	for _, id := range cases {
		t.Run(id, func(t *testing.T) {
			err := validateReservedTenantID(context.Background(), id)
			var rerr *ReservedTenantIDError
			if !errors.As(err, &rerr) {
				t.Fatalf("id %q: expected *ReservedTenantIDError, got %T: %v", id, err, err)
			}
		})
	}
}

// TestValidateReservedTenantID_ColonInfix verifies ids containing ":" are
// rejected.
func TestValidateReservedTenantID_ColonInfix(t *testing.T) {
	cases := []string{"acme:prod", "scope:value", ":leading"}
	for _, id := range cases {
		t.Run(id, func(t *testing.T) {
			err := validateReservedTenantID(context.Background(), id)
			var rerr *ReservedTenantIDError
			if !errors.As(err, &rerr) {
				t.Fatalf("id %q: expected *ReservedTenantIDError, got %T: %v", id, err, err)
			}
		})
	}
}

// TestValidateReservedTenantID_OrdinaryID verifies non-reserved ids pass.
func TestValidateReservedTenantID_OrdinaryID(t *testing.T) {
	cases := []string{"acme", "tenant-1", "user_tenant", "abc-123"}
	for _, id := range cases {
		t.Run(id, func(t *testing.T) {
			if err := validateReservedTenantID(context.Background(), id); err != nil {
				t.Errorf("id %q: unexpected error %v", id, err)
			}
		})
	}
}

// TestValidateReservedTenantID_InternalCallBypass verifies that contexts
// marked via WithInternalCall bypass every reserved-id check except for
// the charset validation which applies unconditionally.
func TestValidateReservedTenantID_InternalCallBypass(t *testing.T) {
	ctx := WithInternalCall(context.Background())
	cases := []string{SystemTenantID, "default", "_internal", "acme:prod"}
	for _, id := range cases {
		t.Run(id, func(t *testing.T) {
			if err := validateReservedTenantID(ctx, id); err != nil {
				t.Errorf("id %q with internal-call ctx: unexpected error %v", id, err)
			}
		})
	}
}

// TestValidateReservedTenantID_RejectsControlBytes verifies that tenant IDs
// containing control characters (bytes < 0x20) are rejected for all callers,
// including internal calls, because they would corrupt scoped storage keys.
func TestValidateReservedTenantID_RejectsControlBytes(t *testing.T) {
	cases := []struct {
		name string
		id   string
	}{
		{"unit separator (namespaceSep)", "acme\x1fuser"},
		{"null byte", "acme\x00user"},
		{"newline", "acme\nuser"},
		{"tab", "acme\tuser"},
		{"carriage return", "acme\ruser"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Reject for normal callers.
			err := validateReservedTenantID(context.Background(), tc.id)
			if err == nil {
				t.Fatalf("id %q: expected error, got nil", tc.id)
			}
			var charErr *InvalidTenantIDCharError
			if !errors.As(err, &charErr) {
				t.Fatalf("id %q: expected *InvalidTenantIDCharError, got %T: %v", tc.id, err, err)
			}

			// Also reject for internal callers — charset check is unconditional.
			ctx := WithInternalCall(context.Background())
			err = validateReservedTenantID(ctx, tc.id)
			if err == nil {
				t.Fatalf("id %q (internal): expected error, got nil", tc.id)
			}
			if !errors.As(err, &charErr) {
				t.Fatalf("id %q (internal): expected *InvalidTenantIDCharError, got %T: %v", tc.id, err, err)
			}
		})
	}
}

// TestValidateReservedTenantID_RejectsPathSeparators verifies that tenant IDs
// containing path separators are rejected for all callers, including internal
// calls — tenant ids become filesystem path segments under barrier isolation,
// so a traversal id must never reach a filepath.Join sink.
func TestValidateReservedTenantID_RejectsPathSeparators(t *testing.T) {
	cases := []struct {
		name string
		id   string
	}{
		{"forward slash traversal", "../../etc"},
		{"forward slash infix", "acme/prod"},
		{"leading slash", "/acme"},
		{"backslash traversal", `..\..\windows`},
		{"backslash infix", `acme\prod`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var charErr *InvalidTenantIDCharError
			err := validateReservedTenantID(context.Background(), tc.id)
			if !errors.As(err, &charErr) {
				t.Fatalf("id %q: expected *InvalidTenantIDCharError, got %T: %v", tc.id, err, err)
			}

			// Also rejected for internal callers — the check is unconditional.
			err = validateReservedTenantID(WithInternalCall(context.Background()), tc.id)
			if !errors.As(err, &charErr) {
				t.Fatalf("id %q (internal): expected *InvalidTenantIDCharError, got %T: %v", tc.id, err, err)
			}
		})
	}
}

// TestValidateReservedTenantID_RejectsRelativeSegments verifies "." and ".."
// are rejected as tenant ids for all callers.
func TestValidateReservedTenantID_RejectsRelativeSegments(t *testing.T) {
	for _, id := range []string{".", ".."} {
		t.Run(id, func(t *testing.T) {
			var idErr *InvalidTenantIDError
			err := validateReservedTenantID(context.Background(), id)
			if !errors.As(err, &idErr) {
				t.Fatalf("id %q: expected *InvalidTenantIDError, got %T: %v", id, err, err)
			}

			err = validateReservedTenantID(WithInternalCall(context.Background()), id)
			if !errors.As(err, &idErr) {
				t.Fatalf("id %q (internal): expected *InvalidTenantIDError, got %T: %v", id, err, err)
			}
		})
	}
}

// TestInvalidTenantIDCharError_Error verifies the error message format.
func TestInvalidTenantIDCharError_Error(t *testing.T) {
	e := &InvalidTenantIDCharError{TenantID: "acme\x1f", Byte: 0x1f}
	got := e.Error()
	if got == "" {
		t.Error("Error() must return non-empty string")
	}
}

// TestReservedTenantIDError_Error verifies the error message format.
func TestReservedTenantIDError_Error(t *testing.T) {
	e := &ReservedTenantIDError{TenantID: "system", Reason: "is platform-reserved"}
	got := e.Error()
	want := "tenant: id system is reserved: is platform-reserved"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
