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

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TestCertTargetGuard_RefusesSOAndOwnerTargets is the security review's
// probe (TestSecReviewProbe_IssueForPrivilegedTargets), turned into a
// permanent regression: an acme Admin with certs:issue cannot obtain a
// certificate for an account holding acme's SO role, and a system Admin
// cannot obtain one for the system tenant's Owner account either. Neither
// case grants an exception -- that is reserved for an Owner CALLER acting on
// an Owner target, exercised separately below.
func TestCertTargetGuard_RefusesSOAndOwnerTargets(t *testing.T) {
	ctx := context.Background()
	users := newCertEnrollmentUserStore(t)
	provider := newCertEnrollmentProvider(t)
	svc := NewCertEnrollmentService(provider, users, nil)

	cases := []struct {
		name, tenant, username, roleName string
	}{
		{"acme Admin targeting acme's SO", "acme", "acme-so", role.RoleNameSO},
		{"system Admin targeting system's Owner", "system", "owner-account", role.RoleNameOwner},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := users.CreateUser(ctx, &user.CreateUserRequest{
				Username: c.username, TenantID: c.tenant, WithoutEmailLoginKey: true,
				RoleIDs: []string{role.SystemRoleID(c.tenant, c.roleName)},
			}); err != nil {
				t.Fatalf("create %s: %v", c.username, err)
			}
			_, csrPEM, err := certtest.NewCSR("attacker")
			if err != nil {
				t.Fatal(err)
			}
			// No identity on ctx: a caller with no recognized Owner grant,
			// exactly like an Admin whose certs:issue permission carries no
			// role information the guard would honor.
			res, err := svc.SignCertificate(ctx, &SignCertificateRequest{
				TenantID: c.tenant, Username: c.username, CSRPEM: csrPEM, TTL: time.Hour,
			})
			var refused *CertTargetRefusedError
			if !errors.As(err, &refused) {
				t.Fatalf("SignCertificate for %s/%s (role %s) = %v, %v; want *CertTargetRefusedError", c.tenant, c.username, c.roleName, res, err)
			}
			if refused.Error() == "" {
				t.Fatal("empty error text")
			}
		})
	}
}

// TestCertTargetGuard_OwnerCallerMayTargetOwner is the one exception the
// owner ruling carves out: an Owner caller may obtain a certificate for an
// Owner account of its own tenant.
func TestCertTargetGuard_OwnerCallerMayTargetOwner(t *testing.T) {
	users := newCertEnrollmentUserStore(t)
	provider := newCertEnrollmentProvider(t)
	svc := NewCertEnrollmentService(provider, users, nil)

	ctx := context.Background()
	if _, err := users.CreateUser(ctx, &user.CreateUserRequest{
		Username: "owner-account", TenantID: "system", WithoutEmailLoginKey: true,
		RoleIDs: []string{role.SystemRoleID("system", role.RoleNameOwner)},
	}); err != nil {
		t.Fatalf("create owner-account: %v", err)
	}
	_, csrPEM, err := certtest.NewCSR("owner-account")
	if err != nil {
		t.Fatal(err)
	}

	ownerCtx := auth.WithIdentity(ctx, &auth.Identity{
		Subject:  "owner-account",
		TenantID: "system",
		Roles:    []string{role.SystemRoleID("system", role.RoleNameOwner)},
	})
	res, err := svc.SignCertificate(ownerCtx, &SignCertificateRequest{
		TenantID: "system", Username: "owner-account", CSRPEM: csrPEM, TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("Owner caller targeting the Owner account = %v, want success", err)
	}
	if res.Username != "owner-account" {
		t.Fatalf("issued for %q, want owner-account", res.Username)
	}
}

// TestCertTargetGuard_AdminMayStillTargetOrdinaryAccounts confirms the guard
// is scoped to SO/Owner only: an Admin -- no identity recognized as Owner --
// still obtains certificates for an ordinary account of its tenant, exactly
// as before this guard existed.
func TestCertTargetGuard_AdminMayStillTargetOrdinaryAccounts(t *testing.T) {
	ctx := context.Background()
	users := newCertEnrollmentUserStore(t)
	provider := newCertEnrollmentProvider(t)
	svc := NewCertEnrollmentService(provider, users, nil)

	createTestAccount(t, users, "acme", "alice")
	_, csrPEM, err := certtest.NewCSR("alice")
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.SignCertificate(ctx, &SignCertificateRequest{
		TenantID: "acme", Username: "alice", CSRPEM: csrPEM, TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("SignCertificate for an ordinary account = %v, want success", err)
	}
	if res.Username != "alice" {
		t.Fatalf("issued for %q, want alice", res.Username)
	}
}

// TestCertTargetGuard_RefusesEnrollForSOTarget confirms the guard applies to
// EnrollCertificate as well as SignCertificate: a bring-your-own certificate
// that would otherwise verify and bind is still refused when the account it
// names holds the tenant's SO role.
func TestCertTargetGuard_RefusesEnrollForSOTarget(t *testing.T) {
	ctx := context.Background()
	users := newCertEnrollmentUserStore(t)
	if _, err := users.CreateUser(ctx, &user.CreateUserRequest{
		Username: "acme-so", TenantID: "acme", WithoutEmailLoginKey: true,
		RoleIDs: []string{role.SystemRoleID("acme", role.RoleNameSO)},
	}); err != nil {
		t.Fatalf("create acme-so: %v", err)
	}
	registry := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
	root := newTestRoot(t, "acme-root")
	if _, err := registry.Add(ctx, "acme", pemOf(root.cert)); err != nil {
		t.Fatal(err)
	}
	leaf := root.clientLeaf(t, "acme-so", "acme")
	svc := NewCertEnrollmentService(nil, users, registry)

	_, err := svc.EnrollCertificate(ctx, &EnrollCertificateRequest{TenantID: "acme", Username: "acme-so", CertPEM: pemOf(leaf)})
	var refused *CertTargetRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("EnrollCertificate for acme's SO = %v, want *CertTargetRefusedError", err)
	}
}
