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

package quic

// method_mapping_guard_test.go is the structural guard for the native-QUIC
// authorization surface. Since the unified-authz cutover, every frame method
// is registered through ONE registration helper (s.handle / s.handleIf /
// s.handleStreamOnly) that simultaneously declares the method's permission in
// the server's QUICMethodRegistry — and buildHandlers' DEFAULT-DENY startup
// assertion panics if any dispatch-map handler escaped the registry. This
// file guards that machinery:
//
//  1. The registry built by a real server matches, exactly, the curated
//     method -> permission table the old quicPermissions map declared (minus
//     its five dead aliases that never had a handler, plus the two officer
//     ceremony share-claim methods that gained the same identity-gated
//     contract they already carry on gRPC and REST), so the migration to
//     registration-site permissions cannot silently change a method's
//     required permission.
//  2. Every dispatch-map handler is covered by the registry (the startup
//     assertion's invariant, re-checked from the outside).
//  3. Every registered permission exists in the global permission catalog
//     (except the deliberately unregistered permission.IdentityGated
//     sentinel) and every method key uses dot notation.
//  4. AuthorizeQUIC is DEFAULT-DENY against the real registry: an unknown
//     method — including a deleted dead alias — is denied without consulting
//     the fine-grained authorizer, while a registered method authorizes.
//  5. Every QUIC permission has a gRPC counterpart on the qrdb.v1 proto
//     descriptors, except the documented QUIC-only families.

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	_ "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1" // registers qrdb.v1 services
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// expectedMethodPerms is the authoritative method -> permission table for the
// native QUIC transport. The share-holder quorum methods (barrier.QuorumBegin,
// barrier.QuorumSubmit and their tenant forms) are permission.IdentityGated:
// the holder's factor proof is the credential, checked by the service.
var expectedMethodPerms = map[string]permission.Permission{
	"kv.Put":    permission.KVPut,
	"kv.Get":    permission.KVGet,
	"kv.Delete": permission.KVDelete,
	"kv.List":   permission.KVList,
	"kv.Scan":   permission.KVRange,
	// The guarded write verbs (go-qrdb#124). They carry the SAME permissions
	// their gRPC RPCs declare in qrdb.proto — a guarded put is still a put, a
	// guarded delete is still a delete — so an identity that can reach the
	// verb over gRPC reaches it over QUIC too.
	"kv.PutIfAbsent":                     permission.KVPut,
	"kv.CompareAndDelete":                permission.KVDelete,
	"kv.CompareAndSwap":                  permission.KVPut,
	"mvcc.PutWithLease":                  permission.MVCCPutWithLease,
	"mvcc.GetRevision":                   permission.MVCCGetRevision,
	"mvcc.GetAtRevision":                 permission.MVCCGetAtRevision,
	"mvcc.Range":                         permission.MVCCRange,
	"mvcc.Compact":                       permission.MVCCCompact,
	"mvcc.CurrentRevision":               permission.MVCCCurrentRevision,
	"mvcc.RestoreToRevision":             permission.MVCCRestore,
	"tx.Transaction":                     permission.TransactionExecute,
	"batch.Batch":                        permission.BatchExecute,
	"batch.ImportBegin":                  permission.BatchExecute,
	"batch.ImportAppend":                 permission.BatchExecute,
	"batch.ImportCommit":                 permission.BatchExecute,
	"batch.ImportAbort":                  permission.BatchExecute,
	"watch.Watch":                        permission.WatchWatch,
	"watch.WatchPrefix":                  permission.WatchWatch,
	"lease.Grant":                        permission.LeaseGrant,
	"lease.Revoke":                       permission.LeaseRevoke,
	"lease.KeepAlive":                    permission.LeaseKeepAlive,
	"lease.TimeToLive":                   permission.LeaseTTL,
	"lease.List":                         permission.LeaseList,
	"index.Create":                       permission.IndexCreate,
	"index.Drop":                         permission.IndexDrop,
	"index.Query":                        permission.IndexQuery,
	"index.Scan":                         permission.IndexScan,
	"index.List":                         permission.IndexList,
	"ts.Write":                           permission.TSWrite,
	"ts.WriteBatch":                      permission.TSWriteBatch,
	"ts.Query":                           permission.TSQuery,
	"ts.Aggregate":                       permission.TSAggregate,
	"ts.WritePoint":                      permission.TSWritePoint,
	"ts.RichQuery":                       permission.TSRichQuery,
	"ts.DeleteSeries":                    permission.TSDeleteSeries,
	"ts.GetRetention":                    permission.TSGetRetention,
	"ts.SetRetention":                    permission.TSSetRetention,
	"ts.ListMetrics":                     permission.TSListMetrics,
	"vector.CreateNamespace":             permission.VectorNamespaceCreate,
	"vector.UpdateNamespace":             permission.VectorNamespaceUpdate,
	"vector.DeleteNamespace":             permission.VectorNamespaceDelete,
	"vector.ListNamespaces":              permission.VectorNamespaceList,
	"vector.GetNamespaceInfo":            permission.VectorNamespaceInfo,
	"vector.Upsert":                      permission.VectorUpsert,
	"vector.Get":                         permission.VectorGet,
	"vector.Delete":                      permission.VectorDelete,
	"vector.BatchUpsert":                 permission.VectorBatchUpsert,
	"vector.BatchDelete":                 permission.VectorBatchDelete,
	"vector.List":                        permission.VectorList,
	"vector.UpdateMetadata":              permission.VectorUpdateMetadata,
	"vector.SearchSimilar":               permission.VectorSearchSimilar,
	"vector.SearchByMetadata":            permission.VectorSearchByMetadata,
	"vector.HybridSearch":                permission.VectorHybridSearch,
	"vector.SearchByID":                  permission.VectorSearchByID,
	"vector.RebuildIndex":                permission.VectorRebuildIndex,
	"vector.GetRebuildProgress":          permission.VectorGetRebuildProg,
	"vector.GetIndexStats":               permission.VectorGetIndexStats,
	"vector.CompactNamespace":            permission.VectorCompact,
	"vector.GetCompactionProgress":       permission.VectorGetCompactProg,
	"vlog.Create":                        permission.VlogCreate,
	"vlog.List":                          permission.VlogList,
	"vlog.Append":                        permission.VlogAppend,
	"vlog.Get":                           permission.VlogGet,
	"vlog.ProveInclusion":                permission.VlogProof,
	"vlog.ProveConsistency":              permission.VlogProof,
	"vlog.Head":                          permission.VlogHead,
	"vlog.SignHead":                      permission.VlogSignHead,
	"vlog.RotateKey":                     permission.VlogSignHead,
	"vlog.ExpireBodies":                  permission.VlogExpire,
	"admin.Health":                       permission.AdminHealth,
	"admin.Status":                       permission.AdminStatus,
	"admin.Peers":                        permission.AdminPeers,
	"admin.AddMember":                    permission.AdminAddMember,
	"admin.RemoveMember":                 permission.AdminRemoveMember,
	"admin.PromoteMember":                permission.AdminAddMember,
	"admin.TransferLeadership":           permission.AdminRemoveMember,
	"admin.RequestSnapshot":              permission.AdminSnapshot,
	"admin.Drain":                        permission.AdminDrain,
	"admin.Capabilities":                 permission.AdminCapabilities,
	"user.Create":                        permission.UsersCreate,
	"user.Get":                           permission.UsersRead,
	"user.Update":                        permission.UsersUpdate,
	"user.Delete":                        permission.UsersDelete,
	"user.List":                          permission.UsersList,
	"user.Enable":                        permission.UsersEnable,
	"user.Disable":                       permission.UsersDisable,
	"user.Suspend":                       permission.UsersSuspend,
	"user.Unsuspend":                     permission.UsersUnsuspend,
	"user.GetByCertThumbprint":           permission.UsersGetByCertThumbprint,
	"user.AddCertBinding":                permission.UsersAddCertBinding,
	"user.RemoveCertBinding":             permission.UsersRemoveCertBinding,
	"role.Create":                        permission.RolesCreate,
	"role.Get":                           permission.RolesRead,
	"role.GetByName":                     permission.RolesRead,
	"role.Update":                        permission.RolesUpdate,
	"role.Delete":                        permission.RolesDelete,
	"role.List":                          permission.RolesList,
	"role.Assign":                        permission.RolesAssign,
	"role.Unassign":                      permission.RolesUnassign,
	"role.ListPermissions":               permission.PermissionsList,
	"role.ResolvePermissions":            permission.PermissionsResolve,
	"role.ListOwnPermissions":            permission.IdentityGated,
	"role.Authorize":                     permission.IdentityGated,
	"role.ReconcileBuiltIn":              permission.RolesCreate,
	"role.ReconcileBuiltInAllTenants":    permission.RolesReconcileAll,
	"cert.Issue":                         permission.CertsIssue,
	"cert.Enroll":                        permission.CertsEnroll,
	"cert.Delegate":                      permission.CertsDelegate,
	"cert.Revoke":                        permission.CertsRevoke,
	"cert.List":                          permission.CertsList,
	"user.Invite":                        permission.UsersInvite,
	"barrier.Initialize":                 permission.SealBootstrap,
	"barrier.Unseal":                     permission.SealUnseal,
	"barrier.ChangePassphrase":           permission.SealRekey,
	"barrier.Seal":                       permission.SealSeal,
	"barrier.Status":                     permission.SealStatus,
	"barrier.ClusterUnseal":              permission.SealUnseal,
	"barrier.ShamirInitialize":           permission.SealInitializeShamir,
	"barrier.EnableEncryption":           permission.IdentityGated,
	"barrier.OfficerRegister":            permission.SealOfficerRegister,
	"barrier.OfficerList":                permission.SealOfficerList,
	"barrier.ShareClaimBegin":            permission.IdentityGated,
	"barrier.ShareClaimComplete":         permission.IdentityGated,
	"barrier.RecoveryOfficerRegister":    permission.SealOfficerRegister,
	"barrier.RecoveryShareClaimBegin":    permission.IdentityGated,
	"barrier.RecoveryShareClaimComplete": permission.IdentityGated,
	"barrier.QuorumBegin":                permission.IdentityGated,
	"barrier.QuorumSubmit":               permission.IdentityGated,
	"barrier.RekeyStart":                 permission.SealRekey,
	"barrier.OfficerRevoke":              permission.SealOfficerRevoke,
	// InitBegin/InitComplete/Unseal/Status are IdentityGated at dispatch —
	// the RBAC check runs inside the handlers with the shared
	// trust-on-first-init / sealed-window fallbacks (see
	// middleware.BeginTenantBarrierInit / AuthorizeTenantBarrierInitProof /
	// AuthorizeTenantBarrierSealedOp), identical to REST, gRPC, and MCP.
	"tenant.BarrierInitBegin":          permission.IdentityGated,
	"tenant.BarrierInitComplete":       permission.IdentityGated,
	"tenant.BarrierSignedInitBegin":    permission.IdentityGated,
	"tenant.BarrierSignedInitComplete": permission.IdentityGated,
	"tenant.BarrierUnseal":             permission.IdentityGated,
	"tenant.BarrierSeal":               permission.TenantBarrierSeal,
	"tenant.BarrierStatus":             permission.IdentityGated,
	"tenant.BarrierOfficerRegister":    permission.TenantBarrierOfficerRegister,
	"tenant.BarrierOfficerList":        permission.TenantBarrierOfficerList,
	"tenant.BarrierShareClaimBegin":    permission.IdentityGated,
	"tenant.BarrierShareClaimComplete": permission.IdentityGated,
	"tenant.BarrierQuorumBegin":        permission.IdentityGated,
	"tenant.BarrierQuorumSubmit":       permission.IdentityGated,
	"tenant.BarrierRekeyStart":         permission.TenantBarrierRekey,
	"tenant.BarrierOfficerRevoke":      permission.TenantBarrierOfficerRevoke,
	"admin.StartShard":                 permission.AdminStartShard,
	"admin.StopShard":                  permission.AdminStopShard,
	"admin.GetShardState":              permission.AdminShardState,
	"admin.GetTopology":                permission.AdminTopology,
	"admin.GetReplicaStates":           permission.AdminReplicaState,
	"admin.GetNodeCompaction":          permission.AdminNodeCompaction,
	"admin.SetNodeCompaction":          permission.AdminNodeCompaction,
	"admin.GetStateHashes":             permission.AdminStateHash,
	"admin.MintJoinToken":              permission.ClusterJoinToken,
	"admin.AddObserverMember":          permission.AdminAddMember,
	"admin.ListPendingJoinRequests":    permission.ClusterJoinToken,
	"admin.ApproveJoinRequest":         permission.ClusterJoinToken,
	"admin.RejectJoinRequest":          permission.ClusterJoinToken,
	"admin.RetireJoinIdentity":         permission.ClusterJoinIdentityRetire,
	"audit.Query":                      permission.AuditQuery,
	"audit.VerifyChain":                permission.AuditVerify,
	"audit.VerifyReplicatedChain":      permission.AuditVerifyReplicated,
	"authfactor.Enroll":                permission.AuthFactorEnroll,
	"authfactor.Challenge":             permission.AuthFactorChallenge,
	"authfactor.Verify":                permission.AuthFactorVerify,
	"authfactor.List":                  permission.AuthFactorList,
	"authfactor.Revoke":                permission.AuthFactorRevoke,
	"barrier.RecoveryDelete":           permission.SealGenerateRecovery,
	"barrier.RecoveryGenerate":         permission.SealGenerateRecovery,
	"barrier.RecoveryStatus":           permission.SealStatus,
	"ceremony.Initialize":              permission.CeremonyInitialize,
	"ceremony.SignCSR":                 permission.CeremonySignCSR,
	"ceremony.Status":                  permission.CeremonyStatus,
	"collection.Create":                permission.CollectionsCreate,
	"collection.Drop":                  permission.CollectionsDrop,
	"collection.GetInfo":               permission.CollectionsRead,
	"collection.List":                  permission.CollectionsList,
	"collection.InsertDocument":        permission.CollectionsInsert,
	"collection.GetDocument":           permission.CollectionsRead,
	"collection.ListDocuments":         permission.CollectionsList,
	"collection.UpdateDocument":        permission.CollectionsUpdate,
	"collection.DeleteDocument":        permission.CollectionsDelete,
	"credential.Submit":                permission.CredentialsSubmit,
	"credential.Strategy":              permission.CredentialsStrategy,
	"credential.List":                  permission.CredentialsList,
	"credential.Delete":                permission.CredentialsDelete,
	"credential.Rotate":                permission.CredentialsRotate,
	"credential.GCExpired":             permission.CredentialsGCExpired,
	"credential.RetrieveVersion":       permission.CredentialsRetrieveVersion,
	"credential.ListVersions":          permission.CredentialsListVersions,
	"credential.Store":                 permission.CredentialsStore,
	"credential.Retrieve":              permission.CredentialsRetrieve,
	"dek.Status":                       permission.DEKStatus,
	"dek.Rotate":                       permission.DEKRotate,
	"dek.Retire":                       permission.DEKRetire,
	"dek.Encrypt":                      permission.DEKEncrypt,
	"dek.Decrypt":                      permission.DEKDecrypt,
	"dek.Rewrap":                       permission.DEKRewrap,
	"abacpolicy.Create":                permission.ABACPoliciesCreate,
	"abacpolicy.Get":                   permission.ABACPoliciesRead,
	"abacpolicy.List":                  permission.ABACPoliciesList,
	"abacpolicy.Update":                permission.ABACPoliciesUpdate,
	"abacpolicy.Delete":                permission.ABACPoliciesDelete,
	"db.Create":                        permission.DatabasesCreate,
	"db.Get":                           permission.DatabasesRead,
	"db.List":                          permission.DatabasesList,
	"db.Drop":                          permission.DatabasesDelete,
	"db.UpdateBarrier":                 permission.DatabasesUpdate,
	"db.UpdateConnection":              permission.DatabasesUpdate,
	"db.UpdateSQL":                     permission.DatabasesUpdate,
	"db.UpdateCompaction":              permission.MVCCCompact,
	"db.UpdateEtcd":                    permission.DatabasesUpdate,
	"db.UpdateProtocol":                permission.DatabasesUpdate,
	"db.UpdateRaft":                    permission.DatabasesUpdate,
	"db.Scale":                         permission.DatabasesScale,
	"db.Seal":                          permission.DatabasesSeal,
	"db.Unseal":                        permission.DatabasesUnseal,
	"db.BarrierStatus":                 permission.DatabasesBarrierStatus,
	"db.BarrierOfficerRegister":        permission.DatabasesOfficerRegister,
	"db.BarrierOfficerList":            permission.DatabasesOfficerList,
	"db.BarrierShamirInitialize":       permission.DatabasesInitializeShamir,
	"db.BarrierShareClaimBegin":        permission.IdentityGated,
	"db.BarrierShareClaimComplete":     permission.IdentityGated,
	"db.BarrierQuorumBegin":            permission.IdentityGated,
	"db.BarrierQuorumSubmit":           permission.IdentityGated,
	"db.BarrierRekeyStart":             permission.DatabasesRekey,
	"db.BarrierOfficerRevoke":          permission.DatabasesOfficerRevoke,
	"db.BarrierOfficerUnenrol":         permission.DatabasesOfficerUnenrol,
	"sql.Query":                        permission.KVRange,
	"lock.Acquire":                     permission.LocksAcquire,
	"lock.TryLock":                     permission.LocksAcquire,
	"lock.Release":                     permission.LocksRelease,
	"lock.Extend":                      permission.LocksExtend,
	"lock.Status":                      permission.LocksStatus,
	"lock.List":                        permission.LocksList,
	"tenant.Create":                    permission.TenantsCreate,
	"tenant.Get":                       permission.TenantsRead,
	"tenant.List":                      permission.TenantsList,
	"tenant.Update":                    permission.TenantsUpdate,
	"tenant.UpdateQuotas":              permission.TenantsSetQuotas,
	"tenant.UpdateEntitlements":        permission.TenantsSetEntitlements,
	"tenant.ListDeleted":               permission.TenantsListDeleted,
	"tenant.ReconcileDeleted":          permission.TenantsReconcileDeleted,
	"tenant.Delete":                    permission.TenantsDelete,
	"volumepool.List":                  permission.VolumePoolList,
	"volumepool.Get":                   permission.VolumePoolRead,
	"volumepool.Create":                permission.VolumePoolCreate,
	"volumepool.Update":                permission.VolumePoolUpdate,
	"volumepool.Delete":                permission.VolumePoolDelete,
	"volumepool.ListTenants":           permission.VolumePoolRead,
	"tenant.TrustAnchorAdd":            permission.TrustAnchorsAdd,
	"tenant.TrustAnchorList":           permission.TrustAnchorsList,
	"tenant.TrustAnchorRemove":         permission.TrustAnchorsRemove,
	"backup.Create":                    permission.BackupCreate,
	"backup.List":                      permission.BackupList,
	"backup.Get":                       permission.BackupRead,
	"backup.Delete":                    permission.BackupDelete,
	"backup.Restore":                   permission.BackupRestore,
	"backup.Verify":                    permission.BackupVerify,
	"rebalance.GetShardStats":          permission.RebalanceStats,
	"rebalance.CreatePlan":             permission.RebalancePlan,
	"rebalance.Execute":                permission.RebalanceExecute,
	"rebalance.GetImbalanceReport":     permission.RebalanceReport,
	"embedding.Embed":                  permission.EmbeddingEmbed,
	"embedding.BatchEmbed":             permission.EmbeddingEmbed,
	"embedding.ListModels":             permission.EmbeddingListModels,
	"rag.IngestDocument":               permission.RAGIngest,
	"rag.Query":                        permission.RAGQuery,
	"rag.DeleteDocument":               permission.RAGDeleteDocument,
	"rag.ListDocuments":                permission.RAGListDocuments,
	"userauthfactor.Add":               permission.UserAuthFactorsAdd,
	"userauthfactor.Get":               permission.UserAuthFactorsGet,
	"userauthfactor.List":              permission.UserAuthFactorsList,
	"userauthfactor.Remove":            permission.UserAuthFactorsRemove,
	"userauthfactor.Delete":            permission.UserAuthFactorsAdd,
	"userauthfactor.Rotate":            permission.UserAuthFactorsRotate,
	"userauthfactor.Relabel":           permission.UserAuthFactorsRelabel,
	"userauthfactor.Login":             permission.UserAuthFactorsLogin,
	"user.GetByUsername":               permission.UsersRead,
	"user.GetByEmail":                  permission.UsersRead,
}

