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
)

// TestNormalize_FoldsAbsentTenantOnly pins the fold itself: "" — and only ""
// — becomes SystemTenantID. Everything else is returned byte-for-byte,
// because Normalize is a boundary fold, not a validator: a boundary that
// silently rewrote a real tenant id would be a cross-tenant bug of its own.
func TestNormalize_FoldsAbsentTenantOnly(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"absent tenant folds to system", "", SystemTenantID},
		{"system tenant is already canonical", SystemTenantID, SystemTenantID},
		{"real tenant passes through", "acme", "acme"},
		{"whitespace is a real value, not absence", " ", " "},
		{"a tenant named like system is not system", "system-2", "system-2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Normalize(tc.in); got != tc.want {
				t.Fatalf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestNormalize_IsIdempotent states the property every boundary relies on:
// normalizing twice is normalizing once, so a value that crosses two folding
// boundaries (context binding, then identity binding) is not disturbed by the
// second.
func TestNormalize_IsIdempotent(t *testing.T) {
	for _, in := range []string{"", SystemTenantID, "acme"} {
		once := Normalize(in)
		if twice := Normalize(once); twice != once {
			t.Fatalf("Normalize is not idempotent for %q: %q then %q", in, once, twice)
		}
	}
}

// TestNormalize_EstablishesIsSystemTenantPrecondition is the assertion #188
// asked for and that nothing made before: whatever Normalize returns,
// IsSystemTenant never observes the empty string.
//
// IsSystemTenant's doc comment argued its safety from a precondition — that
// normalized paths have already folded "" — and the precondition was
// documented and enforced nowhere. This ties the two together in code: the
// predicate's empty branch survives for unscoped-store callers, and the fold
// is what guarantees an authorization decision never reaches it.
func TestNormalize_EstablishesIsSystemTenantPrecondition(t *testing.T) {
	for _, in := range []string{"", SystemTenantID, "acme"} {
		normalized := Normalize(in)
		if normalized == "" {
			t.Fatalf("Normalize(%q) returned the empty string; the precondition IsSystemTenant documents is not enforced", in)
		}
		// The fold must not change the answer, only the representation.
		if IsSystemTenant(normalized) != IsSystemTenant(in) {
			t.Fatalf("Normalize(%q) changed IsSystemTenant from %v to %v",
				in, IsSystemTenant(in), IsSystemTenant(normalized))
		}
	}
}

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
		{"unbound context", context.Background(), SystemTenantID},
		{"explicitly bound empty string", WithTenantID(context.Background(), ""), SystemTenantID},
		{"bound system tenant", WithTenantID(context.Background(), SystemTenantID), SystemTenantID},
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
			if caller := FromContext(tc.ctx); caller != SystemTenantID {
				t.Fatalf("caller tenant reached EffectiveTenant as %q, want it folded to %q", caller, SystemTenantID)
			}
			got, err := EffectiveTenant(tc.ctx, "acme", "create user")
			if err != nil {
				t.Fatalf("EffectiveTenant returned %v", err)
			}
			if got != "acme" {
				t.Fatalf("EffectiveTenant = %q, want %q", got, "acme")
			}
		})
	}

	// The contrast the issue drew: a caller WITH a tenant is refused the same
	// request an absent tenant is granted. Asserted here so the asymmetry is
	// recorded in a test rather than only in an issue body.
	if _, err := EffectiveTenant(WithTenantID(context.Background(), "acme"), "other", "create user"); err == nil {
		t.Fatal("EffectiveTenant granted cross-tenant access to a caller bound to acme; want CrossTenantDeniedError")
	}
}
