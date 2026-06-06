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

package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestErrorMessagesAndUnwrap exercises the Error() strings and Unwrap()
// behavior of every typed error so the contract (message prefix + cause
// chaining) is regression-protected.
func TestErrorMessagesAndUnwrap(t *testing.T) {
	cause := errors.New("boom")

	sentinelOnly := []struct {
		err  error
		want string
	}{
		{&MissingLoggerError{}, "missing logger"},
		{&MissingBarriersError{}, "missing barrier registry"},
		{&MissingPassphraseError{}, "passphrase"},
		{&EmptyTenantIDError{}, "empty tenant id"},
		{&MissingShamirConfigError{}, "shamir"},
		{&UnsupportedStrategyError{Strategy: "tpm2"}, "tpm2"},
	}
	for _, tc := range sentinelOnly {
		if !strings.Contains(tc.err.Error(), tc.want) {
			t.Errorf("%T.Error() = %q, want substring %q", tc.err, tc.err.Error(), tc.want)
		}
	}

	wrapped := []error{
		&SystemBarrierInitError{Cause: cause},
		&FirstBootError{Cause: cause},
		&TenantRecordCreationError{Cause: cause},
		&TenantAdminRoleCreationError{Cause: cause},
		&TenantAdminUserCreationError{Cause: cause},
		&TenantAdminCertError{Cause: cause},
		&TenantBarrierRegistrationError{Cause: cause},
		&TenantBarrierInitError{Cause: cause},
	}
	for _, err := range wrapped {
		if !strings.HasPrefix(err.Error(), "bootstrap:") {
			t.Errorf("%T.Error() = %q, want bootstrap: prefix", err, err.Error())
		}
		if !errors.Is(err, cause) {
			t.Errorf("%T does not unwrap to its cause", err)
		}
	}
}

// TestProvisionTenant_MissingLoggerRejected covers the logger guard on the
// provisioning path.
func TestProvisionTenant_MissingLoggerRejected(t *testing.T) {
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{TenantID: "acme"}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
	})
	var want *MissingLoggerError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *MissingLoggerError", err)
	}
}

// TestProvisionTenant_RoleSeedErrorWrapped covers the tenant-role seed error
// path.
func TestProvisionTenant_RoleSeedErrorWrapped(t *testing.T) {
	sentinel := errors.New("role write failed")
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{TenantID: "acme"}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{createErr: sentinel},
		Logger: discardLogger(),
	})
	var want *TenantAdminRoleCreationError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *TenantAdminRoleCreationError", err)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("err does not unwrap to sentinel: %v", err)
	}
}

// TestProvisionTenant_AdminUserErrorWrapped covers the tenant-admin user
// creation error path.
func TestProvisionTenant_AdminUserErrorWrapped(t *testing.T) {
	sentinel := errors.New("user write failed")
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{TenantID: "acme"}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{createErr: sentinel}, Roles: &fakeRoles{},
		Logger: discardLogger(),
	})
	var want *TenantAdminUserCreationError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *TenantAdminUserCreationError", err)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("err does not unwrap to sentinel: %v", err)
	}
}

// TestProvisionTenant_CertErrorWrapped covers the tenant-admin cert enrollment
// error path (CA failure).
func TestProvisionTenant_CertErrorWrapped(t *testing.T) {
	sentinel := errors.New("CA sealed")
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{TenantID: "acme"}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		CA: &fakeCA{err: sentinel}, Logger: discardLogger(),
	})
	var want *TenantAdminCertError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *TenantAdminCertError", err)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("err does not unwrap to sentinel: %v", err)
	}
}

// TestProvisionTenant_InvalidCertPEMWrapped covers the SPKI-thumbprint parse
// failure path (CA returns non-PEM bytes).
func TestProvisionTenant_InvalidCertPEMWrapped(t *testing.T) {
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{TenantID: "acme"}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		CA: &fakeCA{cert: []byte("not a pem"), key: []byte("k")}, Logger: discardLogger(),
	})
	var want *TenantAdminCertError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *TenantAdminCertError", err)
	}
}

// TestInitializeTenantBarrier_UnsupportedStrategyRejected covers the strategy
// validation guard on the tenant self-service path.
func TestInitializeTenantBarrier_UnsupportedStrategyRejected(t *testing.T) {
	reg := newRegistry(t, seal.NewNoneStrategy())
	err := InitializeTenantBarrier(context.Background(), "acme", seal.StrategyID("vault"), seal.Credentials{}, Dependencies{
		Barriers: reg, Logger: discardLogger(),
	})
	var want *UnsupportedStrategyError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *UnsupportedStrategyError", err)
	}
}

// TestInitializeTenantBarrier_DoubleInitWrapped covers the init error wrap:
// initializing an already-initialized tenant barrier returns a wrapped
// TenantBarrierInitError.
func TestInitializeTenantBarrier_DoubleInitWrapped(t *testing.T) {
	reg := newRegistry(t, seal.NewNoneStrategy())
	deps := Dependencies{Barriers: reg, Logger: discardLogger()}

	if err := InitializeTenantBarrier(context.Background(), "acme", StrategyNone, seal.Credentials{}, deps); err != nil {
		t.Fatalf("first init: %v", err)
	}
	// Second init on the same (now-registered, already-initialized) barrier.
	err := InitializeTenantBarrier(context.Background(), "acme", StrategyNone, seal.Credentials{}, deps)
	var want *TenantBarrierInitError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *TenantBarrierInitError", err)
	}
}