// guardAllowAuthorizer permits everything and counts calls, so the guard can
// observe whether the fine-grained authorizer was consulted.
type guardAllowAuthorizer struct {
	calls int
}

func (a *guardAllowAuthorizer) Authorize(context.Context, *auth.Identity, string, string) error {
	a.calls++
	return nil
}

// TestMethodMappingGuard_RegistryMatchesExpectations asserts exact equality
// between the registry a real server builds and the curated table, in both
// directions. The registry is populated unconditionally (handleIf registers
// the permission even for un-wired services, preserving the static-table
// contract), so a minimally wired server carries the full surface.
func TestMethodMappingGuard_RegistryMatchesExpectations(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	entries := srv.methodPerms.Entries()

	for method, want := range expectedMethodPerms {
		got, ok := entries[method]
		if !ok {
			t.Errorf("method %q missing from registry (was in the authoritative table)", method)
			continue
		}
		if got != want {
			t.Errorf("method %q: registry permission %q, want %q", method, got, want)
		}
	}
	for method := range entries {
		if _, ok := expectedMethodPerms[method]; !ok {
			t.Errorf("method %q registered but not in the authoritative table; update the guard deliberately", method)
		}
	}
	if len(entries) != len(expectedMethodPerms) {
		t.Errorf("registry has %d methods, authoritative table has %d", len(entries), len(expectedMethodPerms))
	}
}

