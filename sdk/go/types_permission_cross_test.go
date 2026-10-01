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

package sdk_test

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// Compile-time: the denial crosses the SDK boundary as an alias of the RBAC
// authorizer's own type, so errors.As against the SDK name matches what the
// authorizer returns. A copy would compile here but fail this assignment.
var (
	_ *rbac.PermissionDeniedError = (*sdk.PermissionDeniedError)(nil)
	_ error                       = (*sdk.PermissionDeniedError)(nil)
	_ sdk.Permission              = sdk.PermissionAuditReadCross
)

// sdkCrossScopePermissions is every cross-scope permission the SDK
// re-exports. TestSDK_CrossScopePermissions_AreComplete compares it with the
// registry, so a cross-scope permission added to pkg/permission without an
// SDK name fails here rather than in a consumer that had to restate it.
var sdkCrossScopePermissions = []sdk.Permission{
	sdk.PermissionTenantsListCross,
	sdk.PermissionTenantsCreateCross,
	sdk.PermissionTenantsApprove,
	sdk.PermissionTenantsCreate,
	sdk.PermissionTenantsSetIsolation,
	sdk.PermissionTenantsSetEntitlements,
	sdk.PermissionTenantsSetQuotas,
	sdk.PermissionTenantsListDeleted,
	sdk.PermissionTenantsReconcileDeleted,
	sdk.PermissionAuditReadCross,
	sdk.PermissionCertsRevokeCross,
	sdk.PermissionVolumePoolCreate,
	sdk.PermissionVolumePoolList,
	sdk.PermissionVolumePoolRead,
	sdk.PermissionVolumePoolUpdate,
	sdk.PermissionVolumePoolDelete,
	sdk.PermissionRegistrationConfigRead,
	sdk.PermissionRegistrationConfigUpdate,
	sdk.PermissionMVCCCompact,
	sdk.PermissionCredentialsSubmit,
	sdk.PermissionCredentialsStrategy,
	sdk.PermissionCredentialsList,
	sdk.PermissionCredentialsDelete,
	sdk.PermissionCredentialsRotate,
	sdk.PermissionCredentialsGCExpired,
	sdk.PermissionCredentialsRetrieveVersion,
	sdk.PermissionCredentialsListVersions,
	sdk.PermissionCredentialsStore,
	sdk.PermissionCredentialsRetrieve,
	sdk.PermissionSealSeal,
	sdk.PermissionSealRekey,
	sdk.PermissionSealRotateRootKey,
	sdk.PermissionSealGenerateRecovery,
	sdk.PermissionSealInitializeShamir,
	sdk.PermissionSealBootstrap,
	sdk.PermissionSealUnseal,
	sdk.PermissionSealRecoverWithKeys,
	sdk.PermissionSealEnableEncryption,
	sdk.PermissionSealSubmitSealVote,
	sdk.PermissionSealOfficerRegister,
	sdk.PermissionSealOfficerList,
	sdk.PermissionCeremonyInitialize,
	sdk.PermissionCeremonyEnrollCustodian,
	sdk.PermissionCeremonyRevokeCustodian,
	sdk.PermissionCeremonySignCSR,
}

