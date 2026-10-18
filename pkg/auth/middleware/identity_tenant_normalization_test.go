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

package middleware

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// tenantRecordingUserServicer records the tenant argument every user lookup is
// issued with, so a test can assert WHICH tenant's IAM the middleware asked.
// It embeds the package's existing double rather than reimplementing the whole
// UserServicer surface.
type tenantRecordingUserServicer struct {
	*mockUserServicer
	lastTenant string
}

func (m *tenantRecordingUserServicer) GetUserByUsername(ctx context.Context, tenantID, username string) (*user.User, error) {
	m.lastTenant = tenantID
	return m.mockUserServicer.GetUserByUsername(ctx, tenantID, username)
}

// TestFinalizeIdentity_FoldsAbsentTenantOnTheIdentity is the regression test
// for #188.
//
// finalizeIdentity is the funnel every authenticated identity crosses on every
// transport, and tenantid.IsSystem names it ("auth/middleware's identity
// normalization") as a reason its empty-string branch is safe. It previously
// folded only the CONTEXT tenant and left identity.TenantID raw — and
// rbac.IsSystemTenant reads the identity field, not the context. So the one
// value the authorization predicate actually consumes was the one value the
// boundary did not normalize.
//
// The assertion is on the predicate's INPUT, not on its answer: after the
// fold, rbac.IsSystemTenant is never handed "". That is what enforcing the
// precondition buys, and it is deliberately all it buys — see
// TestFinalizeIdentity_FoldDoesNotChangeTheAuthorizationOutcome.
func TestFinalizeIdentity_FoldsAbsentTenantOnTheIdentity(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"absent tenant", "", tenantid.System},
		{"system tenant", tenantid.System, tenantid.System},
		{"real tenant", "acme", "acme"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestMiddleware(&mockAuthorizer{})
			id := &auth.Identity{Subject: "admin", TenantID: tc.in}

			ctx, out, err := m.finalizeIdentity(context.Background(), id, unknownMethod())
			if err != nil {
				t.Fatalf("finalizeIdentity returned %v", err)
			}
			if out.TenantID != tc.want {
				t.Fatalf("identity.TenantID = %q, want %q", out.TenantID, tc.want)
			}
			if out.TenantID == "" {
				t.Fatal("identity.TenantID left empty: rbac.IsSystemTenant would be asked about an unnormalized value")
			}
			if got := tenant.FromContext(ctx); got != tc.want {
				t.Fatalf("tenant.FromContext = %q, want %q", got, tc.want)
			}
			// Context and identity must not merely both be correct — they
			// must agree. Two independent copies of the same fold is how they
			// drifted apart in the first place.
			if got := tenant.FromContext(ctx); got != out.TenantID {
				t.Fatalf("context tenant %q disagrees with identity tenant %q", got, out.TenantID)
			}
		})
	}
}

// TestFinalizeIdentity_SealedWindowFoldsToo covers the second exit from
// finalizeIdentity. A tenant whose IAM barrier is sealed returns early, before
// the shared tenant binding at the bottom, and carried its own open-coded copy
// of the fold. The early exit must land on the same normalized identity as the
// normal path, or the sealed window becomes a door the fix does not cover.
func TestFinalizeIdentity_SealedWindowFoldsToo(t *testing.T) {
	users := &tenantRecordingUserServicer{
		mockUserServicer: &mockUserServicer{err: tenant.ErrTenantSealed},
	}
	m := newTestMiddleware(&mockAuthorizer{}, WithUserService(users))

	ctx, out, err := m.finalizeIdentity(context.Background(),
		&auth.Identity{Subject: "admin", TenantID: ""}, unknownMethod())
	if err != nil {
		t.Fatalf("finalizeIdentity returned %v", err)
	}
	if !IdentityIAMSealed(out) {
		t.Fatal("expected the sealed marker to be stamped; this test is not exercising the sealed exit")
	}
	if out.TenantID != tenantid.System {
		t.Fatalf("sealed-exit identity.TenantID = %q, want %q", out.TenantID, tenantid.System)
	}
	if got := tenant.FromContext(ctx); got != tenantid.System {
		t.Fatalf("sealed-exit context tenant = %q, want %q", got, tenantid.System)
	}
}

// TestFinalizeIdentity_UserLookupUsesTheFoldedTenant pins that the fold
// happens BEFORE the IAM lookup, not after it.
//
// This is not cosmetic. Every tenant-scoped store in the repo already routes
// "" to the system keyspace through tenantid.IsSystem (store_resolver,
// identity_index, kv_service, database/manager_tenant), so looking a user up
// under "" and under "system" reaches the same records — but only one of those
// two spellings is the tenant the request is then authorized as. Folding first
// makes the tenant the middleware LOOKS IN and the tenant it AUTHORIZES AS the
// same string, so an audit line and a lookup cannot disagree.
func TestFinalizeIdentity_UserLookupUsesTheFoldedTenant(t *testing.T) {
	users := &tenantRecordingUserServicer{
		mockUserServicer: &mockUserServicer{err: &user.UserNotFoundError{Lookup: "system/admin"}},
	}
	m := newTestMiddleware(&mockAuthorizer{}, WithUserService(users))

	if _, _, err := m.finalizeIdentity(context.Background(),
		&auth.Identity{Subject: "admin", TenantID: ""}, unknownMethod()); err != nil {
		t.Fatalf("finalizeIdentity returned %v", err)
	}
	if users.lastTenant != tenantid.System {
		t.Fatalf("user lookup issued for tenant %q, want the folded %q", users.lastTenant, tenantid.System)
	}
}

// TestFinalizeIdentity_FoldDoesNotChangeTheAuthorizationOutcome records, as an
// executable statement rather than a claim in a commit message, the limit of
// this fix.
//
// Folding "" onto the system tenant preserves rbac.IsSystemTenant's answer
// exactly: an identity that authenticated with no tenant binding was
// system-privileged before the fold and is system-privileged after it. What
// changed is that the mapping is now performed once at a named boundary
// instead of being inferred inside an authorization predicate from a value
// nobody set.
//
// Making absence fail CLOSED is a different change — #188 options 1 and 3 —
// and it is an owner decision, because it would deny principals that
// authenticate today. If that decision is taken, this test is the one that
// must be inverted, and the fold above is the single place that implements it.
func TestFinalizeIdentity_FoldDoesNotChangeTheAuthorizationOutcome(t *testing.T) {
	raw := &auth.Identity{Subject: "admin", TenantID: ""}
	before := rbac.IsSystemTenant(raw)

	m := newTestMiddleware(&mockAuthorizer{})
	_, out, err := m.finalizeIdentity(context.Background(), raw, unknownMethod())
	if err != nil {
		t.Fatalf("finalizeIdentity returned %v", err)
	}
	after := rbac.IsSystemTenant(out)

	if before != after {
		t.Fatalf("the fold changed the authorization answer for an absent tenant: %v -> %v; "+
			"it is a normalization, not a policy change", before, after)
	}
	if !after {
		t.Fatal("an absent tenant no longer resolves system-privileged; that is a policy change #188 leaves to the owner")
	}
}
