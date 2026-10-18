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

package middleware

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// newSystemConfinedUserStore builds the fallback user store exactly as qrdbd
// does (cmd/qrdbd: bootUserSvc): the real user store over a kvstore confined
// to the SYSTEM tenant's identity namespace. It is the faithful double for the
// production fallback because it IS the production construction — it can hold
// system-tenant records and nothing else.
func newSystemConfinedUserStore(t *testing.T) user.UserServicer {
	t.Helper()
	confined, err := kvstore.NewNamespacedKVStore(kvstore.NewMemoryKVStore(),
		tenant.IdentityNamespace(tenantid.System))
	if err != nil {
		t.Fatalf("NewNamespacedKVStore: %v", err)
	}
	gen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("NewSnowflakeGenerator: %v", err)
	}
	svc, err := user.NewStore(confined, gen)
	if err != nil {
		t.Fatalf("user.NewStore: %v", err)
	}
	return svc
}

// TestFallbackUserStore_NeverAnswersForAnotherTenant is the regression for a
// load-dependent false 403 measured in the cluster tenancy suite.
//
// When the linearizable user lookup misses its 500 ms budget, lookupUser
// consults the StaleRead fallback. qrdbd's fallback is confined to the system
// tenant, so for any OTHER tenant's user it can only answer "not found" — and
// lookupUser treated that answer as authoritative. The identity then proceeded
// with no roles, and RBAC returned a definitive 403 to a correctly enrolled
// user whose only problem was a slow ReadIndex. The answer a store that does
// not hold the tenant can give is "I cannot say", which is the transient error
// the primary already returned: 503, retryable.
func TestFallbackUserStore_NeverAnswersForAnotherTenant(t *testing.T) {
	fallback := newSystemConfinedUserStore(t)
	createUser(t, fallback, tenantid.System, "admin", []string{"system-admin-role"})

	// The tenant's own admin shares the subject name, as tenant provisioning
	// creates it; only the tenant differs.
	id := &auth.Identity{Subject: "admin", TenantID: "acme"}
	m := newTestMiddleware(&mockAuthorizer{},
		WithAuthenticator(&mockAuthenticator{name: "mtls", identity: id}),
		WithUserService(&mockUserServicer{err: context.DeadlineExceeded}),
		WithUserServiceFallback(fallback),
	)

	_, got, err := m.authenticate(context.Background())
	var unavailable *StoreUnavailableError
	if !errors.As(err, &unavailable) {
		roles := []string(nil)
		if got != nil {
			roles = got.Roles
		}
		t.Fatalf("authenticate: err = %v (roles %v); want *StoreUnavailableError: a store confined to the "+
			"system tenant cannot say that tenant acme has no such user", err, roles)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("StoreUnavailableError should carry the primary's transient error, got %v", err)
	}
}

// TestFallbackUserStore_StillServesTheSystemTenant pins the half of the
// contract that must not move: for the tenant the fallback holds, a slow
// ReadIndex still fails over to it.
func TestFallbackUserStore_StillServesTheSystemTenant(t *testing.T) {
	fallback := newSystemConfinedUserStore(t)
	createUser(t, fallback, tenantid.System, "admin", []string{"system-admin-role"})

	id := &auth.Identity{Subject: "admin", TenantID: tenantid.System}
	m := newTestMiddleware(&mockAuthorizer{},
		WithAuthenticator(&mockAuthenticator{name: "mtls", identity: id}),
		WithUserService(&mockUserServicer{err: context.DeadlineExceeded}),
		WithUserServiceFallback(fallback),
		WithMTLSBindingPolicy(auth.MTLSBindingEnrolled),
	)

	_, got, err := m.authenticate(context.Background())
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if len(got.Roles) != 1 || got.Roles[0] != "system-admin-role" {
		t.Fatalf("roles = %v, want the system admin's roles from the fallback", got.Roles)
	}
}
