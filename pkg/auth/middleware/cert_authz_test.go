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

// The certs tenancy gate, at the layer that owns it.
//
// The transport tests (pkg/api/rest, pkg/api/grpc) prove the gate is CALLED.
// These prove it DECIDES correctly, against the real rbac.RBACAuthorizer and
// the real permission registry, so a future transport wiring it up inherits a
// rule that was checked rather than one that was copied.

package middleware

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// certAuthzRoles is a role service granting exactly what the built-in Admin
// role grants on the certs surface — CertsRevokeCross included, because the
// real role really does hand it to every tenant admin. The scope rule, not an
// absent grant, is what must refuse a tenant-local caller.
type certAuthzRoles struct {
	role.RoleServicer
	perms []permission.Permission
}

func (r *certAuthzRoles) ResolvePermissions(context.Context, []string) ([]permission.Permission, error) {
	return r.perms, nil
}

func newCertAuthzMiddleware() *AuthMiddleware {
	return New(rbac.New(&certAuthzRoles{perms: []permission.Permission{
		permission.CertsGenerate,
		permission.CertsRevoke,
		permission.CertsRevokeCross,
		permission.CertsList,
	}}))
}

// certAuthzCtx builds the context the authentication path builds: identity
// attached, tenant bound FROM that identity.
func certAuthzCtx(tenantID string) context.Context {
	id := &auth.Identity{Subject: tenantID + "-admin", TenantID: tenantID, Roles: []string{"admin"}}
	return tenant.WithTenantID(auth.WithIdentity(context.Background(), id), tenantID)
}

// stubSerialResolver answers from a fixed serial -> tenant table.
type stubSerialResolver struct {
	owners map[string]string
	err    error
}

func (r *stubSerialResolver) TenantForSerial(_ context.Context, serial string) (string, bool, error) {
	if r.err != nil {
		return "", false, r.err
	}
	owner, ok := r.owners[serial]
	return owner, ok, nil
}

var _ CertSerialResolver = (*stubSerialResolver)(nil)

// ---------------------------------------------------------------------------
// AuthorizeCertTenant
// ---------------------------------------------------------------------------

// TestAuthorizeCertTenant_OwnTenantIsAdmitted is the ordinary case.
func TestAuthorizeCertTenant_OwnTenantIsAdmitted(t *testing.T) {
	mw := newCertAuthzMiddleware()

	if err := mw.AuthorizeCertTenant(certAuthzCtx("acme"), "acme", permission.CertsGenerate); err != nil {
		t.Fatalf("a tenant acting on itself must be admitted: %v", err)
	}
}

// TestAuthorizeCertTenant_ForeignTenantIsRefused is the decision the route
// permission could not make: the caller holds certs:generate, and the target
// is not its tenant.
func TestAuthorizeCertTenant_ForeignTenantIsRefused(t *testing.T) {
	mw := newCertAuthzMiddleware()

	err := mw.AuthorizeCertTenant(certAuthzCtx("acme"), "globex", permission.CertsGenerate)
	if err == nil {
		t.Fatal("a tenant naming another tenant as its target must be refused")
	}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("err = %v, want an authorization denial", err)
	}
}

// TestAuthorizeCertTenant_SystemTenantIsConfinedToItself: the certs verbs are
// ScopeSelfStrict, so the system-tenant rule does not lift them. A certificate
// the system tenant minted for globex's user would authenticate as that user.
func TestAuthorizeCertTenant_SystemTenantIsConfinedToItself(t *testing.T) {
	mw := newCertAuthzMiddleware()
	sys := certAuthzCtx(tenantid.System)

	for _, perm := range []permission.Permission{permission.CertsGenerate, permission.CertsList} {
		err := mw.AuthorizeCertTenant(sys, "globex", perm)
		if !errors.Is(err, auth.ErrUnauthorized) {
			t.Errorf("%s on globex from the system tenant: err = %v, want an authorization denial", perm, err)
		}
		if err := mw.AuthorizeCertTenant(sys, tenantid.System, perm); err != nil {
			t.Errorf("%s on the system tenant's own certificates: %v", perm, err)
		}
	}
}

// TestAuthorizeCertTenant_UnauthenticatedIsRefused covers the nil-identity
// path, which must never fall through to a decision.
func TestAuthorizeCertTenant_UnauthenticatedIsRefused(t *testing.T) {
	mw := newCertAuthzMiddleware()

	err := mw.AuthorizeCertTenant(context.Background(), "acme", permission.CertsGenerate)
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
}

// ---------------------------------------------------------------------------
// AuthorizeCertRevoke
// ---------------------------------------------------------------------------

// TestAuthorizeCertRevoke_OwnTenantSerialIsAdmitted is the ordinary
// revocation and the one that must not become collateral damage.
func TestAuthorizeCertRevoke_OwnTenantSerialIsAdmitted(t *testing.T) {
	mw := newCertAuthzMiddleware()
	res := &stubSerialResolver{owners: map[string]string{"s-acme": "acme"}}

	if err := mw.AuthorizeCertRevoke(certAuthzCtx("acme"), "s-acme", res); err != nil {
		t.Fatalf("a tenant revoking its own certificate must be admitted: %v", err)
	}
}