// TestSDK_CrossScopePermissions_AreTheCanonicalOnes pins the re-exports to
// pkg/permission's values and proves each is registered with cross scope.
func TestSDK_CrossScopePermissions_AreTheCanonicalOnes(t *testing.T) {
	pairs := map[sdk.Permission]permission.Permission{
		sdk.PermissionTenantsListCross:           permission.TenantsListCross,
		sdk.PermissionTenantsCreateCross:         permission.TenantsCreateCross,
		sdk.PermissionTenantsApprove:             permission.TenantsApprove,
		sdk.PermissionTenantsCreate:              permission.TenantsCreate,
		sdk.PermissionTenantsSetIsolation:        permission.TenantsSetIsolation,
		sdk.PermissionTenantsSetEntitlements:     permission.TenantsSetEntitlements,
		sdk.PermissionTenantsSetQuotas:           permission.TenantsSetQuotas,
		sdk.PermissionTenantsListDeleted:         permission.TenantsListDeleted,
		sdk.PermissionTenantsReconcileDeleted:    permission.TenantsReconcileDeleted,
		sdk.PermissionAuditReadCross:             permission.AuditReadCross,
		sdk.PermissionCertsRevokeCross:           permission.CertsRevokeCross,
		sdk.PermissionVolumePoolCreate:           permission.VolumePoolCreate,
		sdk.PermissionVolumePoolList:             permission.VolumePoolList,
		sdk.PermissionVolumePoolRead:             permission.VolumePoolRead,
		sdk.PermissionVolumePoolUpdate:           permission.VolumePoolUpdate,
		sdk.PermissionVolumePoolDelete:           permission.VolumePoolDelete,
		sdk.PermissionRegistrationConfigRead:     permission.RegistrationConfigRead,
		sdk.PermissionRegistrationConfigUpdate:   permission.RegistrationConfigUpdate,
		sdk.PermissionMVCCCompact:                permission.MVCCCompact,
		sdk.PermissionCredentialsSubmit:          permission.CredentialsSubmit,
		sdk.PermissionCredentialsStrategy:        permission.CredentialsStrategy,
		sdk.PermissionCredentialsList:            permission.CredentialsList,
		sdk.PermissionCredentialsDelete:          permission.CredentialsDelete,
		sdk.PermissionCredentialsRotate:          permission.CredentialsRotate,
		sdk.PermissionCredentialsGCExpired:       permission.CredentialsGCExpired,
		sdk.PermissionCredentialsRetrieveVersion: permission.CredentialsRetrieveVersion,
		sdk.PermissionCredentialsListVersions:    permission.CredentialsListVersions,
		sdk.PermissionCredentialsStore:           permission.CredentialsStore,
		sdk.PermissionCredentialsRetrieve:        permission.CredentialsRetrieve,
		sdk.PermissionSealSeal:                   permission.SealSeal,
		sdk.PermissionSealRekey:                  permission.SealRekey,
		sdk.PermissionSealRotateRootKey:          permission.SealRotateRootKey,
		sdk.PermissionSealGenerateRecovery:       permission.SealGenerateRecovery,
		sdk.PermissionSealInitializeShamir:       permission.SealInitializeShamir,
		sdk.PermissionSealBootstrap:              permission.SealBootstrap,
		sdk.PermissionSealUnseal:                 permission.SealUnseal,
		sdk.PermissionSealRecoverWithKeys:        permission.SealRecoverWithKeys,
		sdk.PermissionSealEnableEncryption:       permission.SealEnableEncryption,
		sdk.PermissionSealSubmitSealVote:         permission.SealSubmitSealVote,
		sdk.PermissionSealOfficerRegister:        permission.SealOfficerRegister,
		sdk.PermissionSealOfficerList:            permission.SealOfficerList,
		sdk.PermissionCeremonyInitialize:         permission.CeremonyInitialize,
		sdk.PermissionCeremonyEnrollCustodian:    permission.CeremonyEnrollCustodian,
		sdk.PermissionCeremonyRevokeCustodian:    permission.CeremonyRevokeCustodian,
		sdk.PermissionCeremonySignCSR:            permission.CeremonySignCSR,
	}
	if len(pairs) != len(sdkCrossScopePermissions) {
		t.Fatalf("%d distinct re-exports, want %d: two SDK names share a value", len(pairs), len(sdkCrossScopePermissions))
	}
	for sdkValue, pkgValue := range pairs {
		if sdkValue != pkgValue {
			t.Errorf("sdk %q != pkg %q", sdkValue, pkgValue)
		}
		if !sdk.IsPermissionRegistered(sdkValue) {
			t.Errorf("%q is not registered", sdkValue)
		}
		if !permission.IsCrossScope(sdkValue) {
			t.Errorf("%q is not cross-scope", sdkValue)
		}
	}
	if sdk.PermissionAuditReadCross != "audit:read:cross" {
		t.Errorf("PermissionAuditReadCross = %q, want audit:read:cross", sdk.PermissionAuditReadCross)
	}
}

