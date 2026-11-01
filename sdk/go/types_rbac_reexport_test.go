// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package sdk_test

import (
	"crypto/fips140"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// Compile-time: the RBAC authorizer surface crosses the SDK boundary as
// aliases of the canonical declarations, not as copies.
var (
	_ func(role.RoleServicer, ...rbac.Option) *rbac.RBACAuthorizer = sdk.NewRBACAuthorizer
	_ sdk.Authorizer                                               = (*sdk.RBACAuthorizer)(nil)
	_ rbac.Option                                                  = sdk.RBACOption(nil)
	_ func(time.Duration) rbac.Option                              = sdk.WithRBACCacheTTL
	_ func(bool) rbac.Option                                       = sdk.WithRBACStrictSeparation
	_ func(bool) bool                                              = sdk.ResolveStrictSeparation
	_ func(role.RoleServicer) rbac.Option                          = sdk.WithRBACRoleServiceFallback
	_ func(permission.ScopeResolver) rbac.Option                   = sdk.WithRBACScopeResolver
	_ *rbac.ResolveError                                           = (*sdk.RBACResolveError)(nil)
	_ error                                                        = (*sdk.RBACResolveError)(nil)
	_ role.RoleServicer                                            = sdk.RoleServicer(nil)
)

// TestSDK_RBACReexport_ConstructsTheServerAuthorizer builds the authorizer
// through the SDK alone, with every option, and checks a nil identity gets
// the server's unauthenticated answer.
func TestSDK_RBACReexport_ConstructsTheServerAuthorizer(t *testing.T) {
	authz := sdk.NewRBACAuthorizer(nil,
		sdk.WithRBACCacheTTL(time.Second),
		sdk.WithRBACStrictSeparation(true),
		sdk.WithRBACRoleServiceFallback(nil),
		sdk.WithRBACScopeResolver(nil),
	)
	if authz == nil {
		t.Fatal("NewRBACAuthorizer returned nil")
	}
	err := authz.Authorize(t.Context(), nil, "audit", "read")
	if !errors.Is(err, sdk.ErrUnauthenticated) {
		t.Fatalf("Authorize(nil identity) = %v, want ErrUnauthenticated", err)
	}
}

// TestSDK_PermissionReexport_IdentityRBACAuditRead pins each re-exported
// permission to its canonical value and to its wire name.
func TestSDK_PermissionReexport_IdentityRBACAuditRead(t *testing.T) {
	cases := []struct {
		sdkValue, pkgValue sdk.Permission
		want               string
	}{
		{sdk.PermissionUsersCreate, permission.UsersCreate, "users:create"},
		{sdk.PermissionUsersRead, permission.UsersRead, "users:read"},
		{sdk.PermissionUsersUpdate, permission.UsersUpdate, "users:update"},
		{sdk.PermissionUsersDelete, permission.UsersDelete, "users:delete"},
		{sdk.PermissionUsersList, permission.UsersList, "users:list"},
		{sdk.PermissionUsersEnable, permission.UsersEnable, "users:enable"},
		{sdk.PermissionUsersDisable, permission.UsersDisable, "users:disable"},
		{sdk.PermissionUsersGetByCertThumbprint, permission.UsersGetByCertThumbprint, "users:get_by_cert_thumbprint"},
		{sdk.PermissionUsersAddCertBinding, permission.UsersAddCertBinding, "users:add_cert_binding"},
		{sdk.PermissionUsersRemoveCertBinding, permission.UsersRemoveCertBinding, "users:remove_cert_binding"},
		{sdk.PermissionUsersInvite, permission.UsersInvite, "users:invite"},
		{sdk.PermissionRolesCreate, permission.RolesCreate, "roles:create"},
		{sdk.PermissionRolesRead, permission.RolesRead, "roles:read"},
		{sdk.PermissionRolesUpdate, permission.RolesUpdate, "roles:update"},
		{sdk.PermissionRolesDelete, permission.RolesDelete, "roles:delete"},
		{sdk.PermissionRolesList, permission.RolesList, "roles:list"},
		{sdk.PermissionRolesAssign, permission.RolesAssign, "roles:assign"},
		{sdk.PermissionRolesUnassign, permission.RolesUnassign, "roles:unassign"},
		{sdk.PermissionAuditRead, permission.AuditRead, "audit:read"},
		{sdk.PermissionAuditQuery, permission.AuditQuery, "audit:query"},
		{sdk.PermissionAuditStream, permission.AuditStream, "audit:stream"},
		{sdk.PermissionAuditDelete, permission.AuditDelete, "audit:delete"},
	}
	for _, tc := range cases {
		if tc.sdkValue != tc.pkgValue || string(tc.sdkValue) != tc.want {
			t.Errorf("sdk=%q pkg=%q, want %q", tc.sdkValue, tc.pkgValue, tc.want)
		}
		if !sdk.IsPermissionRegistered(tc.sdkValue) {
			t.Errorf("%q is not registered", tc.sdkValue)
		}
	}
}

// TestSDK_ResolveStrictSeparation_SettingOnIsStrict proves the setting on
// resolves to strict separation in every mode.
func TestSDK_ResolveStrictSeparation_SettingOnIsStrict(t *testing.T) {
	if !sdk.ResolveStrictSeparation(true) {
		t.Fatal("ResolveStrictSeparation(true) = false, want true")
	}
}

// TestSDK_ResolveStrictSeparation_SettingOffFollowsFIPS proves the setting
// off resolves to what FIPS 140-3 mode forces: strict under it, standard
// outside it.
func TestSDK_ResolveStrictSeparation_SettingOffFollowsFIPS(t *testing.T) {
	if got, want := sdk.ResolveStrictSeparation(false), fips140.Enabled(); got != want {
		t.Fatalf("ResolveStrictSeparation(false) = %v, want fips140.Enabled() = %v", got, want)
	}
}
