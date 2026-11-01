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

package service

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/bootstrap"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantadmin"
)

// newOnboardingSOAuthenticator is the production SO PIN store over memory.
func newOnboardingSOAuthenticator(t *testing.T) *auth.SOAuthenticator {
	t.Helper()
	a, err := auth.NewSOAuthenticator(kvstore.NewMemoryKVStore(), audit.NewRingBuffer(64), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("auth.NewSOAuthenticator: %v", err)
	}
	return a
}

func TestOnboardTenant_SO_EnrolledAndAnswered(t *testing.T) {
	ctx := context.Background()
	deps := newOnboardingDeps(t)
	pins := newOnboardingSOAuthenticator(t)
	deps.SOPINs = pins
	svc := NewTenantOnboardingService(deps)

	result, err := svc.OnboardTenant(ctx, &OnboardTenantRequest{
		ID: "acme", AdminUsername: "admin", AdminCSRPEM: onboardingCSR(t),
		SOUsername: "officer", SOCSRPEM: onboardingCSR(t), SOPIN: []byte("so-pin-1234"),
	})
	if err != nil {
		t.Fatalf("OnboardTenant: %v", err)
	}
	so := result.SO
	if so == nil || so.Username != "officer" || so.UserID == 0 || len(so.CertPEM) == 0 || so.Serial == "" || so.Thumbprint == "" {
		t.Fatalf("SO = %+v, want the officer's credential", so)
	}
	if so.Thumbprint == result.Admin.Thumbprint || so.UserID == result.Admin.UserID {
		t.Fatalf("SO = %+v, admin = %+v; want two principals", so, result.Admin)
	}
	account, err := deps.Users.GetUser(ctx, so.UserID)
	if err != nil {
		t.Fatalf("GetUser(SO): %v", err)
	}
	if want := []string{role.SystemRoleID("acme", role.RoleNameSO)}; !slices.Equal(account.RoleIDs, want) {
		t.Fatalf("SO RoleIDs = %v, want %v", account.RoleIDs, want)
	}
	if err := pins.VerifySOPIN(ctx, "acme", []byte("so-pin-1234")); err != nil {
		t.Fatalf("VerifySOPIN = %v, want the SO's PIN committed", err)
	}
}

func TestOnboardTenant_NoSOAnswersNoSO(t *testing.T) {
	deps := newOnboardingDeps(t)
	deps.SOPINs = newOnboardingSOAuthenticator(t)
	result, err := NewTenantOnboardingService(deps).OnboardTenant(context.Background(), &OnboardTenantRequest{
		ID: "acme", AdminUsername: "admin", AdminCSRPEM: onboardingCSR(t),
	})
	if err != nil || result.SO != nil {
		t.Fatalf("OnboardTenant = %+v, %v; want no SO", result, err)
	}
}

func TestOnboardTenant_SO_PartialSetIsRefused(t *testing.T) {
	deps := newOnboardingDeps(t)
	deps.SOPINs = newOnboardingSOAuthenticator(t)
	svc := NewTenantOnboardingService(deps)
	for _, req := range []*OnboardTenantRequest{
		{SOPIN: []byte("pin")},
		{SOUsername: "officer", SOPIN: []byte("pin")},
		{SOChainPEM: []byte("chain")},
	} {
		req.ID, req.AdminUsername, req.AdminCSRPEM = "acme", "admin", onboardingCSR(t)
		var incomplete *bootstrap.IncompleteTenantSOError
		if _, err := svc.OnboardTenant(context.Background(), req); !errors.As(err, &incomplete) {
			t.Fatalf("OnboardTenant(%+v) = %v, want *bootstrap.IncompleteTenantSOError", req, err)
		}
	}
}

