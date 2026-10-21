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
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// The remote Authorize endpoint, on every transport, answers through
// EvaluateAuthorization: these tests drive it over the real RBAC authorizer and
// role store with a tenant-defined custom role, so the decision a layer above
// receives for ota:activate and ota:confirm is the one the PDP makes.

// otaEvalFixture returns a middleware over the real RBAC authorizer and the
// role store behind it, with built-in roles seeded in acme and globex.
func otaEvalFixture(t *testing.T) (*AuthMiddleware, role.RoleServicer) {
	t.Helper()
	roles := authtest.BuiltInRoles(t, tenantid.System, "acme", "globex")
	return New(rbac.New(roles)), roles
}

// otaPerms are the two OTA permissions under test.
var otaPerms = []permission.Permission{permission.OTAActivate, permission.OTAConfirm}

// evaluateOTA asks the Authorize endpoint's question for perm: about target
// when it is set, otherwise about the caller itself. ctx carries the caller
// and the tenant the request is routed to.
func evaluateOTA(m *AuthMiddleware, ctx context.Context, target *auth.Identity, perm permission.Permission) (decision, err error) {
	return m.EvaluateAuthorization(ctx, target, permission.Resource(perm), permission.Action(perm))
}

// TestEvaluateAuthorization_OTACustomRole_AllowedInOwnTenant proves a caller
// whose custom role in acme holds the permission is told it is allowed when
// it asks about itself on acme.
func TestEvaluateAuthorization_OTACustomRole_AllowedInOwnTenant(t *testing.T) {
	for _, perm := range otaPerms {
		m, roles := otaEvalFixture(t)
		caller := authtest.Grant(t, roles, &auth.Identity{Subject: "ota-operator", TenantID: "acme"}, perm)
		decision, err := evaluateOTA(m, callerCtx(caller), nil, perm)
		if err != nil || decision != nil {
			t.Errorf("custom role@acme asks %s on acme: got (%v, %v), want allowed", perm, decision, err)
		}
	}
}

// TestEvaluateAuthorization_OTACustomRole_DeniedInOtherTenant proves the same
// caller is told it is denied when the request is routed to globex: the
// decision is a policy denial returned as the answer, not a refused request.
func TestEvaluateAuthorization_OTACustomRole_DeniedInOtherTenant(t *testing.T) {
	for _, perm := range otaPerms {
		m, roles := otaEvalFixture(t)
		caller := authtest.Grant(t, roles, &auth.Identity{Subject: "ota-operator", TenantID: "acme"}, perm)
		ctx := tenant.WithTenantID(auth.WithIdentity(context.Background(), caller), "globex")
		decision, err := evaluateOTA(m, ctx, nil, perm)
		var denied *rbac.PermissionDeniedError
		if err != nil || !errors.As(decision, &denied) {
			t.Errorf("custom role@acme asks %s on globex: got (%v, %v), want a *PermissionDeniedError decision", perm, decision, err)
		}
	}
}

// TestEvaluateAuthorization_OTACustomRole_AboutAnotherIdentity covers the
// form a layer above uses to gate a device's caller: a resolver holding
// permissions:resolve in acme learns that an acme identity with the custom
// role is allowed, and that a globex identity presenting acme's role ID is
// not, while a resolver in globex gets no answer about the acme identity.
func TestEvaluateAuthorization_OTACustomRole_AboutAnotherIdentity(t *testing.T) {
	for _, perm := range otaPerms {
		m, roles := otaEvalFixture(t)
		resolverA := authtest.Grant(t, roles, &auth.Identity{Subject: "resolver", TenantID: "acme"}, permission.PermissionsResolve)
		resolverB := authtest.Grant(t, roles, &auth.Identity{Subject: "resolver", TenantID: "globex"}, permission.PermissionsResolve)
		holder := authtest.Grant(t, roles, &auth.Identity{Subject: "ota-operator", TenantID: "acme"}, perm)

		decision, err := evaluateOTA(m, callerCtx(resolverA), holder, perm)
		if err != nil || decision != nil {
			t.Errorf("acme resolver asks %s for the acme holder: got (%v, %v), want allowed", perm, decision, err)
		}

		intruder := &auth.Identity{Subject: "intruder", TenantID: "globex", Roles: holder.Roles}
		decision, err = evaluateOTA(m, callerCtx(resolverB), intruder, perm)
		if err != nil || decision == nil {
			t.Errorf("globex resolver asks %s for a globex identity with acme's role ID: got (%v, %v), want a denial", perm, decision, err)
		}

		if decision, err = evaluateOTA(m, callerCtx(resolverB), holder, perm); err == nil {
			t.Errorf("globex resolver got an answer (%v) about the acme holder's %s", decision, perm)
		}
	}
}
