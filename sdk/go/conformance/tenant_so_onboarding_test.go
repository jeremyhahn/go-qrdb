// Copyright 2026 Jeremy Hahn
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

package conformance

import (
	"crypto/tls"
	"slices"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/wiring"
	"github.com/jeremyhahn/go-qrdb/pkg/bootstrap"
	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestTenantOnboarding_SOFromTenantCreate proves tenant create's optional
// security officer on every transport (go-qrdb#1042): the SO's credential is
// answered beside the admin's, carries the SO's own CSR key and logs in, and
// the SO -- not the admin -- grants the tenant's so role.
//
// Control: the admin, logged in on the same transport, is refused the same
// grant, so the SO's admission is the so role at work.
func TestTenantOnboarding_SOFromTenantCreate(t *testing.T) {
	for _, tk := range remoteTransports {
		t.Run(string(tk), func(t *testing.T) {
			c := bootOnboardingCell(t, tk, strictSeparation)
			adminKey, adminCSR, err := certtest.NewCSR("alice")
			if err != nil {
				t.Fatalf("admin CSR: %v", err)
			}
			soKey, soCSR, err := certtest.NewCSR("sam")
			if err != nil {
				t.Fatalf("SO CSR: %v", err)
			}
			resp, err := c.operator.Tenants().CreateTenant(boundedCtx(t), &transport.CreateTenantRequest{
				ID: "acme", Name: "acme",
				AdminUsername: "alice", AdminCSRPEM: string(adminCSR),
				SOUsername: "sam", SOCSRPEM: string(soCSR), SOPin: "sam-pin-1234",
			})
			if err != nil {
				t.Fatalf("%s: create tenant with an SO: %v", tk, err)
			}
			so := resp.SO
			if so == nil || so.Username != "sam" || so.UserID == 0 || so.CertPEM == "" || so.AwaitsBarrierInit {
				t.Fatalf("%s: SO credential = %+v, want sam's, created at once", tk, so)
			}
			if got := user.ComputeSPKIThumbprint(parseLeaf(t, []byte(so.CertPEM))); got != so.Thumbprint || got == resp.Admin.Thumbprint {
				t.Fatalf("%s: SO thumbprint %s, leaf %s, admin %s; want the SO's own key", tk, so.Thumbprint, got, resp.Admin.Thumbprint)
			}
			if _, err := tls.X509KeyPair([]byte(so.CertPEM), soKey); err != nil {
				t.Fatalf("%s: the SO's certificate does not carry the SO's CSR key: %v", tk, err)
			}

			ctx := tenant.WithTenantID(boundedCtx(t), "acme")
			svc := c.n.Services()
			userRole, err := svc.Role.GetRoleByName(ctx, "acme", role.RoleNameUser)
			if err != nil {
				t.Fatalf("role %s: %v", role.RoleNameUser, err)
			}
			uma, err := svc.User.CreateUser(ctx, &user.CreateUserRequest{
				WithoutEmailLoginKey: true, Username: "uma", TenantID: "acme", RoleIDs: []string{userRole.ID},
			})
			if err != nil {
				t.Fatalf("create uma: %v", err)
			}
			soRole := role.SystemRoleID("acme", role.RoleNameSO)

			alice := c.mustLogin(t, []byte(resp.Admin.CertPEM), adminKey)
			if err := alice.Roles().AssignRole(boundedCtx(t), uma.ID, soRole); err == nil {
				t.Fatalf("%s: the admin granted the so role", tk)
			}
			sam := c.mustLogin(t, []byte(so.CertPEM), soKey)
			if err := sam.Roles().AssignRole(boundedCtx(t), uma.ID, soRole); err != nil {
				t.Fatalf("%s: the SO granting the so role: %v", tk, err)
			}
			got, err := svc.User.GetUser(ctx, uma.ID)
			if err != nil || !slices.Contains(got.RoleIDs, soRole) {
				t.Fatalf("%s: uma = %+v, %v; want the so role held", tk, got, err)
			}
		})
	}
}

// TestTenantOnboarding_SOMatchingTheAdminIsRefused proves on every transport
// that an SO sharing the admin's key is refused and leaves no tenant behind.
func TestTenantOnboarding_SOMatchingTheAdminIsRefused(t *testing.T) {
	for _, tk := range remoteTransports {
		t.Run(string(tk), func(t *testing.T) {
			c := bootOnboardingCell(t, tk, strictSeparation)
			_, csr, err := certtest.NewCSR("alice")
			if err != nil {
				t.Fatalf("CSR: %v", err)
			}
			if _, err := c.operator.Tenants().CreateTenant(boundedCtx(t), &transport.CreateTenantRequest{
				ID: "acme", Name: "acme",
				AdminUsername: "alice", AdminCSRPEM: string(csr),
				SOUsername: "alice", SOCSRPEM: string(csr), SOPin: "alice-pin-1234",
			}); err == nil {
				t.Fatalf("%s: under strict separation of duties a tenant was created with the admin as its SO", tk)
			}
			if _, err := c.n.Services().Tenant.GetTenant(boundedCtx(t), "acme"); err == nil {
				t.Fatalf("%s: the refused create left tenant acme behind", tk)
			}
		})
	}
}

// TestTenantOnboarding_StandardPostureTheAdminIsTheSO is the standard
// posture over every remote transport: tenant create accepts the admin as the
// SO, and the admin grants the so role.
func TestTenantOnboarding_StandardPostureTheAdminIsTheSO(t *testing.T) {
	for _, tk := range remoteTransports {
		t.Run(string(tk), func(t *testing.T) {
			c := bootOnboardingCell(t, tk)
			key, csr, err := certtest.NewCSR("alice")
			if err != nil {
				t.Fatalf("CSR: %v", err)
			}
			resp, err := c.operator.Tenants().CreateTenant(boundedCtx(t), &transport.CreateTenantRequest{
				ID: "acme", Name: "acme",
				AdminUsername: "alice", AdminCSRPEM: string(csr),
				SOUsername: "alice", SOCSRPEM: string(csr), SOPin: "alice-pin-1234",
			})
			// FIPS 140 mode forces strict separation whatever is configured,
			// so there the create is refused exactly as in the strict posture.
			if wiring.ResolveStrictSeparation(false) {
				want := (&bootstrap.SOMatchesAdminError{Field: "username"}).Error()
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("%s: FIPS 140 mode forces strict separation of duties: create err = %v, want %q", tk, err, want)
				}
				if _, err := c.n.Services().Tenant.GetTenant(boundedCtx(t), "acme"); err == nil {
					t.Fatalf("%s: the refused create left tenant acme behind", tk)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s: create tenant with the admin as its SO: %v", tk, err)
			}
			if so := resp.SO; so == nil || so.UserID != resp.Admin.UserID || so.Thumbprint != resp.Admin.Thumbprint {
				t.Fatalf("%s: SO = %+v, want the admin's account %+v", tk, so, resp.Admin)
			}

			ctx := tenant.WithTenantID(boundedCtx(t), "acme")
			svc := c.n.Services()
			userRole, err := svc.Role.GetRoleByName(ctx, "acme", role.RoleNameUser)
			if err != nil {
				t.Fatalf("role %s: %v", role.RoleNameUser, err)
			}
			uma, err := svc.User.CreateUser(ctx, &user.CreateUserRequest{
				WithoutEmailLoginKey: true, Username: "uma", TenantID: "acme", RoleIDs: []string{userRole.ID},
			})
			if err != nil {
				t.Fatalf("create uma: %v", err)
			}
			soRole := role.SystemRoleID("acme", role.RoleNameSO)
			alice := c.mustLogin(t, []byte(resp.Admin.CertPEM), key)
			if err := alice.Roles().AssignRole(boundedCtx(t), uma.ID, soRole); err != nil {
				t.Fatalf("%s: the admin granting the so role in the standard posture: %v", tk, err)
			}
			got, err := svc.User.GetUser(ctx, uma.ID)
			if err != nil || !slices.Contains(got.RoleIDs, soRole) {
				t.Fatalf("%s: uma = %+v, %v; want the so role held", tk, got, err)
			}
		})
	}
}

// strictSeparation boots a cell under strict separation of duties.
func strictSeparation(cfg *qrdbconfig.Config) { cfg.Auth.SeparationOfDuties = true }
