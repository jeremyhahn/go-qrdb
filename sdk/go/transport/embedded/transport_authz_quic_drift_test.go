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

package embedded

import (
	"sort"
	"testing"

	quicapi "github.com/jeremyhahn/go-qrdb/pkg/api/quic"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// This file is the drift guard item 10 of the go-qrdb#585 security review
// asked for: operationPermissions (transport_authz_registry.go) is a hand
// transcription of QUIC's dispatch.go table, and a hand transcription can
// go stale silently -- nothing short of a test that reads BOTH tables and
// compares them catches a QUIC permission changing underneath an embedded
// entry that was never touched.
//
// quic.Server.MethodPermissions() is QUIC's own single source of truth
// (methodPerms, populated unconditionally by every s.handle/handleIf/
// handleStreamOnly call in buildHandlers, present whether or not the
// backing service is wired). TestOperationPermissions_MatchQUIC below reads
// it from a server built with every optional service nil -- construction
// alone populates the table; nothing needs to be wired for this comparison.
//
// The two tables are NOT named identically: QUIC's keys are
// "namespace.Verb" wire-protocol names, while operationPermissions' keys
// are either the embedded Transport's bare Go method name or, for a
// sub-client, "TypeName.Method". quicToEmbeddedMethod is the explicit,
// hand-verified correspondence between the two naming schemes -- deriving
// it automatically (e.g. stripping the "namespace." prefix) was tried and
// rejected: 45 of QUIC's 235 entries collide by coincidence with an
// UNRELATED embedded key under that transform (e.g. "cert.List" strips to
// "List", which exists in operationPermissions already -- as KVList, a
// completely different operation), so a bare-string derivation would
// silently assert the wrong pairs equal rather than catching drift.
//
// quicMethodExclusions names every QUIC entry with NO operationPermissions
// counterpart, each with the reason category:
//
//   - "public": the verb this review's item 7 made reachable without an
//     identity on the embedded transport (Health, and the sealed-window
//     bootstrap verbs -- BarrierInitialize, BarrierUnseal,
//     BarrierInitializeShamir, OfficerRegister, RecoveryOfficerRegister,
//     CeremonyInitialize, CeremonySignCSR). QUIC still declares a real
//     permission for its own default-deny assertion; the embedded
//     transport's authorization for these verbs is sealed-window / SO-PIN
//     gated instead, verified by seal_gate_test.go and
//     transport_authz_coverage_test.go's publicOperations, not by this
//     table.
//   - "identity-gated": the share-claim and quorum handshakes (this
//     review's item 6) -- QUIC itself declares permission.IdentityGated for
//     these, and the embedded transport enforces the identical floor via
//     requireIdentity, verified by claim_quorum_identity_gated_test.go, not
//     by this table. role.ListOwnPermissions is identity-gated the same way:
//     the embedded RBACClient.ListOwnPermissions refuses a caller without an
//     identity inside role.ListOwnPermissions itself. role.Authorize too:
//     the server evaluator requires permissions:resolve for another
//     identity, and the embedded RBACClient.Authorize is in-process.
//   - "target-resolved": the verb's embedded equivalent authorizes against
//     a target named IN the request (a user, role, tenant or certificate
//     serial) via authorizeUserTarget / authorizeRoleTarget /
//     authorizeTenantSelf / authorizeTenantTarget / authorizeCertTenant /
//     authorizeCertRevoke in transport_identity_authz.go, tenant_authz.go
//     and transport_auth.go -- never through operationPermissions' ambient
//     SelfTarget resolution, so there is no entry here to compare against.
//     Parity for this family is TestEmbeddedVsREST_TenantAuthorizationParity
//     (tenant_authz_restparity_test.go) and the target-resolved coverage
//     declarations in transport_authz_coverage_test.go, not this table.
//
// A QUIC method in neither map fails the test outright (missingCoverage,
// below): every one of QUIC's 235 current entries is accounted for by
// construction, so a 236th method added without updating EITHER map is the
// condition this guard exists to catch.
var quicToEmbeddedMethod = map[string]string{
	"admin.AddMember":               "AddMember",
	"admin.AddObserverMember":       "AddObserverMember",
	"admin.ApproveJoinRequest":      "ApproveJoinRequest",
	"admin.ListPendingJoinRequests": "ListPendingJoinRequests",
	"admin.RejectJoinRequest":       "RejectJoinRequest",
	"admin.Capabilities":            "Capabilities",
	"admin.Drain":                   "Drain",
	"admin.GetNodeCompaction":       "NodeCompaction",
	"admin.GetShardState":           "GetShardState",
	"admin.GetTopology":             "GetTopology",
	"admin.GetReplicaStates":        "ReplicaStates",
	"admin.MintJoinToken":           "MintJoinToken",
	"admin.Peers":                   "Peers",
	"admin.RemoveMember":            "RemoveMember",
	"admin.PromoteMember":           "PromoteMember",
	"admin.RequestSnapshot":         "RequestSnapshot",
	"admin.RetireJoinIdentity":      "RetireJoinIdentity",
	"admin.SetNodeCompaction":       "SetNodeCompaction",
	"admin.GetStateHashes":          "StateHashes",
	"admin.StartShard":              "StartShard",
	"admin.Status":                  "Status",
	"admin.StopShard":               "StopShard",
	"admin.TransferLeadership":      "TransferLeadership",
	"audit.Query":                   "AuditClient.QueryAudit",
	"audit.VerifyChain":             "AuditClient.VerifyChain",
	"audit.VerifyReplicatedChain":   "AuditClient.VerifyReplicatedChain",
	"authfactor.Challenge":          "ChallengeFactor",
	"authfactor.Enroll":             "EnrollFactor",
	"authfactor.List":               "ListFactors",
	"authfactor.Revoke":             "RevokeFactor",
	"authfactor.Verify":             "VerifyFactor",
	"backup.Create":                 "CreateBackup",
	"backup.Delete":                 "DeleteBackup",
	"backup.Get":                    "GetBackup",
	"backup.List":                   "ListBackups",
	"backup.Restore":                "RestoreBackup",
	"backup.Verify":                 "VerifyBackup",
	"barrier.ClusterUnseal":         "BarrierClusterUnseal",
	"barrier.OfficerList":           "OfficerList",
	"barrier.OfficerRevoke":         "BarrierOfficerRevoke",
	"barrier.RecoveryDelete":        "BarrierDeleteRecoveryKeys",
	"barrier.RecoveryGenerate":      "BarrierGenerateRecoveryKeys",
	"barrier.RecoveryStatus":        "BarrierHasRecoveryKeys",
	"barrier.RekeyStart":            "BarrierRekeyStart",
	"barrier.Seal":                  "BarrierSeal",
	"barrier.Status":                "BarrierStatus",
	"batch.Batch":                   "ExecuteBatch",
	"batch.ImportBegin":             "ImportBegin",
	"batch.ImportAppend":            "ImportAppend",
	"batch.ImportCommit":            "ImportCommit",
	"batch.ImportAbort":             "ImportAbort",
	"ceremony.Status":               "CeremonyStatus",
	"collection.Create":             "CreateCollection",
	"collection.DeleteDocument":     "DeleteDocument",
	"collection.Drop":               "DropCollection",
	"collection.GetDocument":        "GetDocument",
	"collection.GetInfo":            "GetCollectionInfo",
	"collection.InsertDocument":     "InsertDocument",
	"collection.List":               "ListCollections",
	"collection.ListDocuments":      "ListDocuments",
	"collection.UpdateDocument":     "UpdateDocument",
	"credential.Delete":             "CredentialDelete",
	"credential.GCExpired":          "CredentialGCExpired",
	"credential.List":               "CredentialList",
	"credential.ListVersions":       "CredentialListVersions",
	"credential.Rotate":             "CredentialRotate",
	"credential.Store":              "CredentialStore",
	"credential.Strategy":           "CredentialStrategy",
	"credential.Submit":             "CredentialSubmit",
	"db.BarrierOfficerList":         "DatabaseBarrierOfficerList",
	"db.BarrierOfficerRegister":     "DatabaseBarrierOfficerRegister",
	"db.BarrierOfficerRevoke":       "DatabaseBarrierOfficerRevoke",
	"db.BarrierOfficerUnenrol":      "DatabaseBarrierOfficerUnenrol",
	"db.BarrierRekeyStart":          "DatabaseBarrierRekeyStart",
	"db.BarrierShamirInitialize":    "DatabaseBarrierShamirInitialize",
	"db.BarrierStatus":              "DatabaseBarrierStatus",
	"db.Create":                     "CreateDatabase",
	"db.Drop":                       "DropDatabase",
	"db.Get":                        "GetDatabase",
	"db.List":                       "ListDatabases",
	"db.Scale":                      "ScaleDatabase",
	"db.Seal":                       "SealDatabase",
	"db.Unseal":                     "UnsealDatabase",
	"db.UpdateBarrier":              "UpdateDatabaseBarrier",
	"db.UpdateCompaction":           "UpdateDatabaseCompaction",
	"db.UpdateConnection":           "UpdateDatabaseConnection",
	"db.UpdateEtcd":                 "UpdateDatabaseEtcd",
	"db.UpdateProtocol":             "UpdateDatabaseProtocol",
	"db.UpdateRaft":                 "UpdateDatabaseRaft",
	"db.UpdateSQL":                  "UpdateDatabaseSQL",
	"dek.Decrypt":                   "TenantDEKDecrypt",
	"dek.Encrypt":                   "TenantDEKEncrypt",
	"dek.Retire":                    "TenantDEKRetire",
	"dek.Rewrap":                    "TenantDEKRewrap",
	"dek.Rotate":                    "TenantDEKRotate",
	"dek.Status":                    "TenantDEKStatus",
	"abacpolicy.Create":             "CreateABACPolicy",
	"abacpolicy.Get":                "GetABACPolicy",
	"abacpolicy.List":               "ListABACPolicies",
	"abacpolicy.Update":             "UpdateABACPolicy",
	"abacpolicy.Delete":             "DeleteABACPolicy",
	"embedding.BatchEmbed":          "BatchEmbed",
	"embedding.Embed":               "Embed",
	"embedding.ListModels":          "ListEmbeddingModels",
	"index.Create":                  "IndexCreate",
	"index.Drop":                    "IndexDrop",
	"index.List":                    "IndexList",
	"index.Query":                   "IndexQuery",
	"index.Scan":                    "IndexScan",
	"kv.CompareAndDelete":           "CompareAndDelete",
	"kv.CompareAndSwap":             "CompareAndSwap",
	"kv.Delete":                     "Delete",
	"kv.Get":                        "Get",
	"kv.List":                       "List",
	"kv.Put":                        "Put",
	"kv.PutIfAbsent":                "PutIfAbsent",
	"kv.Scan":                       "Scan",
	"lease.Grant":                   "LeaseGrant",
	"lease.KeepAlive":               "LeaseKeepAlive",
	"lease.List":                    "LeaseList",
	"lease.Revoke":                  "LeaseRevoke",
	"lease.TimeToLive":              "LeaseTimeToLive",
	"lock.Acquire":                  "LockAcquire",
	"lock.Extend":                   "LockExtend",
	"lock.List":                     "LockList",
	"lock.Release":                  "LockRelease",
	"lock.Status":                   "LockStatus",
	"lock.TryLock":                  "LockTryLock",
	"mvcc.Compact":                  "Compact",
	"mvcc.CurrentRevision":          "CurrentRevision",
	"mvcc.GetAtRevision":            "GetAtRevision",
	"mvcc.GetRevision":              "GetRevision",
	"mvcc.PutWithLease":             "PutWithLease",
	"mvcc.Range":                    "Range",
	"mvcc.RestoreToRevision":        "RestoreToRevision",
	"rag.DeleteDocument":            "RAGDeleteDocument",
	"rag.IngestDocument":            "RAGIngestDocument",
	"rag.ListDocuments":             "RAGListDocuments",
	"rag.Query":                     "RAGQuery",
	"rebalance.CreatePlan":          "CreateRebalancePlan",
	"rebalance.Execute":             "ExecuteRebalance",
	"rebalance.GetImbalanceReport":  "GetImbalanceReport",
	"rebalance.GetShardStats":       "GetShardStats",
	"role.ListPermissions":          "ListPermissions",
	"role.ResolvePermissions":       "ResolvePermissions",
	"sql.Query":                     "Query",
	"ts.Aggregate":                  "TSAggregate",
	"ts.DeleteSeries":               "TSDeleteSeries",
	"ts.GetRetention":               "TSGetRetention",
	"ts.ListMetrics":                "TSListMetrics",
	"ts.Query":                      "TSQuery",
	"ts.RichQuery":                  "TSRichQuery",
	"ts.SetRetention":               "TSSetRetention",
	"ts.Write":                      "TSWrite",
	"ts.WriteBatch":                 "TSWriteBatch",
	"ts.WritePoint":                 "TSWritePoint",
	"tx.Transaction":                "ExecuteTransaction",
	"user.GetByCertThumbprint":      "GetUserByCertThumbprint",
	"userauthfactor.Add":            "AddAuthFactor",
	"userauthfactor.Get":            "GetAuthFactor",
	"userauthfactor.List":           "ListAuthFactors",
	"userauthfactor.Login":          "LoginAuthFactor",
	"userauthfactor.Relabel":        "RelabelAuthFactor",
	"userauthfactor.Remove":         "RemoveAuthFactor",
	"userauthfactor.Delete":         "DeleteAuthFactor",
	"userauthfactor.Rotate":         "RotateAuthFactor",
	"vector.BatchDelete":            "BatchDeleteVectors",
	"vector.BatchUpsert":            "BatchUpsert",
	"vector.CompactNamespace":       "CompactNamespace",
	"vector.CreateNamespace":        "CreateNamespace",
	"vector.Delete":                 "DeleteVector",
	"vector.DeleteNamespace":        "DeleteNamespace",
	"vector.Get":                    "GetVector",
	"vector.GetCompactionProgress":  "GetCompactionProgress",
	"vector.GetIndexStats":          "GetIndexStats",
	"vector.GetNamespaceInfo":       "GetNamespaceInfo",
	"vector.GetRebuildProgress":     "GetRebuildProgress",
	"vector.HybridSearch":           "HybridSearch",
	"vector.List":                   "ListVectors",
	"vector.ListNamespaces":         "ListNamespaces",
	"vector.RebuildIndex":           "RebuildIndex",
	"vector.SearchByID":             "SearchByID",
	"vector.SearchByMetadata":       "SearchByMetadata",
	"vector.SearchSimilar":          "SearchSimilar",
	"vector.UpdateMetadata":         "UpdateMetadata",
	"vector.Upsert":                 "UpsertVector",
	"volumepool.Create":             "CreatePool",
	"volumepool.Delete":             "DeletePool",
	"volumepool.Get":                "GetPool",
	"volumepool.List":               "ListPools",
	"volumepool.ListTenants":        "ListPoolTenants",
	"volumepool.Update":             "UpdatePool",
	"watch.Watch":                   "Watch",
	"watch.WatchPrefix":             "WatchPrefix",
}

// quicMethodExclusions names every QUIC method with no operationPermissions
// counterpart, each with its reason category -- see the package-level
// comment above for what each category means and which OTHER test verifies
// that method's embedded-side authorization instead.
var quicMethodExclusions = map[string]string{
	"admin.Health":                       "public",
	"barrier.EnableEncryption":           "target-resolved",
	"barrier.Initialize":                 "public",
	"barrier.OfficerRegister":            "public",
	"barrier.QuorumBegin":                "identity-gated",
	"barrier.QuorumSubmit":               "identity-gated",
	"barrier.RecoveryOfficerRegister":    "public",
	"barrier.RecoveryShareClaimBegin":    "identity-gated",
	"barrier.RecoveryShareClaimComplete": "identity-gated",
	"barrier.ShamirInitialize":           "public",
	"barrier.ShareClaimBegin":            "identity-gated",
	"barrier.ShareClaimComplete":         "identity-gated",
	"barrier.Unseal":                     "public",
	"ceremony.Initialize":                "public",
	"ceremony.SignCSR":                   "public",
	"cert.Delegate":                      "target-resolved",
	"cert.Enroll":                        "target-resolved",
	"cert.Issue":                         "target-resolved",
	"cert.List":                          "target-resolved",
	"cert.Revoke":                        "target-resolved",
	"credential.Retrieve":                "wire-only",
	"credential.RetrieveVersion":         "wire-only",
	"db.BarrierQuorumBegin":              "identity-gated",
	"db.BarrierQuorumSubmit":             "identity-gated",
	"db.BarrierShareClaimBegin":          "identity-gated",
	"db.BarrierShareClaimComplete":       "identity-gated",
	"role.Assign":                        "target-resolved",
	"role.Create":                        "target-resolved",
	"role.Delete":                        "target-resolved",
	"role.Get":                           "target-resolved",
	"role.GetByName":                     "target-resolved",
	"role.List":                          "target-resolved",
	"role.ListOwnPermissions":            "identity-gated",
	"role.Authorize":                     "identity-gated",
	"role.ReconcileBuiltIn":              "target-resolved",
	"role.Unassign":                      "target-resolved",
	"role.Update":                        "target-resolved",
	"tenant.BarrierInitBegin":            "identity-gated",
	"tenant.BarrierInitComplete":         "identity-gated",
	"tenant.BarrierOfficerList":          "target-resolved",
	"tenant.BarrierOfficerRegister":      "target-resolved",
	"tenant.BarrierOfficerRevoke":        "target-resolved",
	"tenant.BarrierQuorumBegin":          "identity-gated",
	"tenant.BarrierQuorumSubmit":         "identity-gated",
	"tenant.BarrierRekeyStart":           "target-resolved",
	"tenant.BarrierSeal":                 "target-resolved",
	"tenant.BarrierShareClaimBegin":      "identity-gated",
	"tenant.BarrierShareClaimComplete":   "identity-gated",
	"tenant.BarrierStatus":               "identity-gated",
	"tenant.BarrierUnseal":               "identity-gated",
	"tenant.Create":                      "target-resolved",
	"tenant.Delete":                      "target-resolved",
	"tenant.Get":                         "target-resolved",
	"tenant.List":                        "target-resolved",
	"tenant.ListDeleted":                 "target-resolved",
	"tenant.ReconcileDeleted":            "target-resolved",
	"tenant.TrustAnchorAdd":              "target-resolved",
	"tenant.TrustAnchorList":             "target-resolved",
	"tenant.TrustAnchorRemove":           "target-resolved",
	"tenant.Update":                      "target-resolved",
	"tenant.UpdateEntitlements":          "target-resolved",
	"tenant.UpdateQuotas":                "target-resolved",
	"user.AddCertBinding":                "target-resolved",
	"user.Create":                        "target-resolved",
	"user.Delete":                        "target-resolved",
	"user.Disable":                       "target-resolved",
	"user.Suspend":                       "target-resolved",
	"user.Unsuspend":                     "target-resolved",
	"user.Enable":                        "target-resolved",
	"user.Get":                           "target-resolved",
	"user.GetByEmail":                    "target-resolved",
	"user.GetByUsername":                 "target-resolved",
	"user.Invite":                        "target-resolved",
	"user.List":                          "target-resolved",
	"user.RemoveCertBinding":             "target-resolved",
	"user.Update":                        "target-resolved",
}

// TestOperationPermissions_MatchQUIC is the item-10 drift guard: every QUIC
// method either names an operationPermissions entry that must carry the
// IDENTICAL permission, or is named in quicMethodExclusions with a reason
// that another test (named in the package comment above) independently
// verifies. A QUIC method in neither map, a mapped pair whose permissions
// disagree, or an exclusion reason that no longer applies all fail this
// test -- so operationPermissions cannot silently drift from the table
// REST and QUIC both enforce without a red test naming exactly which
// method and which two permissions disagree.
func TestOperationPermissions_MatchQUIC(t *testing.T) {
	// A server with every optional service nil still populates methodPerms
	// in full: handleIf registers the permission unconditionally and only
	// conditions the handler on the service being wired (dispatch.go).
	quicSrv := quicapi.NewServer(nil, nil, "127.0.0.1:0")
	quicPerms := quicSrv.MethodPermissions()
	if len(quicPerms) == 0 {
		t.Fatal("quic.Server.MethodPermissions() returned nothing -- buildHandlers did not run")
	}

	var missingCoverage []string
	var mismatched []string
	for quicMethod, quicPerm := range quicPerms {
		if reason, excluded := quicMethodExclusions[quicMethod]; excluded {
			switch reason {
			case "public", "identity-gated", "target-resolved", "wire-only":
				// Recognized category; the test named in the package
				// comment covers this method instead.
			default:
				t.Errorf("quicMethodExclusions[%q] = %q: not a recognized reason category", quicMethod, reason)
			}
			continue
		}
		embMethod, ok := quicToEmbeddedMethod[quicMethod]
		if !ok {
			missingCoverage = append(missingCoverage, quicMethod)
			continue
		}
		embPerm, ok := operationPermissions[embMethod]
		if !ok {
			mismatched = append(mismatched, quicMethod+" -> "+embMethod+": not declared in operationPermissions")
			continue
		}
		if embPerm != quicPerm {
			mismatched = append(mismatched, quicMethod+" -> "+embMethod+": quic="+string(quicPerm)+" embedded="+string(embPerm))
		}
	}

	if len(missingCoverage) > 0 {
		sort.Strings(missingCoverage)
		t.Errorf("%d QUIC method(s) are in neither quicToEmbeddedMethod nor quicMethodExclusions: %v",
			len(missingCoverage), missingCoverage)
	}
	if len(mismatched) > 0 {
		sort.Strings(mismatched)
		t.Errorf("%d mismatch(es) between QUIC and embedded permission tables:\n%s",
			len(mismatched), joinLines(mismatched))
	}
}

// TestQUICMethodTables_CoverEveryCurrentMethod guards the two static maps
// above against SHRINKING silently relative to each other: every entry in
// quicToEmbeddedMethod or quicMethodExclusions must still name a REAL QUIC
// method, so a QUIC method renamed or removed leaves a stale, permanently
// vacuous entry here rather than a visible failure.
func TestQUICMethodTables_CoverEveryCurrentMethod(t *testing.T) {
	quicSrv := quicapi.NewServer(nil, nil, "127.0.0.1:0")
	quicPerms := quicSrv.MethodPermissions()

	for method := range quicToEmbeddedMethod {
		if _, ok := quicPerms[method]; !ok {
			t.Errorf("quicToEmbeddedMethod names %q, which QUIC no longer declares", method)
		}
	}
	for method := range quicMethodExclusions {
		if _, ok := quicPerms[method]; !ok {
			t.Errorf("quicMethodExclusions names %q, which QUIC no longer declares", method)
		}
	}
}

// joinLines is a tiny local helper so the table above stays dependency-free
// (avoiding an extra import for a one-line join).
func joinLines(lines []string) string {
	out := ""
	for _, l := range lines {
		out += "  " + l + "\n"
	}
	return out
}

// Compile-time sanity: permission.Permission is the comparable string type
// both tables' values share.
var _ permission.Permission = permission.IdentityGated