// TestMethodMappingGuard_DeadAliasesGone pins the cutover cleanup: the old
// table's five dead aliases (methods that never had a dispatch handler), and
// the share verbs the unified holder quorum retired, must NOT reappear in the
// registry — a request for them is denied at
// authorization instead of authorized and then StatusUnimplemented.
func TestMethodMappingGuard_DeadAliasesGone(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	for _, method := range []string{
		"barrier.UnsealShare",
		"barrier.UnsealShares",
		"barrier.GenerateRecovery",
		"barrier.RecoverWithKeys",
		"ceremony.ClaimShare",
		// Retired by the unified share-holder quorum: every share now goes
		// through barrier.QuorumSubmit from an authenticated holder.
		"barrier.ShamirUnsealShare",
		"barrier.ShamirUnsealShares",
		"barrier.RecoveryRecover",
		"barrier.RootToken",
		"barrier.Rekey",
		"barrier.ShamirSealVote",
		"ceremony.SubmitUnsealShare",
		"ceremony.ClaimCertBegin",
		"ceremony.ClaimCertComplete",
		"ceremony.ClaimShareBegin",
		"ceremony.ClaimShareComplete",
		"tenant.BarrierUnsealShare",
		"tenant.BarrierShamirSealVote",
	} {
		if srv.methodPerms.Covered(method) {
			t.Errorf("dead alias %q reappeared in the registry", method)
		}
		if _, ok := srv.handlers[method]; ok {
			t.Errorf("dead alias %q gained a dispatch handler; remove it from this guard deliberately", method)
		}
	}
}

