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
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantadmin"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// fakeSOPINs records every SetSOPIN call, and answers err.
type fakeSOPINs struct {
	tenants []string
	pins    [][]byte
	err     error
}

func (f *fakeSOPINs) SetSOPIN(_ context.Context, tenantID string, pin []byte) error {
	f.tenants = append(f.tenants, tenantID)
	f.pins = append(f.pins, bytes.Clone(pin))
	return f.err
}

var _ SOPINSetter = (*fakeSOPINs)(nil)
var _ SOPINSetter = (*auth.SOAuthenticator)(nil)

// soConfig is a complete TenantSOConfig over a fresh key.
func soConfig(t *testing.T) *TenantSOConfig {
	t.Helper()
	return &TenantSOConfig{Username: "officer", CSRPEM: validAdminCSR(t), PIN: []byte("so-pin-1234")}
}

// provisionWithSO provisions tenant acme, prefix isolation, with so.
func provisionWithSO(t *testing.T, so *TenantSOConfig, users firstboot.UserCreator, deps Dependencies) (*TenantProvisionResult, error) {
	t.Helper()
	deps.Users = users
	if deps.Tenants == nil {
		deps.Tenants = &fakeTenants{}
	}
	if deps.Roles == nil {
		deps.Roles = &fakeRoles{}
	}
	if deps.Signer == nil {
		deps.Signer = &fakeSigner{cert: realCertPEM(t)}
	}
	deps.TrustDomain = onboardingTrustDomain
	deps.Logger = discardLogger()
	return ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		Isolation:   tenant.IsolationPrefix,
		AdminCSRPEM: validAdminCSR(t),
		SO:          so,
	}, deps)
}

func TestProvisionTenant_SO_EnrolledWithTheSORoleAlone(t *testing.T) {
	users := &fakeCertBindingUsers{}
	pins := &fakeSOPINs{}
	result, err := provisionWithSO(t, soConfig(t), users, Dependencies{SOPINs: pins})
	if err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	if len(users.createCalls) != 2 {
		t.Fatalf("createCalls = %d, want the admin and the SO", len(users.createCalls))
	}
	so := users.createCalls[1]
	want := []string{role.SystemRoleID("acme", role.RoleNameSO)}
	if so.Username != "officer" || !slices.Equal(so.RoleIDs, want) {
		t.Fatalf("SO account = %q %v, want officer %v", so.Username, so.RoleIDs, want)
	}
	if !so.WithoutEmailLoginKey || so.Pending {
		t.Fatalf("SO account = %+v, want a certificate-only active account", so)
	}
	if result.SOUserID == 0 || result.SOUsername != "officer" || len(result.SOCertPEM) == 0 || result.SOCertThumbprint == "" {
		t.Fatalf("result SO = %d %q %d bytes %q, want the SO's credential", result.SOUserID, result.SOUsername, len(result.SOCertPEM), result.SOCertThumbprint)
	}
	if len(users.bindCalls) != 2 || users.bindCalls[1] != result.SOUserID {
		t.Fatalf("bindCalls = %v, want the SO's key bound to %d", users.bindCalls, result.SOUserID)
	}
	if len(pins.tenants) != 1 || pins.tenants[0] != "acme" || string(pins.pins[0]) != "so-pin-1234" {
		t.Fatalf("SetSOPIN calls = %v, want one for acme with the SO's PIN", pins.tenants)
	}
}

func TestProvisionTenant_NoSOKeepsTodaysBehaviour(t *testing.T) {
	users := &fakeCertBindingUsers{}
	result, err := provisionWithSO(t, nil, users, Dependencies{})
	if err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	if len(users.createCalls) != 1 || result.SOUsername != "" || result.SOUserID != 0 {
		t.Fatalf("createCalls = %d, SO = %q; want the admin alone", len(users.createCalls), result.SOUsername)
	}
}