func TestOnboardTenant_SO_RefusedUntilThePINSeamIsAttached(t *testing.T) {
	svc := NewTenantOnboardingService(newOnboardingDeps(t))
	req := func() *OnboardTenantRequest {
		return &OnboardTenantRequest{
			ID: "acme", AdminUsername: "admin", AdminCSRPEM: onboardingCSR(t),
			SOUsername: "officer", SOCSRPEM: onboardingCSR(t), SOPIN: []byte("pin"),
		}
	}
	var seam *bootstrap.MissingProvisioningSeamError
	if _, err := svc.OnboardTenant(context.Background(), req()); !errors.As(err, &seam) {
		t.Fatalf("OnboardTenant before AttachSOPINs = %v, want *bootstrap.MissingProvisioningSeamError", err)
	}
	pins := newOnboardingSOAuthenticator(t)
	svc.AttachSOPINs(pins)
	if _, err := svc.OnboardTenant(context.Background(), req()); err != nil {
		t.Fatalf("OnboardTenant after AttachSOPINs: %v", err)
	}
	if err := pins.VerifySOPIN(context.Background(), "acme", []byte("pin")); err != nil {
		t.Fatalf("VerifySOPIN = %v, want the attached seam used", err)
	}
}

func TestOnboardTenant_SO_MatchingTheAdminIsRefused(t *testing.T) {
	deps := newOnboardingDeps(t)
	deps.SOPINs = newOnboardingSOAuthenticator(t)
	deps.SeparationOfDuties = true
	svc := NewTenantOnboardingService(deps)
	csr := onboardingCSR(t)
	var match *bootstrap.SOMatchesAdminError
	_, err := svc.OnboardTenant(context.Background(), &OnboardTenantRequest{
		ID: "acme", AdminUsername: "admin", AdminCSRPEM: csr, SOUsername: "officer", SOCSRPEM: csr, SOPIN: []byte("pin"),
	})
	if !errors.As(err, &match) || match.Field != "key" {
		t.Fatalf("OnboardTenant(SO key = admin key) = %v, want *bootstrap.SOMatchesAdminError{key}", err)
	}
	if _, err := deps.Tenants.GetTenant(context.Background(), "acme"); err == nil {
		t.Fatal("tenant acme exists after a refused create")
	}
}

func TestOnboardTenant_SO_StandardPostureTheAdminMayBeTheSO(t *testing.T) {
	deps := newOnboardingDeps(t)
	deps.SOPINs = newOnboardingSOAuthenticator(t)
	svc := NewTenantOnboardingService(deps)
	csr := onboardingCSR(t)
	if _, err := svc.OnboardTenant(context.Background(), &OnboardTenantRequest{
		ID: "acme", AdminUsername: "admin", AdminCSRPEM: csr, SOUsername: "admin", SOCSRPEM: csr, SOPIN: []byte("pin"),
	}); err != nil {
		t.Fatalf("OnboardTenant(SO = admin, standard posture) = %v, want nil", err)
	}
	if _, err := deps.Tenants.GetTenant(context.Background(), "acme"); err != nil {
		t.Fatalf("GetTenant(acme) = %v, want the tenant created", err)
	}
}

func TestOnboardTenant_SO_StandardPostureHalfTheAdminIsRefused(t *testing.T) {
	deps := newOnboardingDeps(t)
	deps.SOPINs = newOnboardingSOAuthenticator(t)
	svc := NewTenantOnboardingService(deps)
	csr := onboardingCSR(t)
	var partly *bootstrap.SOPartlyMatchesAdminError
	_, err := svc.OnboardTenant(context.Background(), &OnboardTenantRequest{
		ID: "acme", AdminUsername: "admin", AdminCSRPEM: csr, SOUsername: "officer", SOCSRPEM: csr, SOPIN: []byte("pin"),
	})
	if !errors.As(err, &partly) || partly.Field != "key" {
		t.Fatalf("OnboardTenant(SO key = admin key, another name) = %v, want *bootstrap.SOPartlyMatchesAdminError{key}", err)
	}
}

