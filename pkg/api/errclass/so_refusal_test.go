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

package errclass_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
)

func TestRefusal_SORefusals(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantReason string
		wantTenant string
		wantMD     bool
	}{
		{"required", &auth.SORoleRequiredError{}, auth.ReasonSORoleRequired, "", false},
		{"required wrapped", fmt.Errorf("gate: %w", &auth.SORoleRequiredError{}), auth.ReasonSORoleRequired, "", false},
		{"conflict", &auth.SORoleConflictError{TenantID: "t1"}, auth.ReasonSORoleConflict, "t1", true},
		{"conflict wrapped", fmt.Errorf("role write: %w", &auth.SORoleConflictError{TenantID: "acme"}), auth.ReasonSORoleConflict, "acme", true},
		{"conflict empty tenant", &auth.SORoleConflictError{}, auth.ReasonSORoleConflict, "", true},
		{"conflict joined with required", errors.Join(&auth.SORoleConflictError{TenantID: "t2"}, &auth.SORoleRequiredError{}), auth.ReasonSORoleConflict, "t2", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, md, ok := errclass.Refusal(tc.err)
			if !ok || reason != tc.wantReason {
				t.Fatalf("Refusal = (%q, %v, %v), want reason %q", reason, md, ok, tc.wantReason)
			}
			if !tc.wantMD {
				if len(md) != 0 {
					t.Fatalf("unexpected metadata %v", md)
				}
				return
			}
			if got := auth.SORoleConflictFromMetadata(md); got.TenantID != tc.wantTenant {
				t.Fatalf("decoded tenant %q, want %q", got.TenantID, tc.wantTenant)
			}
		})
	}
}

func TestRefusal_NonSOErrorsCarryNoSOReason(t *testing.T) {
	for name, err := range map[string]error{
		"nil":   nil,
		"plain": errors.New("SO role required"),
	} {
		reason, _, _ := errclass.Refusal(err)
		if reason == auth.ReasonSORoleRequired || reason == auth.ReasonSORoleConflict {
			t.Fatalf("%s: classified as SO refusal %q", name, reason)
		}
	}
}

// A strict RBAC refusal of a combined SO/Admin caller carries the typed
// conflict, so the transports report SO_ROLE_CONFLICT with its tenant.
func TestRefusal_RBACDenialCarriesSOConflict(t *testing.T) {
	err := fmt.Errorf("authorize: %w", &rbac.PermissionDeniedError{
		Resource: "kv", Action: "put", Subject: "both",
		Cause: &auth.SORoleConflictError{TenantID: "acme"},
	})
	reason, md, ok := errclass.Refusal(err)
	if !ok || reason != auth.ReasonSORoleConflict {
		t.Fatalf("Refusal = (%q, %v, %v), want %q", reason, md, ok, auth.ReasonSORoleConflict)
	}
	if got := auth.SORoleConflictFromMetadata(md); got == nil || got.TenantID != "acme" {
		t.Fatalf("metadata %v does not round-trip tenant acme", md)
	}
}
