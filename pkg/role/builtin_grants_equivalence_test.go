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

package role

import (
	"sort"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// ---------------------------------------------------------------------------
// Golden-set drift guard
//
// This file used to compare the registry-derived permission.RolePermissions
// projection against five hand-maintained functions (soPermissions,
// adminPermissions, operatorPermissions, userPermissions,
// auditorPermissions) that lived in store.go. That equivalence proof passed
// exactly (verified before the hand-maintained functions were deleted) and
// the hand-maintained functions are now gone — role placement is registered
// once, as metadata on the permission, in
// pkg/permission/builtin_role_grants.go.
//
// The five golden slices below are the frozen literal snapshot of that
// proven-equivalent state. They exist ONLY to catch future silent drift in
// pkg/permission/builtin_role_grants.go (an accidental permission add/remove
// on a built-in role): a change here is a privilege-escalation or
// availability review item, not a routine update. Deliberately-added grants
// must update BOTH this file and builtin_role_grants.go in the same change.
// ---------------------------------------------------------------------------

var goldenSOPermissions = []permission.Permission{
	permission.SealBootstrap, permission.SealUnseal, permission.SealSeal,
	permission.SealStatus, permission.SealRotateRootKey, permission.SealRekey,
	permission.SealGenerateRecovery, permission.SealRecoverWithKeys,
	permission.SealInitializeShamir,
	permission.SealOfficerRegister, permission.SealOfficerList,
	permission.SealSubmitSealVote,
	permission.CeremonyInitialize, permission.CeremonyEnrollCustodian,
	permission.CeremonyRevokeCustodian, permission.CeremonyClaimCert,
	permission.CeremonySignCSR, permission.CeremonyStatus,
	permission.CertsGenerate, permission.CertsExport, permission.CertsRevoke,
	permission.CertsList, permission.CertsRead,
	permission.RolesCreate, permission.RolesRead, permission.RolesUpdate,
	permission.RolesDelete, permission.RolesList, permission.RolesAssign,
	permission.RolesUnassign, permission.PermissionsList,
	permission.UsersCreate, permission.UsersRead, permission.UsersUpdate,
	permission.UsersDelete, permission.UsersList, permission.UsersEnable,
	permission.UsersDisable,
	permission.TenantsCreate, permission.TenantsRead, permission.TenantsUpdate,
	permission.TenantsDelete, permission.TenantsList,
	permission.TenantsSetIsolation, permission.TenantsSetEntitlements,
	permission.KeysCreate, permission.KeysDelete, permission.KeysList, permission.KeysGet,
	permission.KeysImport, permission.KeysExport,
	permission.EscrowCreate, permission.EscrowRecover, permission.EscrowList, permission.EscrowRevoke,
	permission.BackendsList, permission.BackendsRead,
	permission.PINRead, permission.PINWrite,
	permission.PIVRead, permission.PIVWrite, permission.PIVDelete,
	permission.AuditRead, permission.AuditQuery,
	permission.AdminHealth, permission.AdminStatus, permission.AdminPeers,
	permission.AdminAddMember, permission.AdminRemoveMember,
	permission.AdminSnapshot, permission.AdminDrain, permission.AdminCapabilities,
	permission.AuthFactorEnroll, permission.AuthFactorList, permission.AuthFactorRevoke,
	permission.CACreate, permission.CARead, permission.CAList, permission.CAUpdate,
	permission.ACMEAccountsRead, permission.ACMEAccountsList, permission.ACMEAccountsDisable,
	permission.ACMEOrdersRead, permission.ACMEOrdersList,
	permission.ACMEEABRead, permission.ACMEEABList,
	permission.ACMEEABCreate, permission.ACMEEABDelete,
}

var goldenAdminPermissions = []permission.Permission{
	permission.KeysCreate, permission.KeysRotate, permission.KeysDelete,
	permission.KeysList, permission.KeysGet,
	permission.KeysWrap, permission.KeysUnwrap,
	permission.DEKRotate, permission.DEKRewrap, permission.DEKRetire, permission.DEKStatus,
	permission.KeysImport, permission.KeysExport,
	permission.EscrowCreate, permission.EscrowRecover, permission.EscrowList, permission.EscrowRevoke,
	permission.CertsGenerate, permission.CertsExport, permission.CertsRevoke,
	permission.CertsList, permission.CertsRead,
	permission.BackendsList, permission.BackendsRead,
	permission.PINRead, permission.PINWrite,
	permission.PIVRead, permission.PIVWrite, permission.PIVDelete,
	permission.PasswordsRead, permission.PasswordsWrite, permission.PasswordsDelete,
	permission.PasswordsGenerate, permission.PasswordsLock, permission.PasswordsUnlock,
	permission.SSHRead, permission.SSHWrite, permission.SSHDelete,
	permission.SealUnseal, permission.SealSeal, permission.SealStatus,
	permission.SealRecoverWithKeys,
	permission.CeremonyStatus,
	permission.TenantsRead, permission.TenantsUpdate,
	permission.TenantsDelete, permission.TenantsList,
	permission.TenantBarrierInit, permission.TenantBarrierUnseal,
	permission.TenantBarrierSeal,
	permission.TenantBarrierOfficerRegister, permission.TenantBarrierOfficerList,
	permission.UsersCreate, permission.UsersRead, permission.UsersUpdate,
	permission.UsersDelete, permission.UsersList, permission.UsersEnable,
	permission.UsersDisable,
	permission.RolesRead, permission.RolesList, permission.RolesAssign,
	permission.RolesUnassign, permission.PermissionsList,
	permission.AdminHealth, permission.AdminStatus, permission.AdminPeers,
	permission.AdminAddMember, permission.AdminRemoveMember,
	permission.AdminSnapshot, permission.AdminDrain, permission.AdminCapabilities,
	permission.MVCCRestore,
	permission.AuditRead, permission.AuditQuery,
	permission.TenantsListCross, permission.TenantsCreateCross, permission.TenantsApprove,
	// Tenant provisioning, ScopeCross: effective only for system-tenant Admin.
	// tenants:set_quotas is new to Admin: the quotas routes now check it
	// rather than tenants:update, and the platform operator must hold it.
	permission.TenantsCreate, permission.TenantsSetQuotas,
	permission.TenantsSetEntitlements, permission.TenantsSetIsolation,
	// Retired-tenant listing and reconcile, cross-scope: effective only for
	// system-tenant Admin.
	permission.TenantsListDeleted, permission.TenantsReconcileDeleted,
	permission.VolumePoolCreate, permission.VolumePoolList, permission.VolumePoolRead,
	permission.VolumePoolUpdate, permission.VolumePoolDelete,
	permission.RegistrationConfigRead, permission.RegistrationConfigUpdate,
	permission.CertsRevokeCross,
	permission.SecretsRead, permission.SecretsWrite, permission.SecretsDelete,
	permission.SecretsList,
	permission.TrustRead, permission.TrustWrite,
	permission.ACMERead, permission.ACMEWrite,
	permission.ObjStoreAll,
	permission.OrchestratorAll,
	permission.SchedulerAll,
	permission.MessagingAll,
	permission.ParametersAll,
	permission.DynamicSecretsAll,
	permission.ProjectAll,
	permission.OllamaAll,
	permission.CACreate, permission.CARead, permission.CAList, permission.CAUpdate,
	permission.ACMEAccountsRead, permission.ACMEAccountsList, permission.ACMEAccountsDisable,
	permission.ACMEOrdersRead, permission.ACMEOrdersList,
	permission.ACMEEABRead, permission.ACMEEABList,
	permission.ACMEEABCreate, permission.ACMEEABDelete,
}

var goldenOperatorPermissions = []permission.Permission{
	permission.KeysSign, permission.KeysVerify,
	permission.KeysEncrypt, permission.KeysDecrypt,
	permission.KeysList, permission.KeysGet,
	permission.DEKEncrypt, permission.DEKDecrypt, permission.DEKRewrap, permission.DEKStatus,
	permission.KeysRotate,
	permission.KeysDerive,
	permission.BackendsList, permission.BackendsRead,
	permission.PINRead, permission.PIVRead, permission.PIVUse,
	permission.PasswordsRead, permission.PasswordsWrite, permission.PasswordsDelete,
	permission.PasswordsGenerate, permission.PasswordsLock, permission.PasswordsUnlock,
	permission.SSHRead, permission.SSHWrite, permission.SSHDelete,
	permission.SealUnseal, permission.SealSeal, permission.SealStatus,
	permission.CeremonyStatus,
	permission.KVPut, permission.KVGet, permission.KVDelete,
	permission.KVList, permission.KVExists, permission.KVRange,
	permission.MVCCPutWithLease, permission.MVCCGetRevision,
	permission.MVCCGetAtRevision, permission.MVCCRange,
	permission.MVCCCompact, permission.MVCCCurrentRevision,
	permission.MVCCRestore,
	permission.TransactionExecute,
	permission.BatchExecute,
	permission.WatchWatch, permission.WatchWatchPrefix,
	permission.LeaseGrant, permission.LeaseRevoke,
	permission.LeaseKeepAlive, permission.LeaseTTL, permission.LeaseList,
	permission.IndexCreate, permission.IndexDrop,
	permission.IndexQuery, permission.IndexScan,
	permission.TSWrite, permission.TSWriteBatch,
	permission.TSQuery, permission.TSAggregate,
	permission.VectorNamespaceCreate, permission.VectorNamespaceDelete,
	permission.VectorNamespaceList, permission.VectorNamespaceInfo,
	permission.VectorUpsert, permission.VectorGet, permission.VectorDelete,
	permission.VectorBatchUpsert, permission.VectorBatchDelete, permission.VectorList,
	permission.VectorUpdateMetadata, permission.VectorSearchSimilar,
	permission.VectorSearchByMetadata, permission.VectorHybridSearch,
	permission.VectorSearchByID, permission.VectorRebuildIndex,
	permission.VectorGetRebuildProg, permission.VectorGetIndexStats,
	permission.VectorCompact, permission.VectorGetCompactProg,
	permission.AdminHealth, permission.AdminStatus,
	permission.AuditRead,
	permission.ObjStorePut, permission.ObjStoreGet, permission.ObjStoreDelete,
	permission.ObjStoreList, permission.ObjStoreHead, permission.ObjStoreCopy,
	permission.ObjStoreArchive, permission.ObjStoreRestore,
	permission.ObjStoreVerify, permission.ObjStoreBatch,
	permission.ObjStoreReadPolicy, permission.ObjStoreReadBackend,
	permission.ObjStoreReadStats, permission.ObjStoreTenantUsage,
	permission.ObjStoreTenantQuotaRead,
	permission.OrchestratorSpawn, permission.OrchestratorPrompt,
	permission.OrchestratorStatus, permission.OrchestratorClose,
	permission.OrchestratorList, permission.OrchestratorTaskCreate,
	permission.OrchestratorTaskRead, permission.OrchestratorTaskList,
	permission.OrchestratorTaskClaim, permission.OrchestratorTaskReadmit,
	permission.OrchestratorTaskBlock, permission.OrchestratorTaskComplete,
	permission.OrchestratorTaskDecompose, permission.OrchestratorArtifactSubmit,
	permission.OrchestratorArtifactRead, permission.OrchestratorJobStart,
	permission.OrchestratorJobRead, permission.OrchestratorJobList,
	permission.OrchestratorJobCancel, permission.OrchestratorBackendList,
	permission.OrchestratorDiscover, permission.OrchestratorHook,
	permission.OrchestratorWorkflowRead, permission.OrchestratorWorkflowWrite,
	permission.OrchestratorHealth, permission.OrchestratorInference,
	permission.OrchestratorAgentRegister, permission.OrchestratorAgentRead,
	permission.OrchestratorAgentList, permission.OrchestratorAgentRemove,
	permission.OrchestratorAgentHeartbeat, permission.OrchestratorCacheRead,
	permission.OrchestratorCacheWrite, permission.OrchestratorBroadcast,
	permission.MessagingChannelCreate, permission.MessagingChannelRead,
	permission.MessagingChannelList, permission.MessagingChannelDelete,
	permission.MessagingPublish, permission.MessagingSubscribe,
	permission.MessagingUnsubscribe, permission.MessagingWatch,
	permission.MessagingStats,
	permission.SchedulerCreate, permission.SchedulerRead, permission.SchedulerList,
	permission.SchedulerUpdate, permission.SchedulerDelete,
	permission.SchedulerTrigger, permission.SchedulerPause,
	permission.SchedulerResume, permission.SchedulerCancel,
	permission.SchedulerHistory, permission.SchedulerStatus,
	permission.ParametersRead, permission.ParametersWrite,
	permission.ParametersDelete, permission.ParametersList,
	permission.ParametersSearch,
	permission.DynamicSecretsGenerate, permission.DynamicSecretsLeaseRenew,
	permission.DynamicSecretsLeaseRevoke, permission.DynamicSecretsLeaseList,
	permission.ProjectCreate, permission.ProjectRead, permission.ProjectList,
	permission.ProjectUpdate, permission.ProjectDelete,
	permission.OllamaHealth, permission.OllamaModelList,
	permission.OllamaModelInfo, permission.OllamaEmbed,
	permission.CARead, permission.CAList,
	permission.ACMEAccountsRead, permission.ACMEAccountsList,
	permission.ACMEOrdersRead, permission.ACMEOrdersList,
	permission.ACMEEABRead, permission.ACMEEABList,
}

var goldenUserPermissions = []permission.Permission{
	permission.KeysSign, permission.KeysVerify,
	permission.KeysEncrypt, permission.KeysDecrypt,
	permission.DEKEncrypt, permission.DEKDecrypt,
	permission.KeysDerive,
	permission.KeysGet, permission.KeysList,
	permission.PIVUse,
	permission.PasswordsRead, permission.PasswordsLock, permission.PasswordsUnlock,
	permission.AdminHealth, permission.AdminStatus,
	permission.OrchestratorSpawn, permission.OrchestratorPrompt,
	permission.OrchestratorStatus, permission.OrchestratorClose,
	permission.OrchestratorList, permission.OrchestratorTaskCreate,
	permission.OrchestratorTaskRead, permission.OrchestratorTaskList,
	permission.OrchestratorTaskDecompose, permission.OrchestratorArtifactRead,
	permission.OrchestratorJobStart, permission.OrchestratorJobRead,
	permission.OrchestratorJobList, permission.OrchestratorJobCancel,
	permission.OrchestratorWorkflowRead, permission.OrchestratorHealth,
	permission.OrchestratorInference,
}

var goldenAuditorPermissions = []permission.Permission{
	permission.AuditRead, permission.AuditQuery, permission.AuditStream,
	permission.AdminHealth, permission.AdminStatus, permission.AdminPeers,
	permission.UsersList, permission.UsersRead,
	permission.RolesList, permission.RolesRead, permission.PermissionsList,
	permission.CertsList, permission.CertsRead,
	permission.BackendsList, permission.BackendsRead,
	permission.PINRead, permission.PIVRead, permission.PasswordsRead,
	permission.TenantsList, permission.TenantsRead,
	permission.KeysList, permission.KeysGet,
	permission.EscrowList,
	permission.CeremonyStatus,
	permission.AuthFactorList,
	permission.OrchestratorStatus, permission.OrchestratorList,
	permission.OrchestratorTaskRead, permission.OrchestratorTaskList,
	permission.OrchestratorJobRead, permission.OrchestratorJobList,
	permission.OrchestratorWorkflowRead, permission.OrchestratorAgentRead,
	permission.OrchestratorAgentList, permission.OrchestratorBackendList,
	permission.OrchestratorHealth,
	permission.CARead, permission.CAList,
	permission.ACMEAccountsRead, permission.ACMEAccountsList,
	permission.ACMEOrdersRead, permission.ACMEOrdersList,
	permission.ACMEEABRead, permission.ACMEEABList,
}

// permSet converts a permission slice to a set for order-independent
// comparison.
func permSet(perms []permission.Permission) map[permission.Permission]struct{} {
	set := make(map[permission.Permission]struct{}, len(perms))
	for _, p := range perms {
		set[p] = struct{}{}
	}
	return set
}

// assertSameSet fails the test with a precise diff when got and want are not
// the exact same set of permissions.
func assertSameSet(t *testing.T, roleName string, got, want []permission.Permission) {
	t.Helper()
	gotSet := permSet(got)
	wantSet := permSet(want)

	var missing, extra []permission.Permission
	for p := range wantSet {
		if _, ok := gotSet[p]; !ok {
			missing = append(missing, p)
		}
	}
	for p := range gotSet {
		if _, ok := wantSet[p]; !ok {
			extra = append(extra, p)
		}
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
	sort.Slice(extra, func(i, j int) bool { return extra[i] < extra[j] })

	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("%s: registry-derived permission set does not match the golden set\n  missing (in golden, not derived): %v\n  extra (in derived, not golden):   %v",
			roleName, missing, extra)
	}
}

// TestBuiltinRoleGrants_SOMatchesGoldenSet pins the Security Officer role's
// registry-derived permission set against the frozen golden snapshot. See
// the package doc above: this is the drift guard left behind after the
// hand-maintained soPermissions() function it once cross-checked was
// deleted.
func TestBuiltinRoleGrants_SOMatchesGoldenSet(t *testing.T) {
	assertSameSet(t, "so", permission.RolePermissions(permission.RoleSO), goldenSOPermissions)
}

// TestBuiltinRoleGrants_AdminMatchesGoldenSet is the Admin counterpart of
// TestBuiltinRoleGrants_SOMatchesGoldenSet.
func TestBuiltinRoleGrants_AdminMatchesGoldenSet(t *testing.T) {
	assertSameSet(t, "admin", permission.RolePermissions(permission.RoleAdmin), goldenAdminPermissions)
}

// TestBuiltinRoleGrants_OperatorMatchesGoldenSet is the Operator counterpart
// of TestBuiltinRoleGrants_SOMatchesGoldenSet.
func TestBuiltinRoleGrants_OperatorMatchesGoldenSet(t *testing.T) {
	assertSameSet(t, "operator", permission.RolePermissions(permission.RoleOperator), goldenOperatorPermissions)
}

// TestBuiltinRoleGrants_UserMatchesGoldenSet is the User counterpart of
// TestBuiltinRoleGrants_SOMatchesGoldenSet.
func TestBuiltinRoleGrants_UserMatchesGoldenSet(t *testing.T) {
	assertSameSet(t, "user", permission.RolePermissions(permission.RoleUser), goldenUserPermissions)
}

// TestBuiltinRoleGrants_AuditorMatchesGoldenSet is the Auditor counterpart of
// TestBuiltinRoleGrants_SOMatchesGoldenSet.
func TestBuiltinRoleGrants_AuditorMatchesGoldenSet(t *testing.T) {
	assertSameSet(t, "auditor", permission.RolePermissions(permission.RoleAuditor), goldenAuditorPermissions)
}

// TestBuiltInRoles_LateRegistrationLandsInDeclaredRole is the ordering-hazard
// proof required alongside the registry inversion: BuiltInRoles must
// materialize permission.RolePermissions lazily, at role-seed time, not
// cache a value computed at package-init time. This test runs long after
// every package init() in the program has completed (Go test binaries run
// all init()s before any test function), so registering a brand-new
// permission and role grant here — exactly what a higher layer such as
// go-quicbaas or go-xkms does at server-bootstrap time, before role seeding
// — simulates a late contribution. It must appear in BuiltInRoles' Admin
// role the very next time BuiltInRoles is called.
//
// permission.Register / permission.RegisterRoleGrant are additive-only by
// design (mirroring permission.RegisterScope — there is no unregister), so
// the "test:" probe permission this test adds to the process-global
// registry is never removed. It cannot corrupt the golden-set exact-match
// assertions above because it is namespaced under "test:" (it can never
// collide with a real permission constant) and this test is declared after
// them in this file, so it runs after they do.
func TestBuiltInRoles_LateRegistrationLandsInDeclaredRole(t *testing.T) {
	const late permission.Permission = "test:late-registration-builtinroles"

	before := roleByName(BuiltInRoles("late-reg-tenant"), RoleNameAdmin)
	for _, p := range before.Permissions {
		if p == late {
			t.Fatalf("precondition failed: %q already present on Admin", late)
		}
	}

	permission.Register(late)
	permission.RegisterRoleGrant(permission.RoleAdmin, late)

	after := roleByName(BuiltInRoles("late-reg-tenant"), RoleNameAdmin)
	found := false
	for _, p := range after.Permissions {
		if p == late {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Admin role after late registration = %v, missing %q", after.Permissions, late)
	}
}