// TestMethodMappingGuard_EveryHandlerCovered re-checks the startup
// assertion's invariant from the outside: every dispatch-map handler carries
// a registered permission.
func TestMethodMappingGuard_EveryHandlerCovered(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	for method := range srv.handlers {
		if !srv.methodPerms.Covered(method) {
			t.Errorf("handler %q has no registered permission (default-deny would make it unreachable)", method)
		}
	}
}

// TestMethodMappingGuard_PermissionsRegistered verifies every permission
// referenced by the registry exists in the global permission catalog, except
// the deliberately unregistered identity-gated sentinel (claim routes are
// authorized at the handler layer, and the sentinel must never be grantable
// to a role).
func TestMethodMappingGuard_PermissionsRegistered(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	for method, perm := range srv.methodPerms.Entries() {
		if perm == permission.IdentityGated {
			continue
		}
		if !permission.IsRegistered(perm) {
			t.Errorf("QUIC method %q declares unregistered permission %q", method, perm)
		}
	}
}

// TestMethodMappingGuard_AllKeysUseDotNotation preserves the frame protocol's
// "<domain>.<Method>" naming invariant.
func TestMethodMappingGuard_AllKeysUseDotNotation(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	for method := range srv.methodPerms.Entries() {
		if !strings.Contains(method, ".") {
			t.Errorf("QUIC method key %q does not use dot notation", method)
		}
	}
}

