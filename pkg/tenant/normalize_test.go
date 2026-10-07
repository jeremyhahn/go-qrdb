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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestFromContext_FoldsAbsentTenant proves the FIRST of the two paths
// IsSystemTenant's doc comment names as already folding "". It was true
// before this change and is asserted here because the doc comment leaned on
// it without a test.
//
// The explicitly-bound empty string is the interesting case: a caller that
// calls WithTenantID(ctx, "") has bound a value, not left the key absent, and
// FromContext must fold it exactly as it folds an unbound context.
func TestFromContext_FoldsAbsentTenant(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"unbound context", context.Background(), tenantid.System},
		{"explicitly bound empty string", WithTenantID(context.Background(), ""), tenantid.System},
		{"bound system tenant", WithTenantID(context.Background(), tenantid.System), tenantid.System},
		{"bound real tenant", WithTenantID(context.Background(), "acme"), "acme"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FromContext(tc.ctx)
			if got != tc.want {
				t.Fatalf("FromContext = %q, want %q", got, tc.want)
			}
			if got == "" {
				t.Fatal("FromContext returned the empty string: IsSystemTenant's precondition is violated on the context path")
			}
		})
	}
}

// TestEffectiveTenant_AbsentTenantNeverArrivesUnnormalized is the go-qrdb
// analogue of the consumer-side ResolveTenantFor trace in #188.
//
// EffectiveTenant sources the caller tenant from FromContext, never from a
// caller-supplied string, so an unnormalized "" cannot reach its
// IsSystemTenant call at all. Both rows below therefore take the
// system-tenant branch by the same route, which is what makes the fold the
// enforcement point: change the fold and BOTH rows change together, rather
// than one of them slipping through a second, unnormalized door.
//
// Note what this test does NOT claim. An absent tenant still RESOLVES
// cross-tenant here, because folding maps it onto the system tenant, which is
// privileged by design. Making absence fail closed is a different decision
// (#188 options 1 and 3) and is not what enforcing the precondition buys.
func TestEffectiveTenant_AbsentTenantNeverArrivesUnnormalized(t *testing.T) {
	unbound := context.Background()
	boundEmpty := WithTenantID(context.Background(), "")

	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{"unbound context", unbound},
		{"explicitly bound empty string", boundEmpty},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if caller := FromContext(tc.ctx); caller != tenantid.System {
				t.Fatalf("caller tenant reached EffectiveTenant as %q, want it folded to %q", caller, tenantid.System)
			}
			// The system tenant has no lift (owner ruling, 2026-10-01): an
			// absent tenant folds to it, but folding to a tenant is not the
			// same as that tenant reaching another one. Both rows are
			// refused by the same route FromContext's fold feeds into.
			if _, err := EffectiveTenant(tc.ctx, "acme", "create user"); err == nil {
				t.Fatal("EffectiveTenant granted cross-tenant access to a caller folded to the system tenant; want CrossTenantDeniedError")
			}
		})
	}

	// The same refusal holds for a caller explicitly bound to a real tenant.
	if _, err := EffectiveTenant(WithTenantID(context.Background(), "acme"), "other", "create user"); err == nil {
		t.Fatal("EffectiveTenant granted cross-tenant access to a caller bound to acme; want CrossTenantDeniedError")
	}
}