func TestProvisionTenant_SO_UsesTheInjectedSignerAndBinder(t *testing.T) {
	signer := &fakeSigner{cert: realCertPEM(t)}
	binder := &fakeBinder{}
	so := soConfig(t)
	result, err := provisionWithSO(t, so, &fakeUsers{}, Dependencies{Signer: signer, Binder: binder, SOPINs: &fakeSOPINs{}})
	if err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	if signer.called.Load() != 2 || !bytes.Equal(signer.lastCSR, so.CSRPEM) {
		t.Fatalf("signer calls = %d, last CSR is the SO's = %v; want the SO's CSR signed by the injected signer", signer.called.Load(), bytes.Equal(signer.lastCSR, so.CSRPEM))
	}
	if binder.calls != 2 || binder.userID != result.SOUserID || binder.tenantID != "acme" {
		t.Fatalf("binder = %+v, want the SO bound last through the injected binder", binder)
	}
}

func TestProvisionTenant_SO_UsesTheInjectedBYOCVerification(t *testing.T) {
	root := newOnboardingRoot(t, "Acme Root")
	so := &TenantSOConfig{Username: "officer", CertPEM: onboardingPEM(root.clientLeaf(t, "officer", "acme")), PIN: []byte("pin")}
	signer := &fakeSigner{cert: realCertPEM(t)}
	users := &fakeCertBindingUsers{}
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID: "acme", Isolation: tenant.IsolationPrefix,
		AdminCSRPEM: validAdminCSR(t), TrustAnchorsPEM: onboardingPEM(root.cert), SO: so,
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: users, Roles: &fakeRoles{}, Signer: signer,
		TrustAnchors: trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore()), TrustDomain: onboardingTrustDomain,
		SOPINs: &fakeSOPINs{}, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	if signer.called.Load() != 1 {
		t.Fatalf("signer calls = %d, want the admin's CSR alone signed (the SO brought its own)", signer.called.Load())
	}
	if len(users.bindCalls) != 2 {
		t.Fatalf("bindCalls = %v, want the SO's own certificate bound", users.bindCalls)
	}
}

func TestProvisionTenant_SO_RefusedBeforeTheTenantIsCreated(t *testing.T) {
	sameKey := validAdminCSR(t)
	cases := []struct {
		name  string
		admin []byte
		so    func(*TenantSOConfig)
		deps  Dependencies
		check func(error) bool
	}{
		{"no username", nil, func(s *TenantSOConfig) { s.Username = "" }, Dependencies{SOPINs: &fakeSOPINs{}}, isIncomplete("so_username")},
		{"no credential", nil, func(s *TenantSOConfig) { s.CSRPEM = nil }, Dependencies{SOPINs: &fakeSOPINs{}}, isIncomplete("so_csr_pem or so_cert_pem")},
		{"no PIN", nil, func(s *TenantSOConfig) { s.PIN = nil }, Dependencies{SOPINs: &fakeSOPINs{}}, isIncomplete("so_pin")},
		{"both credentials", nil, func(s *TenantSOConfig) { s.CertPEM = []byte("x") }, Dependencies{SOPINs: &fakeSOPINs{}}, errAs[*ConflictingSOCredentialError]},
		{"strict: admin's username", nil, func(s *TenantSOConfig) { s.Username = "Admin" }, Dependencies{SOPINs: &fakeSOPINs{}, SeparationOfDuties: true}, isMatch("username")},
		{"strict: admin's key", sameKey, func(s *TenantSOConfig) { s.CSRPEM = sameKey }, Dependencies{SOPINs: &fakeSOPINs{}, SeparationOfDuties: true}, isMatch("key")},
		{"strict: the admin itself", sameKey, func(s *TenantSOConfig) { s.Username, s.CSRPEM = "admin", sameKey }, Dependencies{SOPINs: &fakeSOPINs{}, SeparationOfDuties: true}, isMatch("username")},
		{"standard: admin's username, another key", nil, func(s *TenantSOConfig) { s.Username = "Admin" }, Dependencies{SOPINs: &fakeSOPINs{}}, isPartlyMatch("username")},
		{"standard: admin's key, another name", sameKey, func(s *TenantSOConfig) { s.CSRPEM = sameKey }, Dependencies{SOPINs: &fakeSOPINs{}}, isPartlyMatch("key")},
		{"unparsable CSR", nil, func(s *TenantSOConfig) { s.CSRPEM = []byte("not a csr") }, Dependencies{SOPINs: &fakeSOPINs{}}, errAs[*SOCredentialRejectedError]},
		{"uncertifiable name", nil, func(s *TenantSOConfig) { s.Username = "bad/name" }, Dependencies{SOPINs: &fakeSOPINs{}}, errAs[*SOCredentialRejectedError]},
		{"no PIN seam", nil, func(*TenantSOConfig) {}, Dependencies{}, errAs[*MissingProvisioningSeamError]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			so := soConfig(t)
			tc.so(so)
			tenants := &fakeTenants{}
			users := &fakeCertBindingUsers{}
			tc.deps.Tenants = tenants
			admin := tc.admin
			if admin == nil {
				admin = validAdminCSR(t)
			}
			tc.deps.Users, tc.deps.Roles = users, &fakeRoles{}
			tc.deps.Signer = &fakeSigner{cert: realCertPEM(t)}
			tc.deps.TrustDomain, tc.deps.Logger = onboardingTrustDomain, discardLogger()
			_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
				TenantID: "acme", Isolation: tenant.IsolationPrefix, AdminCSRPEM: admin, SO: so,
			}, tc.deps)
			if !tc.check(err) {
				t.Fatalf("err = %v (%T), want the %s refusal", err, err, tc.name)
			}
			if len(tenants.createCalls) != 0 || len(users.createCalls) != 0 {
				t.Fatalf("tenant creates = %d, user creates = %d; want nothing written", len(tenants.createCalls), len(users.createCalls))
			}
		})
	}
}

