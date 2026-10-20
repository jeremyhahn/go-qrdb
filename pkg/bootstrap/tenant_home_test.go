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

package bootstrap

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// fakeHomes is a HomeEnsurer recording the tenants it was asked for and how
// many users existed at that moment.
type fakeHomes struct {
	users         *fakeUsers
	ensured       []string
	usersAtEnsure int
	err           error
}

func (f *fakeHomes) EnsureHome(_ context.Context, tenantID string) error {
	f.ensured = append(f.ensured, tenantID)
	f.usersAtEnsure = len(f.users.createCalls)
	return f.err
}

func TestProvisionTenant_EnsuresHomeBeforeFirstRecord(t *testing.T) {
	users := &fakeUsers{}
	homes := &fakeHomes{users: users}

	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		Isolation:   tenant.IsolationPrefix,
		AdminCSRPEM: validAdminCSR(t),
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: users, Roles: &fakeRoles{},
		Signer: &fakeSigner{cert: realCertPEM(t)}, Binder: &fakeBinder{},
		TrustDomain: onboardingTrustDomain,
		Homes:       homes, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	if len(homes.ensured) != 1 || homes.ensured[0] != "acme" {
		t.Fatalf("EnsureHome calls = %v, want [acme]", homes.ensured)
	}
	if homes.usersAtEnsure != 0 {
		t.Fatalf("%d users existed when the home was ensured, want 0 (home first)", homes.usersAtEnsure)
	}
	if len(users.createCalls) != 1 {
		t.Fatalf("user createCalls = %d, want 1", len(users.createCalls))
	}
}

func TestProvisionTenant_HomeFailureStopsProvisioning(t *testing.T) {
	users, roles := &fakeUsers{}, &fakeRoles{}
	homeErr := errors.New("no quorum")
	homes := &fakeHomes{users: users, err: homeErr}

	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		Isolation:   tenant.IsolationPrefix,
		AdminCSRPEM: validAdminCSR(t),
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: users, Roles: roles,
		Signer: &fakeSigner{cert: realCertPEM(t)}, Binder: &fakeBinder{},
		TrustDomain: onboardingTrustDomain,
		Homes:       homes, Logger: discardLogger(),
	})
	var want *TenantHomeProvisionError
	if !errors.As(err, &want) || !errors.Is(err, homeErr) {
		t.Fatalf("err = %v, want *TenantHomeProvisionError wrapping the home error", err)
	}
	if len(users.createCalls) != 0 || roles.createCount.Load() != 0 {
		t.Fatal("no identity record may be written when the home store is unavailable")
	}
}

func TestTenantHomeProvisionError_Message(t *testing.T) {
	err := &TenantHomeProvisionError{Cause: errors.New("no quorum")}
	if got, want := err.Error(), "bootstrap: tenant home store provisioning failed: no quorum"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}
