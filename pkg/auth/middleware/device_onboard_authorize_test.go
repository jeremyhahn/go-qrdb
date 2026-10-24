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
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// onboardingTenants is acme and globex under the real RBAC authorizer. Each
// tenant has defined a "device-onboarder" custom role carrying devices:onboard
// alone, and acme an "iam-checker" carrying permissions:resolve.
type onboardingTenants struct {
	mw                  *AuthMiddleware
	acmeOnboarder       string
	globexOnboarder     string
	acmePermissionCheck string
}

func newOnboardingTenants(t *testing.T) onboardingTenants {
	t.Helper()
	roles := authtest.BuiltInRoles(t, tenantid.System, "acme", "globex")
	return onboardingTenants{
		acmeOnboarder:       authtest.CustomRole(t, roles, "acme", "device-onboarder", permission.DevicesOnboard),
		globexOnboarder:     authtest.CustomRole(t, roles, "globex", "device-onboarder", permission.DevicesOnboard),
		acmePermissionCheck: authtest.CustomRole(t, roles, "acme", "iam-checker", permission.PermissionsResolve),
		mw:                  New(rbac.New(roles)),
	}
}

func onboard(m *AuthMiddleware, caller *auth.Identity, requestTenant string, target *auth.Identity) (decision, err error) {
	ctx := tenant.WithTenantID(auth.WithIdentity(context.Background(), caller), requestTenant)
	return m.EvaluateAuthorization(ctx, target,
		permission.Resource(permission.DevicesOnboard), permission.Action(permission.DevicesOnboard))
}

func TestEvaluateAuthorization_DevicesOnboard_HolderAllowedInOwnTenant(t *testing.T) {
	ts := newOnboardingTenants(t)
	dana := &auth.Identity{Subject: "dana", TenantID: "acme", Roles: []string{ts.acmeOnboarder}}
	if decision, err := onboard(ts.mw, dana, "acme", nil); err != nil || decision != nil {
		t.Fatalf("dana@acme devices:onboard in acme: decision=%v err=%v", decision, err)
	}
}

func TestEvaluateAuthorization_DevicesOnboard_HolderRefusedInOtherTenant(t *testing.T) {
	ts := newOnboardingTenants(t)
	dana := &auth.Identity{Subject: "dana", TenantID: "acme", Roles: []string{ts.acmeOnboarder}}
	decision, err := onboard(ts.mw, dana, "globex", nil)
	if err != nil || !errors.Is(decision, auth.ErrUnauthorized) {
		t.Fatalf("dana@acme devices:onboard in globex: decision=%v err=%v; want a denial", decision, err)
	}
}

// TestEvaluateAuthorization_DevicesOnboard_AboutAnotherIdentity asks, as an
// acme caller holding permissions:resolve, whether acme's holder may onboard
// (yes) and whether globex's holder of the same-named role may (never
// answered: the question crosses into another tenant).
func TestEvaluateAuthorization_DevicesOnboard_AboutAnotherIdentity(t *testing.T) {
	ts := newOnboardingTenants(t)
	checker := &auth.Identity{Subject: "ivan", TenantID: "acme", Roles: []string{ts.acmePermissionCheck}}
	dana := &auth.Identity{Subject: "dana", TenantID: "acme", Roles: []string{ts.acmeOnboarder}}
	if decision, err := onboard(ts.mw, checker, "acme", dana); err != nil || decision != nil {
		t.Fatalf("ivan@acme asking about dana@acme: decision=%v err=%v; want allowed", decision, err)
	}
	gus := &auth.Identity{Subject: "gus", TenantID: "globex", Roles: []string{ts.globexOnboarder}}
	if _, err := onboard(ts.mw, checker, "acme", gus); err == nil {
		t.Fatal("ivan@acme got an answer about gus@globex")
	}
}

// TestEvaluateAuthorization_DevicesOnboard_HolderWithoutResolveCannotAsk
// checks that holding devices:onboard is not leave to ask about others.
func TestEvaluateAuthorization_DevicesOnboard_HolderWithoutResolveCannotAsk(t *testing.T) {
	ts := newOnboardingTenants(t)
	dana := &auth.Identity{Subject: "dana", TenantID: "acme", Roles: []string{ts.acmeOnboarder}}
	erin := &auth.Identity{Subject: "erin", TenantID: "acme", Roles: []string{ts.acmeOnboarder}}
	if _, err := onboard(ts.mw, dana, "acme", erin); err == nil {
		t.Fatal("dana@acme, without permissions:resolve, got an answer about erin@acme")
	}
}