func TestOnboardTenant_SO_PendingRecordedUnderThePendingSOKey(t *testing.T) {
	ctx := context.Background()
	deps := newOnboardingDeps(t)
	store := kvstore.NewMemoryKVStore()
	deps.Pending = tenantadmin.NewKVStore(store)
	deps.PendingSOs = tenantadmin.NewSOKVStore(store)
	deps.SOPINs = newOnboardingSOAuthenticator(t)
	svc := NewTenantOnboardingService(deps)

	result, err := svc.OnboardTenant(ctx, &OnboardTenantRequest{
		ID: "acme", AdminUsername: "admin", AdminCSRPEM: onboardingCSR(t), IsolationType: tenant.IsolationVolume,
		SOUsername: "officer", SOCSRPEM: onboardingCSR(t), SOPIN: []byte("pin"),
	})
	if err != nil {
		t.Fatalf("OnboardTenant: %v", err)
	}
	if result.SO == nil || !result.SO.AwaitsBarrierInit || result.SO.UserID != 0 {
		t.Fatalf("SO = %+v, want it awaiting the barrier's init", result.SO)
	}
	rec, err := deps.PendingSOs.Get(ctx, "acme")
	if err != nil || rec == nil || rec.Username != "officer" || rec.Thumbprint != result.SO.Thumbprint {
		t.Fatalf("pending SO = %+v, %v; want the officer recorded", rec, err)
	}
	if account, err := deps.Users.GetUserByUsername(ctx, "acme", "officer"); err == nil {
		t.Fatalf("GetUserByUsername(officer) = %+v, want no account before the barrier is up", account)
	}
	if rec, err := deps.Pending.Get(ctx, "acme"); err != nil || rec == nil || rec.Username != "admin" {
		t.Fatalf("pending admin = %+v, %v; want the admin recorded beside the SO", rec, err)
	}
}

func TestOnboardTenantRequest_SO_SealsThePINAndWipesTheWireBytes(t *testing.T) {
	req := &OnboardTenantRequest{SOUsername: "officer", SOCSRPEM: []byte("csr"), SOPIN: []byte("so-pin-1234")}
	cfg, err := req.so(context.Background())
	if err != nil {
		t.Fatalf("so: %v", err)
	}
	t.Cleanup(cfg.PIN.Destroy)
	if cfg.Username != "officer" || string(cfg.CSRPEM) != "csr" {
		t.Fatalf("so = %+v, want the request's SO fields", cfg)
	}
	if cfg.PIN == nil || !cfg.PIN.IsAlive() {
		t.Fatal("so: PIN not sealed into a live Secret")
	}
	if err := cfg.PIN.WithBytes(context.Background(), func(b []byte) error {
		if string(b) != "so-pin-1234" {
			t.Fatalf("sealed PIN = %q, want so-pin-1234", b)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithBytes: %v", err)
	}
	if slices.ContainsFunc(req.SOPIN, func(b byte) bool { return b != 0 }) {
		t.Fatalf("request SOPIN = %q after sealing, want it wiped", req.SOPIN)
	}
}

func TestOnboardTenantRequest_SO_NoSOAndNoPIN(t *testing.T) {
	cfg, err := (&OnboardTenantRequest{}).so(context.Background())
	if err != nil || cfg != nil {
		t.Fatalf("so(no SO) = %+v, %v; want nil, nil", cfg, err)
	}
	cfg, err = (&OnboardTenantRequest{SOUsername: "officer"}).so(context.Background())
	if err != nil || cfg == nil || cfg.PIN != nil {
		t.Fatalf("so(no PIN) = %+v, %v; want a config with a nil PIN", cfg, err)
	}
}

func TestOnboardTenantRequest_SO_SealFailureIsTyped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (&OnboardTenantRequest{SOUsername: "officer", SOPIN: []byte("pin")}).so(ctx)
	var enrol *bootstrap.TenantSOEnrollmentError
	if !errors.As(err, &enrol) || enrol.Op != "PIN seal" {
		t.Fatalf("so(cancelled) = %v, want *bootstrap.TenantSOEnrollmentError{PIN seal}", err)
	}
}
