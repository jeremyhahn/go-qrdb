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

// Package authtest holds test support for driving a transport's REAL
// authentication and authorization chain: a fixed authenticator standing in
// for an mTLS handshake, a real role store seeded with the built-in roles, and
// identities that hold them.
//
// The point is that nothing downstream of authentication is a double. A test
// of tenant isolation that stubs the authorizer proves only that the handler
// asked; one wired to rbac.New over BuiltInRoles proves that the grants the
// product actually ships refuse the request.
//
// It is imported by tests only; a production import of it is a defect.
package authtest

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// tenantIAMSealedMetadataKey is the identity metadata key
// pkg/auth/middleware stamps on an identity whose tenant IAM was sealed at
// authentication time. authtest_test.go pins it against
// middleware.IdentityIAMSealed, so a rename there fails here rather than
// silently turning SealedIdentity into an ordinary one.
const tenantIAMSealedMetadataKey = "qrdb_tenant_iam_sealed"

// FixedAuthenticator authenticates every request as a copy of one identity,
// so a test can drive a transport's real authentication and authorization
// chain without a certificate authority. The identity can be swapped between
// requests with Set; a transport authenticates on its own goroutines, so the
// identity is held atomically. Construct it with NewFixedAuthenticator.
type FixedAuthenticator struct {
	identity atomic.Pointer[auth.Identity]
}

var _ auth.Authenticator = (*FixedAuthenticator)(nil)

// NewFixedAuthenticator returns a FixedAuthenticator authenticating as id.
func NewFixedAuthenticator(id *auth.Identity) *FixedAuthenticator {
	a := &FixedAuthenticator{}
	a.Set(id)
	return a
}

// Set replaces the identity later requests authenticate as.
func (a *FixedAuthenticator) Set(id *auth.Identity) { a.identity.Store(id) }

// Authenticate returns a copy of the current identity, or
// auth.ErrUnauthenticated when none has been set.
func (a *FixedAuthenticator) Authenticate(context.Context) (*auth.Identity, error) {
	id := a.identity.Load()
	if id == nil {
		return nil, auth.ErrUnauthenticated
	}
	copied := *id
	return &copied, nil
}

// Name identifies the authenticator.
func (a *FixedAuthenticator) Name() string { return "authtest-fixed" }

// BuiltInRoles returns a real in-memory role store holding the built-in roles
// of every tenant in tenantIDs, reconciled exactly as a node reconciles them
// at boot.
func BuiltInRoles(t testing.TB, tenantIDs ...string) role.RoleServicer {
	t.Helper()
	roles, err := role.NewStore(kvstore.NewMemoryKVStore())
	if err != nil {
		t.Fatalf("authtest: role store: %v", err)
	}
	for _, id := range tenantIDs {
		if _, err := roles.ReconcileBuiltInRoles(context.Background(), id); err != nil {
			t.Fatalf("authtest: reconcile built-in roles for %q: %v", id, err)
		}
	}
	return roles
}

// CustomRole creates in roles a tenant-defined role named name in tenantID
// carrying exactly perms, and returns its ID: the role a tenant Admin defines
// and assigns, as opposed to a built-in one. A store failure fails the test.
func CustomRole(t testing.TB, roles role.RoleServicer, tenantID, name string, perms ...permission.Permission) string {
	t.Helper()
	created, err := roles.CreateRole(context.Background(), &role.CreateRoleRequest{
		Name:        name,
		TenantID:    tenantID,
		Permissions: perms,
	})
	if err != nil {
		t.Fatalf("authtest: create role %q in %q: %v", name, tenantID, err)
	}
	return created.ID
}

// AdminIdentity returns an identity holding the built-in Admin role of its own
// tenant, and nothing else.
func AdminIdentity(tenantID, subject string) *auth.Identity {
	return &auth.Identity{
		Subject:  subject,
		TenantID: tenantID,
		Roles:    []string{role.SystemRoleID(tenantID, role.RoleNameAdmin)},
	}
}

// SealedIdentity returns the identity the middleware produces for a caller
// whose tenant IAM is sealed: no roles, and the sealed-window marker. It is the
// only identity the sealed-window fallback admits.
func SealedIdentity(tenantID, subject string) *auth.Identity {
	return &auth.Identity{
		Subject:  subject,
		TenantID: tenantID,
		Metadata: map[string]string{tenantIAMSealedMetadataKey: "true"},
	}
}

// OneToken returns a registry declaring, for perm, a bucket of one token that
// does not measurably refill within a test: the first request is admitted and
// every later one is refused.
func OneToken(perm permission.Permission) *ratelimit.Registry {
	r := ratelimit.NewRegistry()
	r.Register(perm, ratelimit.Policy{RatePerSecond: 1.0 / 3600, Burst: 1})
	return r
}

// Tenants is a real in-memory tenant service (tenant.NewKVTenantService)
// holding the tenants a test names. The one method it replaces is
// TenantBarrierUnseal, which it admits and counts: the KV service has no
// barrier to unseal, and a test of the sealed-window gate needs to see how
// many unseals the gate let through to the service.
type Tenants struct {
	tenant.TenantServicer
	unseals atomic.Int64
}

var _ tenant.TenantServicer = (*Tenants)(nil)

// NewTenants returns a Tenants holding every id in ids. The system tenant is
// created the way first boot creates it, through tenant.WithInternalCall.
func NewTenants(t testing.TB, ids ...string) *Tenants {
	t.Helper()
	svc, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("authtest: tenant service: %v", err)
	}
	ctx := tenant.WithInternalCall(context.Background())
	for _, id := range ids {
		if _, err := svc.CreateTenant(ctx, id, id, tenant.IsolationPrefix); err != nil {
			t.Fatalf("authtest: create tenant %q: %v", id, err)
		}
	}
	return &Tenants{TenantServicer: svc}
}

// TenantBarrierUnseal admits and counts the unseal.
func (s *Tenants) TenantBarrierUnseal(context.Context, string, []byte) error {
	s.unseals.Add(1)
	return nil
}

// Unseals reports how many unseals reached the service.
func (s *Tenants) Unseals() int64 { return s.unseals.Load() }

// HoldsData wires the tenant service's isolation-change precondition to a
// probe that answers hasData for every tenant, so a test can drive the
// "tenant must be empty" rule through a transport.
func (s *Tenants) HoldsData(t testing.TB, hasData bool) {
	t.Helper()
	w, ok := s.TenantServicer.(tenant.IsolationChangeWiring)
	if !ok {
		t.Fatal("authtest: the tenant service does not accept a data probe")
	}
	w.SetTenantDataProbe(fixedDataProbe(hasData))
}

// fixedDataProbe answers the same data state for every tenant.
type fixedDataProbe bool

// TenantHasData returns the fixed answer.
func (p fixedDataProbe) TenantHasData(context.Context, string) (bool, error) { return bool(p), nil }
