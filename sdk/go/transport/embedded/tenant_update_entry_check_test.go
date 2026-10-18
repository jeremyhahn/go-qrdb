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

package embedded

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestUpdateTenant_EntryCheckChargesRateLimitAndAudit is the owner security
// review's item 4: UpdateTenant must charge tenants:update through the
// ENTRY check (rate limit plus audit) before the target re-check
// (AuthorizeTenantUpdate), exactly as REST's route-level middleware charges
// it before handleTenantByID ever runs AuthorizeTenantUpdate as a
// handler-level re-check. AuthorizeTenantTarget -- what the re-check calls
// -- deliberately charges NEITHER on a grant (see its own doc comment in
// pkg/auth/middleware/middleware.go), so without the entry check a
// successful embedded UpdateTenant was rate-limited and audited nowhere at
// all.
//
// A burst=1 policy on tenants:update proves the rate limit: the first
// rename succeeds, the second (same caller, same permission, no time
// elapsed) is refused by the limiter -- which could only happen if
// UpdateTenant actually calls into the rate-limited authorize() path at
// least once per call. A mutation deleting the entry check reverts this to
// an unlimited number of successes.
func TestUpdateTenant_EntryCheckChargesRateLimitAndAudit(t *testing.T) {
	roles := authtest.BuiltInRoles(t, "acme")
	ring := audit.NewRingBuffer(64)

	limits := ratelimit.NewRegistry()
	limits.Register(permission.TenantsUpdate, ratelimit.Policy{RatePerSecond: 0.001, Burst: 1})
	limiter := ratelimit.NewTokenBucketLimiter()
	t.Cleanup(func() { _ = limiter.Close() })

	mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles),
		authmw.WithAuditLogger(audit.NewDecisionLogger(ring)),
		authmw.WithRateLimiter(limiter),
		authmw.WithRateLimitPolicies(limits),
	)

	svc := &mockTenantService{
		updateFn: func(_ context.Context, id string, patch tenant.TenantPatch) (*tenant.TenantInfo, error) {
			return &tenant.TenantInfo{ID: id, Name: patch.Name}, nil
		},
	}
	tr := newExtTransport(t, WithTenantService(svc), WithAuthMiddleware(mw))

	acmeAdmin := ctxFor(authtest.AdminIdentity("acme", "alice"))

	// First rename: the burst=1 bucket is full, so it is admitted.
	if _, err := tr.UpdateTenant(acmeAdmin, &transport.UpdateTenantRequest{TenantID: "acme", Name: "Acme One"}); err != nil {
		t.Fatalf("first UpdateTenant: %v", err)
	}

	// Second rename, same caller/permission, no time elapsed: the entry
	// check's rate limiter has no token left, so it must be refused. This
	// is NOT a permission denial -- the caller holds tenants:update -- it
	// is specifically a rate-limit refusal, which can only fire if
	// UpdateTenant is actually charging the limiter.
	_, err := tr.UpdateTenant(acmeAdmin, &transport.UpdateTenantRequest{TenantID: "acme", Name: "Acme Two"})
	if err == nil {
		t.Fatal("second UpdateTenant within the same burst window succeeded; want a rate-limit refusal")
	}

	// And the audit log carries BOTH decisions for tenants:update -- the
	// entry check's own record, present whether the call is admitted or
	// refused (AuthorizeTarget's authorize() always audits).
	var tenantsUpdateEvents int
	for _, ev := range ring.Snapshot() {
		if ev.Resource == permission.TenantsUpdate.String() {
			tenantsUpdateEvents++
		}
	}
	if tenantsUpdateEvents < 2 {
		t.Fatalf("audit log carries %d tenants:update decisions, want at least 2 (one per "+
			"UpdateTenant call) -- the entry check must audit every decision, not only denials",
			tenantsUpdateEvents)
	}
}