// TestMethodMappingGuard_DefaultDenyRegression drives AuthorizeQUIC through
// the server's authorize path against the REAL registry: an unknown method is
// denied without consulting the fine-grained authorizer; a deleted dead alias
// is likewise denied; a registered method reaches the authorizer with its
// declared permission and succeeds.
func TestMethodMappingGuard_DefaultDenyRegression(t *testing.T) {
	authz := &guardAllowAuthorizer{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{},
		WithAuthMiddleware(authmw.New(authz, authmw.WithRoleResolver(authtest.BuiltInRoles(t)))),
	)
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{
		Subject:  "guard-user",
		TenantID: "t1",
		Roles:    []string{"admin"},
	})

	if err := srv.authorize(ctx, "totally.Unknown"); err == nil {
		t.Error("unknown method must be denied (default-deny)")
	}
	if err := srv.authorize(ctx, "barrier.UnsealShare"); err == nil {
		t.Error("dead alias barrier.UnsealShare must be denied (default-deny)")
	}
	if authz.calls != 0 {
		t.Errorf("authorizer consulted %d times for denied methods, want 0", authz.calls)
	}

	// A few known methods spanning permission families resolve and authorize.
	for _, method := range []string{"kv.Put", "admin.Health", "tenant.UpdateEntitlements", "audit.Query"} {
		if err := srv.authorize(ctx, method); err != nil {
			t.Errorf("registered method %q: unexpected authorize error: %v", method, err)
		}
	}
	if authz.calls != 4 {
		t.Errorf("authorizer consulted %d times for allowed methods, want 4", authz.calls)
	}
}

