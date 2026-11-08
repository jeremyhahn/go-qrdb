// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: AGPL-3.0-or-later

package bootstrap

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantadmin"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// The tenant SO outside strict separation of duties: the admin may be the SO,
// named by the admin's username and key together.

func TestProvisionTenant_SO_StandardPostureTheAdminIsTheSO(t *testing.T) {
	csr := validAdminCSR(t)
	users := &fakeCertBindingUsers{}
	pins := &fakeSOPINs{}
	result, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID: "acme", Isolation: tenant.IsolationPrefix, AdminCSRPEM: csr,
		SO: &firstboot.SOConfig{Username: "Admin", CSRPEM: csr, PIN: sealedPIN(t, "so-pin-1234")},
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: users, Roles: &fakeRoles{}, Signer: &fakeSigner{cert: realCertPEM(t)},
		TrustDomain: onboardingTrustDomain, SOPINs: pins, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	if len(users.createCalls) != 1 {
		t.Fatalf("createCalls = %d, want the admin's account alone", len(users.createCalls))
	}
	want := []string{role.SystemRoleID("acme", role.RoleNameAdmin), role.SystemRoleID("acme", role.RoleNameSO)}
	if got := users.createCalls[0]; got.Username != "admin" || !slices.Equal(got.RoleIDs, want) {
		t.Fatalf("admin account = %q %v, want admin %v", got.Username, got.RoleIDs, want)
	}
	if len(users.bindCalls) != 1 {
		t.Fatalf("bindCalls = %v, want the admin's one key bound once", users.bindCalls)
	}
	if result.SOUsername != "admin" || result.SOUserID != result.AdminUserID || result.SOCertThumbprint != result.AdminCertThumbprint || len(result.SOCertPEM) == 0 {
		t.Fatalf("result SO = %q %d %q, want the admin's account and credential", result.SOUsername, result.SOUserID, result.SOCertThumbprint)
	}
	if len(pins.tenants) != 1 || string(pins.pins[0]) != "so-pin-1234" {
		t.Fatalf("SetSOPIN calls = %v, want the SO PIN committed", pins.tenants)
	}
}