func isIncomplete(field string) func(error) bool {
	return func(err error) bool {
		var e *IncompleteTenantSOError
		return errors.As(err, &e) && e.Field == field
	}
}

func isMatch(field string) func(error) bool {
	return func(err error) bool {
		var e *SOMatchesAdminError
		return errors.As(err, &e) && e.Field == field
	}
}

func isPartlyMatch(field string) func(error) bool {
	return func(err error) bool {
		var e *SOPartlyMatchesAdminError
		return errors.As(err, &e) && e.Field == field
	}
}

func errAs[E error](err error) bool {
	var e E
	return errors.As(err, &e)
}

func TestPlanSO_RefusesADeferredAdmin(t *testing.T) {
	_, err := planSO(TenantProvisionConfig{TenantID: "acme", SO: soConfig(t)}, Dependencies{SOPINs: &fakeSOPINs{}}, AdminEnrollDeferred, "admin", &adminPlan{}, false)
	var conflict *ConflictingAdminEnrollmentError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want *ConflictingAdminEnrollmentError", err)
	}
}

func TestPlanSO_NoSOPlansNothing(t *testing.T) {
	p, err := planSO(TenantProvisionConfig{TenantID: "acme"}, Dependencies{}, AdminEnrollCert, "admin", &adminPlan{}, false)
	if p != nil || err != nil {
		t.Fatalf("planSO = %v, %v; want nil, nil", p, err)
	}
}

func TestPlanSO_PendingNeedsPendingSOsAndAValidName(t *testing.T) {
	deps := Dependencies{SOPINs: &fakeSOPINs{}, TrustDomain: onboardingTrustDomain, PendingAdminSigner: &fakeSigner{}}
	_, err := planSO(TenantProvisionConfig{TenantID: "acme", SO: soConfig(t)}, deps, AdminEnrollCert, "admin", &adminPlan{}, true)
	if !errAs[*MissingProvisioningSeamError](err) {
		t.Fatalf("err = %v, want *MissingProvisioningSeamError for PendingSOs", err)
	}
	deps.PendingSOs = tenantadmin.NewSOKVStore(kvstore.NewMemoryKVStore())
	so := soConfig(t)
	so.Username = "bad name"
	_, err = planSO(TenantProvisionConfig{TenantID: "acme", SO: so}, deps, AdminEnrollCert, "admin", &adminPlan{}, true)
	if !errAs[*InvalidAdminUsernameError](err) {
		t.Fatalf("err = %v, want *InvalidAdminUsernameError", err)
	}
}

func TestPlanSO_PropagatesAMissingSigner(t *testing.T) {
	_, err := planSO(TenantProvisionConfig{TenantID: "acme", SO: soConfig(t)}, Dependencies{SOPINs: &fakeSOPINs{}, TrustDomain: onboardingTrustDomain}, AdminEnrollCert, "admin", &adminPlan{}, false)
	if err == nil || errAs[*SOCredentialRejectedError](err) {
		t.Fatalf("err = %v, want the missing-signer error unchanged", err)
	}
}

