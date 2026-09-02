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

package rbac

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestAuthorize_NilIdentityIsRejectedBeforeIsSystemTenant answers, in code,
// the reachability question #188 asked rather than assumed: can a nil
// *auth.Identity reach IsSystemTenant's nil branch — where absence of an
// identity is system privilege — through the authorizer?
//
// It cannot. Authorize's first statement rejects a nil identity with
// auth.ErrUnauthenticated, and IsSystemTenant is consulted only far below
// that guard, in the system-tenant scope rule. The nil branch is therefore
// dead on the authorization path: it is live only for a direct caller of the
// exported predicate (including sdk.IdentityIsSystemTenant, which aliases it),
// and no such caller exists in this repository.
//
// The guard is what makes the branch dead, so the guard is what this test
// pins. If a future refactor moves the nil check — or adds a second
// authorization entry point that skips it — this fails, and the nil branch
// becomes reachable again exactly then.
func TestAuthorize_NilIdentityIsRejectedBeforeIsSystemTenant(t *testing.T) {
	if !IsSystemTenant(nil) {
		t.Fatal("IsSystemTenant(nil) is no longer true; this test's premise has changed and #188's question needs re-answering")
	}

	a := New(nil)
	err := a.Authorize(context.Background(), nil, "kv", "put")
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("Authorize(nil identity) = %v, want auth.ErrUnauthenticated; "+
			"a nil identity now reaches the system-tenant rule, where IsSystemTenant(nil) grants system privilege", err)
	}
}

// TestAuthorize_UnnormalizedEmptyTenantIsNotReachedViaTheMiddleware documents
// the OTHER half of the same predicate — the half that was reachable.
//
// IsSystemTenant(nil) was dead code; IsSystemTenant(&Identity{TenantID: ""})
// was not. Three in-repo paths mint an identity with an empty TenantID:
//
//   - pkg/auth/mtls.go: a verified client certificate carrying neither a
//     SPIFFE tenant SAN nor a Subject OU authenticates successfully with
//     TenantID "" (TenantFromCert returns "", false, nil).
//   - pkg/auth/middleware/onbehalf.go: a forwarded caller assertion re-carries
//     the origin node's tenant, which pkg/auth/forward does not require to be
//     non-empty.
//   - sdk/go/driver/session.go: a DSN with a user and no tenant.
//
// The fix is at the boundary (auth/middleware.finalizeIdentity now folds
// identity.TenantID through tenant.Normalize), so this test asserts the
// predicate's contract that the boundary depends on: a normalized identity
// and its unnormalized twin must agree, which is what makes folding a safe
// no-op rather than a policy change.
func TestAuthorize_UnnormalizedEmptyTenantIsNotReachedViaTheMiddleware(t *testing.T) {
	raw := &auth.Identity{Subject: "admin", TenantID: ""}
	folded := &auth.Identity{Subject: "admin", TenantID: tenant.Normalize(raw.TenantID)}

	if folded.TenantID != tenant.SystemTenantID {
		t.Fatalf("tenant.Normalize(%q) = %q, want %q", raw.TenantID, folded.TenantID, tenant.SystemTenantID)
	}
	if IsSystemTenant(raw) != IsSystemTenant(folded) {
		t.Fatalf("folding an absent tenant changed the authorization answer: %v -> %v",
			IsSystemTenant(raw), IsSystemTenant(folded))
	}

	// Guard against the reading that the fold made an absent tenant harmless.
	// It did not: the folded identity is system-privileged, which is exactly
	// the behaviour #188 flags and exactly the decision it leaves to the owner
	// (options 1 and 3 -- a fail-closed authorization predicate).
	if !IsSystemTenant(folded) {
		t.Fatal("an absent tenant no longer resolves system-privileged; that is a deliberate policy change, not this fix")
	}
}
