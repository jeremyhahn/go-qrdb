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

// The secrets:* tenancy gate (go-qrdb#372), at the layer that owns it.
//
// secrets:read/write/delete/list are ScopeSelfStrict, not ScopeTenantOnly:
// every identity -- the system tenant included -- reaches its OWN vault and
// nobody else's. These tests exercise the real rbac.RBACAuthorizer through
// AuthMiddleware.AuthorizeTenant with a target tenant INJECTED into the
// context rather than derived from the identity, because a self-scope check
// compared against a context tenant bound from the identity is vacuously
// true for every caller (go-qrdb's own "vacuous ScopeSelf" lesson). Every
// denial case below names a target tenant that genuinely differs from the
// caller's own tenant.
package middleware

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// secretsScopeRoles is a role service granting the full secrets:* family, so
// every test below turns on the SCOPE rule rather than a missing grant.
type secretsScopeRoles struct {
	role.RoleServicer
}

func (secretsScopeRoles) ResolvePermissions(context.Context, []string) ([]permission.Permission, error) {
	return []permission.Permission{
		permission.SecretsRead, permission.SecretsWrite,
		permission.SecretsDelete, permission.SecretsList,
	}, nil
}

func newSecretsScopeMiddleware() *AuthMiddleware {
	return New(rbac.New(secretsScopeRoles{}))
}

// secretsIdentityCtx attaches an identity for the given tenant WITHOUT
// pre-binding a target tenant into the context -- AuthorizeTenant is
// responsible for injecting the target, and the test supplies it separately
// so the two can genuinely differ.
func secretsIdentityCtx(callerTenantID string) context.Context {
	id := &auth.Identity{
		Subject:  callerTenantID + "-vault-user",
		TenantID: callerTenantID,
		Roles:    []string{"admin"},
	}
	return auth.WithIdentity(context.Background(), id)
}

// A tenant reading its OWN vault is admitted.
func TestAuthorizeTenant_Secrets_OwnTenantIsAdmitted(t *testing.T) {
	mw := newSecretsScopeMiddleware()

	if err := mw.AuthorizeTenant(secretsIdentityCtx("acme"), "acme", permission.SecretsRead); err != nil {
		t.Fatalf("a tenant reading its own vault must be admitted: %v", err)
	}
}

// A tenant reading ANOTHER tenant's vault -- the target injected here is
// "globex", genuinely distinct from the caller's own tenant "acme" -- must
// be refused.
func TestAuthorizeTenant_Secrets_ForeignTenantIsRefused(t *testing.T) {
	mw := newSecretsScopeMiddleware()

	err := mw.AuthorizeTenant(secretsIdentityCtx("acme"), "globex", permission.SecretsWrite)
	if err == nil {
		t.Fatal("a tenant naming another tenant's vault as its target must be refused")
	}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("err = %v, want an authorization denial", err)
	}
}

// The system tenant reaches its OWN vault. ScopeTenantOnly would have denied
// this outright, which is the regression #372 exists to fix.
func TestAuthorizeTenant_Secrets_SystemTenantOwnVaultIsAdmitted(t *testing.T) {
	mw := newSecretsScopeMiddleware()

	if err := mw.AuthorizeTenant(secretsIdentityCtx(tenantid.System), tenantid.System, permission.SecretsList); err != nil {
		t.Fatalf("the system tenant must reach its own vault: %v", err)
	}
}

// The system tenant does NOT reach another tenant's vault. Under ScopeSelf
// (the system-tenant lift) this would have been admitted; ScopeSelfStrict
// must refuse it just as it refuses any other identity.
func TestAuthorizeTenant_Secrets_SystemTenantForeignVaultIsRefused(t *testing.T) {
	mw := newSecretsScopeMiddleware()

	err := mw.AuthorizeTenant(secretsIdentityCtx(tenantid.System), "acme", permission.SecretsDelete)
	if err == nil {
		t.Fatal("the system tenant must NOT reach another tenant's vault")
	}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("err = %v, want an authorization denial", err)
	}
}

// Unauthenticated callers never reach a decision, regardless of target.
func TestAuthorizeTenant_Secrets_UnauthenticatedIsRefused(t *testing.T) {
	mw := newSecretsScopeMiddleware()

	err := mw.AuthorizeTenant(context.Background(), "acme", permission.SecretsRead)
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
}