func TestProvisionTenant_SO_PINCommittedSoTheSOAuthenticatorAdmitsIt(t *testing.T) {
	verifier, err := auth.NewSOAuthenticator(kvstore.NewMemoryKVStore(), audit.NewRingBuffer(64), discardLogger())
	if err != nil {
		t.Fatalf("NewSOAuthenticator: %v", err)
	}
	if _, err := provisionWithSO(t, soConfig(t), &fakeCertBindingUsers{}, Dependencies{SOPINs: verifier}); err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	if err := verifier.VerifySOPIN(context.Background(), "acme", []byte("so-pin-1234")); err != nil {
		t.Fatalf("VerifySOPIN(the SO's PIN) = %v, want nil", err)
	}
	if err := verifier.VerifySOPIN(context.Background(), "acme", []byte("wrong")); !errors.Is(err, auth.ErrSOPINInvalid) {
		t.Fatalf("VerifySOPIN(wrong) = %v, want ErrSOPINInvalid", err)
	}
}

func TestProvisionTenant_SO_ExclusivePINFailureUndoesEverything(t *testing.T) {
	users := &fakeCertBindingUsers{}
	tenants := &fakeTenants{}
	pinErr := errors.New("pin store down")
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID: "acme", Isolation: tenant.IsolationPrefix, AdminCSRPEM: validAdminCSR(t), SO: soConfig(t), Exclusive: true,
	}, Dependencies{
		Tenants: tenants, Users: users, Roles: &fakeRoles{}, Signer: &fakeSigner{cert: realCertPEM(t)},
		TrustDomain: onboardingTrustDomain, SOPINs: &fakeSOPINs{err: pinErr}, Logger: discardLogger(),
	})
	var undone *ProvisioningUndoneError
	var enrol *TenantSOEnrollmentError
	if !errors.As(err, &undone) || !errors.As(err, &enrol) || !errors.Is(err, pinErr) {
		t.Fatalf("err = %v, want a ProvisioningUndoneError over the SO's PIN commit", err)
	}
	if len(users.deleteCalls) != 2 || len(tenants.undoCalls) != 1 {
		t.Fatalf("deleted users = %v, tenant undos = %v; want both accounts and the tenant undone", users.deleteCalls, tenants.undoCalls)
	}
}

func TestProvisionTenant_SO_ExclusiveRefusesACommittedPIN(t *testing.T) {
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID: "acme", Isolation: tenant.IsolationPrefix, AdminCSRPEM: validAdminCSR(t), SO: soConfig(t), Exclusive: true,
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeCertBindingUsers{}, Roles: &fakeRoles{}, Signer: &fakeSigner{cert: realCertPEM(t)},
		TrustDomain: onboardingTrustDomain, SOPINs: &fakeSOPINs{err: auth.ErrSOPINAlreadySet}, Logger: discardLogger(),
	})
	if !errors.Is(err, auth.ErrSOPINAlreadySet) {
		t.Fatalf("err = %v, want ErrSOPINAlreadySet refused", err)
	}
}

func TestProvisionTenant_SO_ConvergentReRunKeepsTheAccountAndPIN(t *testing.T) {
	users := &fakeCertBindingUsers{fakeUsers: fakeUsers{exists: true}}
	result, err := provisionWithSO(t, soConfig(t), users, Dependencies{SOPINs: &fakeSOPINs{err: auth.ErrSOPINAlreadySet}})
	if err != nil {
		t.Fatalf("ProvisionTenant re-run: %v", err)
	}
	if result.SOUsername != "officer" || result.SOUserID != 0 || len(result.SOCertPEM) != 0 || len(users.bindCalls) != 0 {
		t.Fatalf("result SO = %q %d %d bytes, binds %v; want the existing SO left as it is", result.SOUsername, result.SOUserID, len(result.SOCertPEM), users.bindCalls)
	}
}

func TestProvisionTenant_SO_AccountCreationFailureIsTyped(t *testing.T) {
	createErr := errors.New("user store down")
	users := &failNthCreateUsers{fail: 2, err: createErr}
	_, err := provisionWithSO(t, soConfig(t), users, Dependencies{SOPINs: &fakeSOPINs{}})
	var enrol *TenantSOEnrollmentError
	if !errors.As(err, &enrol) || enrol.Op != "account creation" || !errors.Is(err, createErr) {
		t.Fatalf("err = %v, want *TenantSOEnrollmentError{account creation}", err)
	}
}