// TestMethodMappingGuard_QUICPermissionsHaveGRPCCounterparts verifies each
// QUIC permission is also carried by some gRPC method's (qrdb.v1.permission)
// proto option, except the documented QUIC-only families. Moved from the old
// table-driven mapping_test.go leg in pkg/auth/middleware; it now iterates
// the real registry.
func TestMethodMappingGuard_QUICPermissionsHaveGRPCCounterparts(t *testing.T) {
	grpcPerms := make(map[permission.Permission]struct{})
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if fd.Package() != "qrdb.v1" {
			return true
		}
		svcs := fd.Services()
		for i := 0; i < svcs.Len(); i++ {
			svc := svcs.Get(i)
			methods := svc.Methods()
			for j := 0; j < methods.Len(); j++ {
				full := "/" + string(svc.FullName()) + "/" + string(methods.Get(j).Name())
				if perm, ok := authmw.LookupGRPCFromDescriptor(full); ok {
					grpcPerms[perm] = struct{}{}
				}
			}
		}
		return true
	})
	if len(grpcPerms) == 0 {
		t.Fatal("no qrdb.v1 method carries a (qrdb.v1.permission) option; descriptor resolution is broken")
	}

	// QUIC-only permissions that have no gRPC proto counterpart.
	quicOnly := map[permission.Permission]struct{}{
		// IAM permissions (users, roles, certs) are QUIC-only for now.
		permission.UsersCreate:   {},
		permission.UsersRead:     {},
		permission.UsersUpdate:   {},
		permission.UsersDelete:   {},
		permission.UsersList:     {},
		permission.UsersEnable:   {},
		permission.UsersDisable:  {},
		permission.RolesCreate:   {},
		permission.RolesRead:     {},
		permission.RolesUpdate:   {},
		permission.RolesDelete:   {},
		permission.RolesList:     {},
		permission.RolesAssign:   {},
		permission.RolesUnassign: {},
		permission.CertsRevoke:   {},
		permission.CertsList:     {},
		// Rich TS operations use JSON encoding (no proto messages).
		permission.TSWritePoint:   {},
		permission.TSRichQuery:    {},
		permission.TSDeleteSeries: {},
		permission.TSGetRetention: {},
		permission.TSSetRetention: {},
		permission.TSListMetrics:  {},
		// Phase 2 compliance permissions — gRPC proto stubs pending.
		// These appear in both REST and QUIC but not yet in gRPC.
		permission.SealBootstrap:        {},
		permission.SealUnseal:           {},
		permission.SealSeal:             {},
		permission.SealStatus:           {},
		permission.SealInitializeShamir: {},
		permission.SealRekey:            {},
		permission.SealGenerateRecovery: {},
		permission.CeremonyInitialize:   {},
		permission.CeremonyStatus:       {},
		permission.CeremonySignCSR:      {},
		permission.AuditQuery:           {},
		permission.AuthFactorEnroll:     {},
		permission.AuthFactorChallenge:  {},
		permission.AuthFactorVerify:     {},
		permission.AuthFactorList:       {},
		permission.AuthFactorRevoke:     {},
		// Per-user auth-factor lifecycle: QUIC/REST/MCP-only (no gRPC method).
		permission.UserAuthFactorsAdd:     {},
		permission.UserAuthFactorsGet:     {},
		permission.UserAuthFactorsList:    {},
		permission.UserAuthFactorsRemove:  {},
		permission.UserAuthFactorsRotate:  {},
		permission.UserAuthFactorsRelabel: {},
		permission.UserAuthFactorsLogin:   {},
	}

	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	for method, perm := range srv.methodPerms.Entries() {
		if _, ok := grpcPerms[perm]; !ok {
			if _, skip := quicOnly[perm]; skip {
				continue
			}
			t.Errorf("QUIC method %q maps to %q which has no gRPC counterpart", method, perm)
		}
	}
}