// TestSDK_CrossScopePermissions_AreComplete is the discovery half: every
// permission the registry tags cross-scope has an SDK name. The floor guards
// the loop — an empty registry would otherwise pass it vacuously.
func TestSDK_CrossScopePermissions_AreComplete(t *testing.T) {
	var registered []string
	for _, p := range sdk.AllPermissions() {
		if permission.IsCrossScope(p) {
			registered = append(registered, p.String())
		}
	}
	if len(registered) < len(sdkCrossScopePermissions) {
		t.Fatalf("registry holds %d cross-scope permissions, fewer than the %d re-exported", len(registered), len(sdkCrossScopePermissions))
	}
	exported := make([]string, 0, len(sdkCrossScopePermissions))
	for _, p := range sdkCrossScopePermissions {
		exported = append(exported, p.String())
	}
	sort.Strings(registered)
	sort.Strings(exported)
	if len(registered) != len(exported) {
		t.Fatalf("cross-scope permissions without an SDK name:\n registry %v\n sdk      %v", registered, exported)
	}
	for i := range registered {
		if registered[i] != exported[i] {
			t.Fatalf("cross-scope permissions without an SDK name:\n registry %v\n sdk      %v", registered, exported)
		}
	}
}

// TestSDK_PermissionDeniedError_IsDistinguishable is the behaviour the
// re-export exists for: against a real RBAC authorizer, a tenant-local
// identity holding audit:read:cross is DENIED — the cross-scope rule, not a
// missing grant — and the consumer recognises the denial with errors.As on
// the SDK name. The system-tenant control proves the grant itself is real,
// so the denial is the scope rule and not an empty role.
func TestSDK_PermissionDeniedError_IsDistinguishable(t *testing.T) {
	ctx := context.Background()
	roles, err := role.NewStore(kvstore.NewMemoryKVStore())
	if err != nil {
		t.Fatalf("role.NewStore: %v", err)
	}
	auditors := map[string]string{}
	for _, tid := range []string{tenant.SystemTenantID, "acme"} {
		r, cerr := roles.CreateRole(tenant.WithTenantID(ctx, tid), &role.CreateRoleRequest{
			Name:        "cross-auditor",
			TenantID:    tid,
			Permissions: []permission.Permission{sdk.PermissionAuditReadCross},
		})
		if cerr != nil {
			t.Fatalf("CreateRole(%s): %v", tid, cerr)
		}
		auditors[tid] = r.ID
	}
	var authz sdk.Authorizer = rbac.New(roles)
	resource := permission.Resource(sdk.PermissionAuditReadCross)
	action := permission.Action(sdk.PermissionAuditReadCross)

	system := &sdk.Identity{Subject: "ops", TenantID: tenant.SystemTenantID, Roles: []string{auditors[tenant.SystemTenantID]}}
	if err := authz.Authorize(tenant.WithTenantID(ctx, tenant.SystemTenantID), system, resource, action); err != nil {
		t.Fatalf("system-tenant auditor refused %s: %v", sdk.PermissionAuditReadCross, err)
	}

	local := &sdk.Identity{Subject: "eve", TenantID: "acme", Roles: []string{auditors["acme"]}}
	err = authz.Authorize(tenant.WithTenantID(ctx, "acme"), local, resource, action)
	var denied *sdk.PermissionDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("tenant-local %s: got %T (%v), want *sdk.PermissionDeniedError", sdk.PermissionAuditReadCross, err, err)
	}
	if denied.Subject != "eve" || denied.Resource != resource || denied.Action != action {
		t.Errorf("denial = %+v, want eve on %s:%s", denied, resource, action)
	}

	// A failure to decide is NOT a denial: the consumer must be able to tell
	// the two apart, which is the whole reason the type crosses the boundary.
	// A role the store cannot resolve is the authorizer failing to decide.
	ghost := &sdk.Identity{Subject: "ops", TenantID: tenant.SystemTenantID, Roles: []string{"no-such-role"}}
	undecided := authz.Authorize(tenant.WithTenantID(ctx, tenant.SystemTenantID), ghost, resource, action)
	if undecided == nil {
		t.Fatal("an unresolvable role was admitted")
	}
	if errors.As(undecided, &denied) {
		t.Errorf("a resolution failure (%v) matched *sdk.PermissionDeniedError", undecided)
	}
}