// failNthCreateUsers fails its fail-th CreateUser with err.
type failNthCreateUsers struct {
	fakeCertBindingUsers
	fail int
	err  error
	n    int
}

func (f *failNthCreateUsers) CreateUser(ctx context.Context, req *user.CreateUserRequest) (*user.User, error) {
	f.n++
	if f.n == f.fail {
		return nil, f.err
	}
	return f.fakeCertBindingUsers.CreateUser(ctx, req)
}

func TestProvisionTenant_SO_BarrierIsolation_RecordsThePendingSOAndCompletesIt(t *testing.T) {
	ctx := context.Background()
	store := kvstore.NewMemoryKVStore()
	admins := tenantadmin.NewKVStore(store)
	sos := tenantadmin.NewSOKVStore(store)
	pins := &fakeSOPINs{}
	users := &fakeCertBindingUsers{}
	result, err := ProvisionTenant(ctx, TenantProvisionConfig{TenantID: "acme", AdminCSRPEM: validAdminCSR(t), SO: soConfig(t)}, Dependencies{
		Tenants: &fakeTenants{}, Users: users, Roles: &fakeRoles{},
		PendingAdminSigner: &fakeSigner{cert: realCertPEM(t)}, TrustDomain: onboardingTrustDomain,
		PendingAdmins: admins, PendingSOs: sos, SOPINs: pins, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	if !result.SOAwaitsBarrierInit || result.SOUserID != 0 || len(users.createCalls) != 0 {
		t.Fatalf("SO awaits = %v, id %d, creates %d; want the SO pending and no account", result.SOAwaitsBarrierInit, result.SOUserID, len(users.createCalls))
	}
	rec, err := sos.Get(ctx, "acme")
	if err != nil || rec == nil || rec.Username != "officer" || rec.Thumbprint != result.SOCertThumbprint {
		t.Fatalf("pending SO = %+v, %v; want officer under %q", rec, err, result.SOCertThumbprint)
	}
	if len(pins.tenants) != 1 {
		t.Fatalf("SetSOPIN calls = %d, want the PIN committed at create", len(pins.tenants))
	}

	ok, err := CompletePendingTenantAdmin(ctx, "acme", Dependencies{
		PendingAdmins: admins, PendingSOs: sos, Users: users, Roles: &fakeRoles{}, Logger: discardLogger(),
	})
	if err != nil || !ok {
		t.Fatalf("CompletePendingTenantAdmin = %v, %v; want true, nil", ok, err)
	}
	if len(users.createCalls) != 2 || users.createCalls[0].Username != "admin" || users.createCalls[1].Username != "officer" {
		t.Fatalf("createCalls = %+v, want the admin then the SO", users.createCalls)
	}
	if want := []string{role.SystemRoleID("acme", role.RoleNameSO)}; !slices.Equal(users.createCalls[1].RoleIDs, want) {
		t.Fatalf("SO RoleIDs = %v, want %v", users.createCalls[1].RoleIDs, want)
	}
	if rec, _ := sos.Get(ctx, "acme"); rec != nil {
		t.Fatalf("pending SO after completion = %+v, want it consumed", rec)
	}
}

func TestProvisionTenant_SO_BarrierIsolation_AutoUnsealLeavesTheSOToTheCompletion(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	result, err := ProvisionTenant(context.Background(), TenantProvisionConfig{TenantID: "acme", AdminCSRPEM: validAdminCSR(t), SO: soConfig(t)}, Dependencies{
		Tenants: &fakeTenants{autoUnseal: true}, Users: &fakeCertBindingUsers{}, Roles: &fakeRoles{},
		PendingAdminSigner: &fakeSigner{cert: realCertPEM(t)}, TrustDomain: onboardingTrustDomain,
		PendingAdmins: tenantadmin.NewKVStore(store), PendingSOs: tenantadmin.NewSOKVStore(store), SOPINs: &fakeSOPINs{}, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	if result.SOAwaitsBarrierInit || !result.AdminCompletionDue {
		t.Fatalf("SO awaits = %v, completion due = %v; want false, true", result.SOAwaitsBarrierInit, result.AdminCompletionDue)
	}
}

func TestProvisionTenant_SO_BarrierIsolation_ReRunKeepsTheRecordedSO(t *testing.T) {
	ctx := context.Background()
	store := kvstore.NewMemoryKVStore()
	sos := tenantadmin.NewSOKVStore(store)
	recorded := &tenantadmin.Pending{Username: "officer", Thumbprint: "kept", NotAfter: time.Now().Add(time.Hour).UTC(), Serial: "7"}
	if err := sos.Create(ctx, "acme", recorded); err != nil {
		t.Fatal(err)
	}
	signer := &fakeSigner{cert: realCertPEM(t)}
	result, err := ProvisionTenant(ctx, TenantProvisionConfig{TenantID: "acme", AdminCSRPEM: validAdminCSR(t), SO: soConfig(t)}, Dependencies{
		Tenants: &fakeTenants{createAlreadyErr: true}, Users: &fakeCertBindingUsers{}, Roles: &fakeRoles{},
		PendingAdminSigner: signer, TrustDomain: onboardingTrustDomain,
		PendingAdmins: tenantadmin.NewKVStore(store), PendingSOs: sos, SOPINs: &fakeSOPINs{err: auth.ErrSOPINAlreadySet}, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("ProvisionTenant re-run: %v", err)
	}
	if result.SOCertThumbprint != "kept" || result.SOCertSerial != "7" || signer.called.Load() != 1 {
		t.Fatalf("SO thumbprint %q serial %q, signer calls %d; want the recorded SO kept and only the admin signed", result.SOCertThumbprint, result.SOCertSerial, signer.called.Load())
	}
}

func TestProvisionTenant_SO_BarrierIsolation_PendingSOReadErrorIsTyped(t *testing.T) {
	readErr := errors.New("read failed")
	store := kvstore.NewMemoryKVStore()
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{TenantID: "acme", AdminCSRPEM: validAdminCSR(t), SO: soConfig(t)}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeCertBindingUsers{}, Roles: &fakeRoles{},
		PendingAdminSigner: &fakeSigner{cert: realCertPEM(t)}, TrustDomain: onboardingTrustDomain,
		PendingAdmins: tenantadmin.NewKVStore(store),
		PendingSOs:    &pendingAdminsFaulting{inner: tenantadmin.NewSOKVStore(store), getErr: readErr},
		SOPINs:        &fakeSOPINs{}, Logger: discardLogger(),
	})
	var enrol *TenantSOEnrollmentError
	if !errors.As(err, &enrol) || enrol.Op != "pending record read" || !errors.Is(err, readErr) {
		t.Fatalf("err = %v, want *TenantSOEnrollmentError{pending record read}", err)
	}
}

func TestProvisionTenant_SO_BarrierIsolation_PendingSORecordErrorIsTyped(t *testing.T) {
	putErr := errors.New("put failed")
	store := kvstore.NewMemoryKVStore()
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{TenantID: "acme", AdminCSRPEM: validAdminCSR(t), SO: soConfig(t)}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeCertBindingUsers{}, Roles: &fakeRoles{},
		PendingAdminSigner: &fakeSigner{cert: realCertPEM(t)}, TrustDomain: onboardingTrustDomain,
		PendingAdmins: tenantadmin.NewKVStore(store),
		PendingSOs:    &pendingAdminsFaulting{inner: tenantadmin.NewSOKVStore(store), putErr: putErr},
		SOPINs:        &fakeSOPINs{}, Logger: discardLogger(),
	})
	var enrol *TenantSOEnrollmentError
	if !errors.As(err, &enrol) || enrol.Op != "pending record record" || !errors.Is(err, putErr) {
		t.Fatalf("err = %v, want *TenantSOEnrollmentError{pending record record}", err)
	}
}

func TestCompletePendingTenantAdmin_CompletesAPendingSOAlone(t *testing.T) {
	ctx := context.Background()
	sos := tenantadmin.NewSOKVStore(kvstore.NewMemoryKVStore())
	if err := sos.Create(ctx, "acme", &tenantadmin.Pending{Username: "officer", Thumbprint: "t"}); err != nil {
		t.Fatal(err)
	}
	users := &fakeCertBindingUsers{}
	ok, err := CompletePendingTenantAdmin(ctx, "acme", Dependencies{PendingSOs: sos, Users: users, Roles: &fakeRoles{}, Logger: discardLogger()})
	if err != nil || !ok || len(users.createCalls) != 1 || users.createCalls[0].Username != "officer" {
		t.Fatalf("CompletePendingTenantAdmin = %v, %v, creates %+v; want the SO completed", ok, err, users.createCalls)
	}
}

func TestCompletePendingTenantAdmin_RefusesToAdoptAnAccountWithoutTheSORole(t *testing.T) {
	ctx := context.Background()
	sos := tenantadmin.NewSOKVStore(kvstore.NewMemoryKVStore())
	if err := sos.Create(ctx, "acme", &tenantadmin.Pending{Username: "officer", Thumbprint: "t"}); err != nil {
		t.Fatal(err)
	}
	users := &fakeUsersWithLookup{
		fakeUsers:  fakeUsers{exists: true},
		lookupUser: &user.User{ID: 9, Username: "officer", TenantID: "acme", RoleIDs: []string{role.SystemRoleID("acme", role.RoleNameAdmin)}},
	}
	binder := &fakeBinder{}
	ok, err := CompletePendingTenantAdmin(ctx, "acme", Dependencies{PendingSOs: sos, Users: users, Roles: &fakeRoles{}, Binder: binder, Logger: discardLogger()})
	var conflict *PendingAdminAccountConflictError
	if ok || !errors.As(err, &conflict) || conflict.Role != role.RoleNameSO || binder.calls != 0 {
		t.Fatalf("CompletePendingTenantAdmin = %v, %v, binds %d; want an SO-role conflict and nothing bound", ok, err, binder.calls)
	}
}

func TestCompletePendingTenantAdmin_PendingSOReadErrorIsTyped(t *testing.T) {
	readErr := errors.New("read failed")
	_, err := CompletePendingTenantAdmin(context.Background(), "acme", Dependencies{
		PendingSOs: &pendingAdminsFaulting{inner: tenantadmin.NewSOKVStore(kvstore.NewMemoryKVStore()), getErr: readErr},
		Logger:     discardLogger(),
	})
	var enrol *TenantSOEnrollmentError
	if !errors.As(err, &enrol) || !errors.Is(err, readErr) {
		t.Fatalf("err = %v, want *TenantSOEnrollmentError over the read", err)
	}
}

func TestAccountKinds_WrapEachRolesFailuresInItsOwnType(t *testing.T) {
	cause := errors.New("cause")
	admin, so := accountKinds[role.RoleNameAdmin], accountKinds[role.RoleNameSO]
	if !errAs[*TenantAdminUserCreationError](admin.createErr(cause)) || !errAs[*TenantAdminCertError](admin.certErr(cause)) || !errAs[*PendingTenantAdminError](admin.pendingErr("read", cause)) {
		t.Fatal("the admin's failures are not the admin's types")
	}
	for _, err := range []error{so.createErr(cause), so.certErr(cause), so.pendingErr("read", cause)} {
		if !errAs[*TenantSOEnrollmentError](err) || !errors.Is(err, cause) {
			t.Fatalf("SO failure %v is not a TenantSOEnrollmentError over its cause", err)
		}
	}
}

func TestSOErrors_DescribeAndUnwrap(t *testing.T) {
	cause := errors.New("cause")
	for _, err := range []error{
		&IncompleteTenantSOError{Field: "so_pin"}, &ConflictingSOCredentialError{}, &SOMatchesAdminError{Field: "key"}, &SOPartlyMatchesAdminError{Field: "key"},
		&SOCredentialRejectedError{Cause: cause}, &TenantSOEnrollmentError{Op: "PIN commit", Cause: cause},
		&PendingAdminAccountConflictError{TenantID: "acme", Username: "officer", Role: role.RoleNameSO},
	} {
		if err.Error() == "" {
			t.Fatalf("%T has an empty message", err)
		}
	}
	if !errors.Is(&SOCredentialRejectedError{Cause: cause}, cause) || !errors.Is(&TenantSOEnrollmentError{Cause: cause}, cause) {
		t.Fatal("an SO error does not unwrap to its cause")
	}
}