func TestProvisionTenant_SO_StandardPostureAdminAsPendingSOIsCompletedOnTheAdminsAccount(t *testing.T) {
	ctx := context.Background()
	store := kvstore.NewMemoryKVStore()
	admins := tenantadmin.NewKVStore(store)
	sos := tenantadmin.NewSOKVStore(store)
	pins := &fakeSOPINs{}
	csr := validAdminCSR(t)
	result, err := ProvisionTenant(ctx, TenantProvisionConfig{
		TenantID: "acme", AdminCSRPEM: csr,
		SO: &firstboot.SOConfig{Username: "admin", CSRPEM: csr, PIN: sealedPIN(t, "so-pin-1234")},
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeCertBindingUsers{}, Roles: &fakeRoles{},
		PendingAdminSigner: &fakeSigner{cert: realCertPEM(t)}, TrustDomain: onboardingTrustDomain,
		PendingAdmins: admins, PendingSOs: sos, SOPINs: pins, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	admin, _ := admins.Get(ctx, "acme")
	so, _ := sos.Get(ctx, "acme")
	if admin == nil || so == nil || !reflect.DeepEqual(so, admin) {
		t.Fatalf("pending SO = %+v, want the pending admin %+v", so, admin)
	}
	if !result.SOAwaitsBarrierInit || result.SOCertThumbprint != result.AdminCertThumbprint || len(pins.tenants) != 1 {
		t.Fatalf("result SO = awaits %v %q, PINs %d; want the admin's credential pending and the PIN committed", result.SOAwaitsBarrierInit, result.SOCertThumbprint, len(pins.tenants))
	}

	users, roles := realIdentityStores(t)
	completion := Dependencies{PendingAdmins: admins, PendingSOs: sos, Users: users, Roles: roles, Logger: discardLogger()}
	if ok, err := CompletePendingTenantAdmin(ctx, "acme", completion); err != nil || !ok {
		t.Fatalf("CompletePendingTenantAdmin = %v, %v; want true, nil", ok, err)
	}
	account, err := users.GetUserByUsername(ctx, "acme", "admin")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	for _, name := range []string{role.RoleNameAdmin, role.RoleNameSO} {
		if !slices.Contains(account.RoleIDs, role.SystemRoleID("acme", name)) {
			t.Fatalf("admin RoleIDs = %v, want the %s role", account.RoleIDs, name)
		}
	}
	if len(account.CertBindings) != 1 || account.CertBindings[0].Thumbprint != admin.Thumbprint {
		t.Fatalf("admin bindings = %+v, want the one pending credential", account.CertBindings)
	}
	if a, _ := admins.Get(ctx, "acme"); a != nil {
		t.Fatalf("pending admin after completion = %+v, want it consumed", a)
	}
	if s, _ := sos.Get(ctx, "acme"); s != nil {
		t.Fatalf("pending SO after completion = %+v, want it consumed", s)
	}
}

// An SO distinct from the admin is enrolled the same way in both postures:
// at once with the so role alone, or as the __sys:pending-so record a barrier
// tenant completes, with the SO PIN committed either way.
func TestProvisionTenant_SO_DistinctSOInBothPostures(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(map[bool]string{false: "standard", true: "strict"}[strict], func(t *testing.T) {
			users := &fakeCertBindingUsers{}
			pins := &fakeSOPINs{}
			result, err := provisionWithSO(t, soConfig(t), users, Dependencies{SOPINs: pins, SeparationOfDuties: strict})
			if err != nil {
				t.Fatalf("ProvisionTenant: %v", err)
			}
			want := []string{role.SystemRoleID("acme", role.RoleNameSO)}
			if len(users.createCalls) != 2 || !slices.Equal(users.createCalls[1].RoleIDs, want) || result.SOUserID == result.AdminUserID {
				t.Fatalf("createCalls = %+v, want the admin and an SO holding %v alone", users.createCalls, want)
			}
			if len(pins.tenants) != 1 {
				t.Fatalf("SetSOPIN calls = %v, want the SO PIN committed", pins.tenants)
			}

			ctx := context.Background()
			store := kvstore.NewMemoryKVStore()
			sos := tenantadmin.NewSOKVStore(store)
			pins = &fakeSOPINs{}
			result, err = ProvisionTenant(ctx, TenantProvisionConfig{
				TenantID: "acme", AdminCSRPEM: validAdminCSR(t), SO: soConfig(t),
			}, Dependencies{
				Tenants: &fakeTenants{}, Users: &fakeCertBindingUsers{}, Roles: &fakeRoles{},
				PendingAdminSigner: &fakeSigner{cert: realCertPEM(t)}, TrustDomain: onboardingTrustDomain,
				PendingAdmins: tenantadmin.NewKVStore(store), PendingSOs: sos, SOPINs: pins,
				SeparationOfDuties: strict, Logger: discardLogger(),
			})
			if err != nil {
				t.Fatalf("ProvisionTenant(pending): %v", err)
			}
			if so, _ := sos.Get(ctx, "acme"); so == nil || so.Username != "officer" || !result.SOAwaitsBarrierInit || len(pins.tenants) != 1 {
				t.Fatalf("pending SO = %+v, awaits %v, PINs %d; want officer recorded and the PIN committed", so, result.SOAwaitsBarrierInit, len(pins.tenants))
			}
		})
	}
}

func TestCompletePendingTenantAdmin_SOAdoptsOnlyTheAdminBoundToItsCredential(t *testing.T) {
	ctx := context.Background()
	store := kvstore.NewMemoryKVStore()
	sos := tenantadmin.NewSOKVStore(store)
	users, roles := realIdentityStores(t)
	if _, err := users.CreateUser(ctx, &user.CreateUserRequest{
		WithoutEmailLoginKey: true, Username: "admin", TenantID: "acme",
		RoleIDs: []string{role.SystemRoleID("acme", role.RoleNameAdmin)},
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := sos.Create(ctx, "acme", &tenantadmin.Pending{Username: "admin", Thumbprint: adminThumbprint(), Serial: "1"}); err != nil {
		t.Fatalf("record pending SO: %v", err)
	}
	_, err := CompletePendingTenantAdmin(ctx, "acme", Dependencies{PendingSOs: sos, Users: users, Roles: roles, Logger: discardLogger()})
	if !errAs[*PendingAdminAccountConflictError](err) {
		t.Fatalf("CompletePendingTenantAdmin = %v, want *PendingAdminAccountConflictError for an admin not bound to the SO's credential", err)
	}
}

// realIdentityStores is the user and role store a real node completes a
// pending tenant account against.
func realIdentityStores(t *testing.T) (user.UserServicer, role.RoleServicer) {
	t.Helper()
	idgen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatal(err)
	}
	users, err := user.NewStore(kvstore.NewMemoryKVStore(), idgen)
	if err != nil {
		t.Fatal(err)
	}
	roles, err := role.NewStore(kvstore.NewMemoryKVStore())
	if err != nil {
		t.Fatal(err)
	}
	return users, roles
}

func TestGrantSORole(t *testing.T) {
	ctx := context.Background()
	soID := role.SystemRoleID("acme", role.RoleNameSO)
	adminID := role.SystemRoleID("acme", role.RoleNameAdmin)

	held := &user.User{RoleIDs: []string{adminID, soID}}
	if err := grantSORole(ctx, Dependencies{Users: &fakeUsers{}}, "acme", held); err != nil {
		t.Fatalf("grantSORole(already held) = %v, want nil without an update", err)
	}
	if err := grantSORole(ctx, Dependencies{Users: &fakeUsers{}}, "acme", &user.User{RoleIDs: []string{adminID}}); !errAs[*MissingProvisioningSeamError](err) {
		t.Fatalf("grantSORole(no UpdateUser) = %v, want *MissingProvisioningSeamError", err)
	}

	users, _ := realIdentityStores(t)
	account, err := users.CreateUser(ctx, &user.CreateUserRequest{
		WithoutEmailLoginKey: true, Username: "admin", TenantID: "acme", RoleIDs: []string{adminID},
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := grantSORole(ctx, Dependencies{Users: users}, "acme", account); err != nil {
		t.Fatalf("grantSORole = %v, want nil", err)
	}
	got, err := users.GetUserByUsername(ctx, "acme", "admin")
	if err != nil || !slices.Equal(got.RoleIDs, []string{adminID, soID}) {
		t.Fatalf("RoleIDs after grant = %v (%v), want %v", got.RoleIDs, err, []string{adminID, soID})
	}
}

// updateFailingUsers is a user store whose UpdateUser fails.
type updateFailingUsers struct {
	fakeUsers
	err error
}

func (u *updateFailingUsers) UpdateUser(context.Context, *user.UpdateUserRequest) (*user.User, error) {
	return nil, u.err
}

var _ userUpdater = (*updateFailingUsers)(nil)

func TestGrantSORole_UpdateFailureIsTyped(t *testing.T) {
	updateErr := errors.New("update failed")
	account := &user.User{ID: 7, RoleIDs: []string{role.SystemRoleID("acme", role.RoleNameAdmin)}}
	err := grantSORole(context.Background(), Dependencies{Users: &updateFailingUsers{err: updateErr}}, "acme", account)
	if !errAs[*TenantSOEnrollmentError](err) || !errors.Is(err, updateErr) {
		t.Fatalf("grantSORole = %v, want *TenantSOEnrollmentError wrapping the update failure", err)
	}
}

func TestRecordAdminAsPendingSO(t *testing.T) {
	ctx := context.Background()
	storeErr := errors.New("store failed")
	pending := &tenantadmin.Pending{Username: "admin", Thumbprint: adminThumbprint(), Serial: "1"}
	withAdmin := func(t *testing.T) (tenantadmin.Store, tenantadmin.Store) {
		t.Helper()
		store := kvstore.NewMemoryKVStore()
		admins := tenantadmin.NewKVStore(store)
		if err := admins.Create(ctx, "acme", pending); err != nil {
			t.Fatalf("record pending admin: %v", err)
		}
		return admins, tenantadmin.NewSOKVStore(store)
	}

	t.Run("records the pending admin as the SO and undoes it", func(t *testing.T) {
		admins, sos := withAdmin(t)
		undo := &undoStack{}
		result := &TenantProvisionResult{AdminCertPEM: []byte("cert")}
		if err := recordAdminAsPendingSO(ctx, "acme", Dependencies{PendingAdmins: admins, PendingSOs: sos}, undo, result); err != nil {
			t.Fatalf("recordAdminAsPendingSO: %v", err)
		}
		if so, _ := sos.Get(ctx, "acme"); so == nil || so.Username != pending.Username || so.Thumbprint != pending.Thumbprint || so.Serial != pending.Serial {
			t.Fatalf("pending SO = %+v, want %+v", so, pending)
		}
		if result.SOUsername != "admin" || string(result.SOCertPEM) != "cert" || result.SOCertThumbprint != pending.Thumbprint || result.SOCertSerial != "1" {
			t.Fatalf("result SO = %+v, want the pending admin's credential", result)
		}
		if errs := undo.run(ctx, discardLogger(), "acme"); len(errs) != 0 {
			t.Fatalf("undo: %v", errs)
		}
		if so, _ := sos.Get(ctx, "acme"); so != nil {
			t.Fatalf("pending SO after undo = %+v, want it deleted", so)
		}
	})
	t.Run("pending admin read failure", func(t *testing.T) {
		admins, sos := withAdmin(t)
		deps := Dependencies{PendingAdmins: &pendingAdminsFaulting{inner: admins, getErr: storeErr}, PendingSOs: sos}
		if err := recordAdminAsPendingSO(ctx, "acme", deps, &undoStack{}, &TenantProvisionResult{}); !errors.Is(err, storeErr) {
			t.Fatalf("recordAdminAsPendingSO = %v, want the read failure", err)
		}
	})
	t.Run("no pending admin", func(t *testing.T) {
		store := kvstore.NewMemoryKVStore()
		deps := Dependencies{PendingAdmins: tenantadmin.NewKVStore(store), PendingSOs: tenantadmin.NewSOKVStore(store)}
		if err := recordAdminAsPendingSO(ctx, "acme", deps, &undoStack{}, &TenantProvisionResult{}); !errAs[*MissingProvisioningSeamError](err) {
			t.Fatalf("recordAdminAsPendingSO = %v, want *MissingProvisioningSeamError", err)
		}
	})
	t.Run("pending SO record failure", func(t *testing.T) {
		admins, sos := withAdmin(t)
		deps := Dependencies{PendingAdmins: admins, PendingSOs: &pendingAdminsFaulting{inner: sos, putErr: storeErr}}
		if err := recordAdminAsPendingSO(ctx, "acme", deps, &undoStack{}, &TenantProvisionResult{}); !errAs[*TenantSOEnrollmentError](err) || !errors.Is(err, storeErr) {
			t.Fatalf("recordAdminAsPendingSO = %v, want *TenantSOEnrollmentError wrapping the record failure", err)
		}
	})
}

func TestAdminNamedAsSO(t *testing.T) {
	adminID := role.SystemRoleID("acme", role.RoleNameAdmin)
	bound := []user.CertBinding{{Thumbprint: "tp"}}
	pending := &tenantadmin.Pending{Thumbprint: "tp"}
	tests := []struct {
		name    string
		account *user.User
		role    string
		want    bool
	}{
		{"admin bound to the credential", &user.User{RoleIDs: []string{adminID}, CertBindings: bound}, role.RoleNameSO, true},
		{"completing the admin, not the SO", &user.User{RoleIDs: []string{adminID}, CertBindings: bound}, role.RoleNameAdmin, false},
		{"not the admin", &user.User{CertBindings: bound}, role.RoleNameSO, false},
		{"admin bound to another credential", &user.User{RoleIDs: []string{adminID}, CertBindings: []user.CertBinding{{Thumbprint: "other"}}}, role.RoleNameSO, false},
		{"admin with no binding", &user.User{RoleIDs: []string{adminID}}, role.RoleNameSO, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := adminNamedAsSO("acme", tt.account, pending, tt.role); got != tt.want {
				t.Fatalf("adminNamedAsSO = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBoundTo(t *testing.T) {
	account := &user.User{CertBindings: []user.CertBinding{{Thumbprint: "a"}, {Thumbprint: "b"}}}
	if !boundTo(account, "b") {
		t.Fatal("boundTo(b) = false, want true")
	}
	if boundTo(account, "c") || boundTo(&user.User{}, "a") {
		t.Fatal("boundTo an unbound thumbprint = true, want false")
	}
}