// TestAuthorizeCertRevoke_ForeignTenantSerialIsRefused is the defect.
func TestAuthorizeCertRevoke_ForeignTenantSerialIsRefused(t *testing.T) {
	mw := newCertAuthzMiddleware()
	res := &stubSerialResolver{owners: map[string]string{"s-globex": "globex"}}

	err := mw.AuthorizeCertRevoke(certAuthzCtx("acme"), "s-globex", res)
	if err == nil {
		t.Fatal("a tenant revoking another tenant's serial must be refused")
	}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("err = %v, want an authorization denial", err)
	}
}

// TestAuthorizeCertRevoke_UnknownSerialRequiresTheSystemTenant is the
// CA-operator rule. The caller HOLDS CertsRevokeCross; ScopeCross is what
// refuses it.
func TestAuthorizeCertRevoke_UnknownSerialRequiresTheSystemTenant(t *testing.T) {
	mw := newCertAuthzMiddleware()
	res := &stubSerialResolver{owners: map[string]string{}}

	if err := mw.AuthorizeCertRevoke(certAuthzCtx("acme"), "unknown", res); err == nil {
		t.Fatal("a tenant-local caller must not write an unattributable deny marker")
	}
	if err := mw.AuthorizeCertRevoke(certAuthzCtx(tenantid.System), "unknown", res); err != nil {
		t.Fatalf("deny-by-serial must survive for the CA operator: %v", err)
	}
}

// TestAuthorizeCertRevoke_SystemTenantCannotRevokeAnotherTenantsSerial: on
// the known-serial branch the operator is confined to its own tenant like
// everyone else. Holding certs:revoke:cross does not help, because a serial
// with an owner is never the deny-by-serial case.
func TestAuthorizeCertRevoke_SystemTenantCannotRevokeAnotherTenantsSerial(t *testing.T) {
	mw := newCertAuthzMiddleware()
	res := &stubSerialResolver{owners: map[string]string{"s-globex": "globex", "s-system": tenantid.System}}
	sys := certAuthzCtx(tenantid.System)

	err := mw.AuthorizeCertRevoke(sys, "s-globex", res)
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("the platform operator revoking globex's serial: err = %v, want an authorization denial", err)
	}
	if err := mw.AuthorizeCertRevoke(sys, "s-system", res); err != nil {
		t.Fatalf("the platform operator revoking its own serial: %v", err)
	}
}

// TestAuthorizeCertRevoke_ResolverErrorRefuses is the fail-closed case. An
// unknown target is not an absent one: reporting a resolver failure as "no
// owner" would route every revocation into the deny-by-serial branch the
// moment the store hiccuped.
func TestAuthorizeCertRevoke_ResolverErrorRefuses(t *testing.T) {
	mw := newCertAuthzMiddleware()
	sentinel := errors.New("store is down")
	res := &stubSerialResolver{err: sentinel}

	err := mw.AuthorizeCertRevoke(certAuthzCtx("acme"), "s-acme", res)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the resolver's own error", err)
	}
}

// TestAuthorizeCertRevoke_NilResolverRefusesAsUnavailable covers the wiring
// fault. It must not read as a permission problem, or an entitled operator is
// sent to fix a grant that was never consulted.
func TestAuthorizeCertRevoke_NilResolverRefusesAsUnavailable(t *testing.T) {
	mw := newCertAuthzMiddleware()

	err := mw.AuthorizeCertRevoke(certAuthzCtx("acme"), "s-acme", nil)
	var cerr *CertSerialUnresolvableError
	if !errors.As(err, &cerr) {
		t.Fatalf("err = %v, want *CertSerialUnresolvableError", err)
	}
	if cerr.Serial != "s-acme" {
		t.Errorf("Serial = %q, want %q", cerr.Serial, "s-acme")
	}
	if got := ClassifyAuthError(err); got != AuthFailureUnavailable {
		t.Errorf("class = %v, want AuthFailureUnavailable: a wiring fault is not a denial", got)
	}
}

// TestAuthorizeCertRevoke_UnauthenticatedIsRefusedBeforeResolving proves the
// identity check runs FIRST. An anonymous caller must not be able to probe
// which tenant owns a serial by watching how the gate fails.
func TestAuthorizeCertRevoke_UnauthenticatedIsRefusedBeforeResolving(t *testing.T) {
	mw := newCertAuthzMiddleware()
	res := &countingResolver{}

	err := mw.AuthorizeCertRevoke(context.Background(), "s-acme", res)
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
	if res.calls != 0 {
		t.Errorf("the resolver was consulted %d time(s) for an unauthenticated caller", res.calls)
	}
}

// countingResolver records whether it was consulted at all.
type countingResolver struct{ calls int }

func (r *countingResolver) TenantForSerial(context.Context, string) (string, bool, error) {
	r.calls++
	return "", false, nil
}

var _ CertSerialResolver = (*countingResolver)(nil)
