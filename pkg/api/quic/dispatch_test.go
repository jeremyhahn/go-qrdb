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

import (
	"testing"
)

// expectedMethods is the authoritative list of every method name that
// buildHandlers must register. Each entry corresponds to exactly one
// handler registration in dispatch.go.
var expectedMethods = []string{
	// KV (8)
	"kv.Put",
	"kv.Get",
	"kv.Delete",
	"kv.List",
	"kv.Scan",
	// The guarded write verbs (go-qrdb#124).
	"kv.PutIfAbsent",
	"kv.CompareAndDelete",
	"kv.CompareAndSwap",

	// Admin (15)
	"admin.Health",
	"admin.Status",
	"admin.Peers",
	"admin.AddMember",
	"admin.RemoveMember",
	"admin.PromoteMember",
	"admin.TransferLeadership",
	"admin.RequestSnapshot",
	"admin.Drain",
	"admin.Capabilities",
	"admin.StartShard",
	"admin.StopShard",
	"admin.GetShardState",
	"admin.GetTopology",
	"admin.GetReplicaStates",
	"admin.GetNodeCompaction",
	"admin.SetNodeCompaction",
	"admin.GetStateHashes",
	"admin.MintJoinToken",
	"admin.AddObserverMember",
	"admin.ListPendingJoinRequests",
	"admin.ApproveJoinRequest",
	"admin.RejectJoinRequest",
	"admin.RetireJoinIdentity",

	// MVCC (7)
	"mvcc.PutWithLease",
	"mvcc.GetRevision",
	"mvcc.GetAtRevision",
	"mvcc.Range",
	"mvcc.Compact",
	"mvcc.CurrentRevision",
	"mvcc.RestoreToRevision",

	// Transaction (1)
	"tx.Transaction",

	// Batch (6)
	"batch.Batch",
	"batch.ImportBegin",
	"batch.ImportAppend",
	"batch.ImportCommit",
	"batch.ImportAbort",

	// Watch is server-streaming (handleWatchStream); not in the unary table.

	// Lease (5)
	"lease.Grant",
	"lease.Revoke",
	"lease.KeepAlive",
	"lease.TimeToLive",
	"lease.List",

	// Lock (6)
	"lock.Acquire",
	"lock.TryLock",
	"lock.Release",
	"lock.Extend",
	"lock.Status",
	"lock.List",

	// Index (5)
	"index.Create",
	"index.Drop",
	"index.Query",
	"index.Scan",
	"index.List",

	// TimeSeries (10)
	"ts.Write",
	"ts.WriteBatch",
	"ts.Query",
	"ts.Aggregate",
	"ts.WritePoint",
	"ts.RichQuery",
	"ts.DeleteSeries",
	"ts.GetRetention",
	"ts.SetRetention",
	"ts.ListMetrics",

	// Vector (20)
	"vector.CreateNamespace",
	"vector.DeleteNamespace",
	"vector.ListNamespaces",
	"vector.GetNamespaceInfo",
	"vector.Upsert",
	"vector.Get",
	"vector.Delete",
	"vector.BatchUpsert",
	"vector.BatchDelete",
	"vector.List",
	"vector.UpdateMetadata",
	"vector.SearchSimilar",
	"vector.SearchByMetadata",
	"vector.HybridSearch",
	"vector.SearchByID",
	"vector.RebuildIndex",
	"vector.GetRebuildProgress",
	"vector.GetIndexStats",
	"vector.CompactNamespace",
	"vector.GetCompactionProgress",

	// User management (10)
	"user.Create",
	"user.Get",
	"user.GetByUsername",
	"user.GetByEmail",
	"user.GetByCertThumbprint",
	"user.AddCertBinding",
	"user.RemoveCertBinding",
	"user.Update",
	"user.Delete",
	"user.List",
	"user.Enable",
	"user.Disable",
	"user.Suspend",
	"user.Unsuspend",

	// Role management (10)
	"role.Create",
	"role.Get",
	"role.GetByName",
	"role.Update",
	"role.Delete",
	"role.List",
	"role.Assign",
	"role.Unassign",
	"role.ListPermissions",
	"role.ResolvePermissions",
	"role.ListOwnPermissions",
	"role.Authorize",
	"role.ReconcileBuiltIn",

	// Certificate management (4)
	"cert.Issue",
	"cert.Enroll",
	"cert.Delegate",
	"cert.Revoke",
	"cert.List",

	// Collection (9)
	"collection.Create",
	"collection.Drop",
	"collection.List",
	"collection.GetInfo",
	"collection.InsertDocument",
	"collection.GetDocument",
	"collection.UpdateDocument",
	"collection.DeleteDocument",
	"collection.ListDocuments",

	// Database management (9)
	"db.Create",
	"db.Get",
	"db.List",
	"db.Drop",
	"db.UpdateProtocol",
	"db.UpdateEtcd",
	"db.UpdateRaft",
	"db.Scale",
	"db.UpdateBarrier",
	"db.UpdateConnection",
	"db.UpdateSQL",
	"db.UpdateCompaction",
	"db.Seal",
	"db.Unseal",
	"db.BarrierStatus",
	"db.BarrierOfficerRegister",
	"db.BarrierOfficerList",
	"db.BarrierShamirInitialize",
	"db.BarrierShareClaimBegin",
	"db.BarrierShareClaimComplete",
	"db.BarrierQuorumBegin",
	"db.BarrierQuorumSubmit",
	"db.BarrierRekeyStart",
	"db.BarrierOfficerRevoke",
	"db.BarrierOfficerUnenrol",

	// SQL (1)
	"sql.Query",

	// Barrier (24)
	"barrier.Initialize",
	"barrier.ClusterUnseal",
	"barrier.Unseal",
	"barrier.ChangePassphrase",
	"barrier.Seal",
	"barrier.Status",
	"barrier.ShamirInitialize",
	"barrier.EnableEncryption",
	"barrier.RecoveryGenerate",
	"barrier.RecoveryDelete",
	"barrier.RecoveryStatus",
	"barrier.OfficerRegister",
	"barrier.OfficerList",
	"barrier.ShareClaimBegin",
	"barrier.ShareClaimComplete",
	"barrier.RecoveryOfficerRegister",
	"barrier.RecoveryShareClaimBegin",
	"barrier.RecoveryShareClaimComplete",
	"barrier.QuorumBegin",
	"barrier.QuorumSubmit",
	"barrier.RekeyStart",
	"barrier.OfficerRevoke",

	// Ceremony (9)
	"ceremony.Initialize",
	"ceremony.Status",
	"ceremony.SignCSR",

	// Tenant (12)
	"tenant.Create",
	"tenant.Get",
	"tenant.List",
	"tenant.Update",
	"tenant.Delete",
	"tenant.UpdateQuotas",
	"tenant.UpdateEntitlements",
	"tenant.ListDeleted",
	"tenant.ReconcileDeleted",
	"volumepool.List",
	"volumepool.Get",
	"volumepool.Create",
	"volumepool.Update",
	"volumepool.Delete",
	"volumepool.ListTenants",
	"tenant.TrustAnchorAdd",
	"tenant.TrustAnchorList",
	"tenant.TrustAnchorRemove",
}

func TestBuildHandlers_RegistersAllMethods(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	for _, method := range expectedMethods {
		t.Run(method, func(t *testing.T) {
			if _, ok := srv.handlers[method]; !ok {
				t.Fatalf("method %q not registered in handlers map", method)
			}
		})
	}
}

func TestBuildHandlers_MethodCount(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	// 201 = 200 + barrier.ChangePassphrase, the supported passphrase change
	// of the system barrier (docs/barrier-seal).
	// 200 = 198 + user.Suspend and user.Unsuspend, the suspension verbs
	// granted beside user.Enable and user.Disable (docs/identity-rbac).
	// 198 = 197 + role.Authorize, the server-side Authorize decision,
	// identity-gated with permissions:resolve for another identity.
	// 197 = 196 + role.ListOwnPermissions, the authenticated caller's own
	// effective permissions, identity-gated (docs/identity-rbac).
	// 196 = 195 + admin.TransferLeadership, handing a shard's leadership to a
	// named voter and waiting until it leads (docs/clustering).
	// 195 = 194 + cert.Delegate, a tenant-issued delegation certificate for a
	// service principal (docs/certificate-authority).
	// 194 = 193 + admin.GetStateHashes, every local replica's applied index
	// and state digest (docs/clustering).
	// 193 = 192 + admin.GetReplicaStates, each replica's applied index and
	// state-machine hash on the answering node (docs/clustering).
	// 192 = 187 + 5 verbs for the member-side CSR approval queue, observer
	// join mode, and identity retirement (docs/clustering):
	// admin.AddObserverMember (add a replica as a non-voting observer rather
	// than a voter, promoted later with admin.PromoteMember),
	// admin.ListPendingJoinRequests, admin.ApproveJoinRequest and
	// admin.RejectJoinRequest (reviewing a tokenless CSR queued by
	// pkg/clusterenroll's approval path), and admin.RetireJoinIdentity
	// (permanently refusing any future admission for a node identity).
	// 187 = 186 + admin.PromoteMember, promoting a committed observer of a
	// shard to a voter in place (docs/clustering).
	// 186 = 183 + cert.Issue and cert.Enroll replacing the single retired
	// cert.Generate (net +1), plus tenant.TrustAnchorAdd/List/Remove (+3),
	// minus cert.Export (-1): the tenant-onboarding verbs.
	// 183 = 174 + 4 chunked import session verbs (batch.ImportBegin/
	// ImportAppend/ImportCommit/ImportAbort) - 1 for the retired
	// batch.Import, the single-message RPC these four replace on a
	// dataset bigger than one pre-auth-capped message
	// (docs/transactions/architecture.md "Import session", go-qrdb#576
	// part 2)
	// + the 6 volumepool.* verbs (docs/volume-pools/api.md): List, Get,
	// Create, Update, Delete, ListTenants.
	// cert.Export is retired entirely: encoding a PKCS#12 archive needs
	// the private key, and no private key may reach a node (go-qrdb
	// security review). The archive is now built LOCALLY, in the CLI and the
	// desktop app, through the exported pkg/cert.EncodePKCS12 -- never over
	// any transport. Server-side keygen (cert.Generate) is gone too.
	// 174 = 171 + 3 verbs added independently on top of the shared 171
	// baseline below, none of which replaces another: barrier.ClusterUnseal,
	// the authorized cluster-unseal record (docs/barrier-seal);
	// db.BarrierOfficerUnenrol, removing a database holder before its
	// custody is initialized (docs/databases/api.md); and
	// admin.MintJoinToken, the member-side CSR join-token mint verb (B5/B7,
	// go-qrdb cluster-join-token work).
	// 171 = 162 + the 9 db.Barrier* share-holder verbs of a Shamir database
	// barrier (docs/databases/api.md). audit.VerifyChain, the Tier-1
	// audit-chain verification, is also outside this table: it is registered
	// only when the server has an audit logger, and this one has none.
	// 162 = 172 - 14 share verbs retired by the unified share-holder quorum
	// (7 barrier, 7 ceremony; 3 tenant ones sat outside this table) + the 4
	// barrier quorum/rekey/revoke verbs that replace them. Every share is now
	// submitted by an authenticated holder through barrier.QuorumSubmit.
	// The 172 was 170 + admin.GetNodeCompaction and admin.SetNodeCompaction,
	// the platform's node-keyspace compaction policy (docs/key-value).
	// The 170 was 169 + db.UpdateCompaction, a database's scheduled-compaction
	// policy (docs/key-value).
	// The 169 was 168 + db.UpdateSQL, a database's SQL concurrency mode
	// (docs/row-locking).
	// The 168 was 166 + tenant.ListDeleted and tenant.ReconcileDeleted, the retired
	// tenant id listing and the node-local residue reconcile (go-qrdb#470,
	// go-qrdb#469).
	// The 166 was 165 prior unary handlers + user.RemoveCertBinding, the withdrawal
	// counterpart of user.AddCertBinding and the only way to unenroll a key
	// (go-qrdb#518).
	// The 165 was 164 plus role.ReconcileBuiltIn, the one
	// sanctioned writer to a built-in role's permissions (go-qrdb#354).
	// The 164 was 161 plus the 3 guarded write verbs (kv.PutIfAbsent,
	// kv.CompareAndDelete, kv.CompareAndSwap — go-qrdb#124), and the 161 was
	// 160 plus user.GetByEmail, the canonical login-key lookup (go-qrdb#190).
	// The 2 watch methods remain on the streaming path, not the unary table.
	const wantCount = 201
	if got := len(srv.handlers); got != wantCount {
		t.Fatalf("handler count = %d, want %d", got, wantCount)
	}
}

func TestBuildHandlers_NoDuplicates(t *testing.T) {
	// If buildHandlers registered the same key twice, the second
	// registration would silently overwrite the first. Verify that the
	// total number of handlers equals the expected method list length.
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	if len(srv.handlers) != len(expectedMethods) {
		t.Fatalf("handler count (%d) != expected method count (%d); "+
			"possible duplicate registration", len(srv.handlers), len(expectedMethods))
	}

	// Also verify every expected method is present (catches both
	// duplicates and missing methods).
	for _, method := range expectedMethods {
		if _, ok := srv.handlers[method]; !ok {
			t.Errorf("expected method %q not found in handlers map", method)
		}
	}
}

func TestBuildHandlers_AllHandlersCallable(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	for method, handler := range srv.handlers {
		t.Run(method, func(t *testing.T) {
			if handler == nil {
				t.Fatalf("handler for method %q is nil", method)
			}
		})
	}
}

func TestBuildHandlers_NoExtraMethods(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	expectedSet := make(map[string]struct{}, len(expectedMethods))
	for _, m := range expectedMethods {
		expectedSet[m] = struct{}{}
	}

	for method := range srv.handlers {
		if _, ok := expectedSet[method]; !ok {
			t.Errorf("unexpected method %q registered in handlers map", method)
		}
	}
}

func TestBuildHandlers_CategoryCounts(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	categories := map[string]int{
		"kv":       0,
		"admin":    0,
		"mvcc":     0,
		"tx":       0,
		"batch":    0,
		"watch":    0,
		"lease":    0,
		"lock":     0,
		"index":    0,
		"ts":       0,
		"vector":   0,
		"user":     0,
		"role":     0,
		"cert":     0,
		"barrier":  0,
		"ceremony": 0,
		"tenant":   0,
		"sql":      0,
	}

	for method := range srv.handlers {
		for i := range method {
			if method[i] == '.' {
				prefix := method[:i]
				if _, ok := categories[prefix]; ok {
					categories[prefix]++
				}
				break
			}
		}
	}

	expectedCounts := map[string]int{
		"kv":       8,
		"admin":    24,
		"mvcc":     7,
		"tx":       1,
		"batch":    5,
		"lease":    5,
		"lock":     6,
		"index":    5,
		"ts":       10,
		"vector":   20,
		"user":     14,
		"role":     13,
		"cert":     5,
		"barrier":  22,
		"ceremony": 3,
		"tenant":   12,
		"sql":      1,
	}

	for cat, want := range expectedCounts {
		t.Run(cat, func(t *testing.T) {
			if got := categories[cat]; got != want {
				t.Fatalf("category %q has %d handlers, want %d", cat, got, want)
			}
		})
	}
}

func TestBuildHandlers_Idempotent(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	firstCount := len(srv.handlers)

	// Call buildHandlers again; it should replace the map entirely
	// without accumulating extra entries.
	srv.buildHandlers()

	if got := len(srv.handlers); got != firstCount {
		t.Fatalf("handler count after second buildHandlers = %d, want %d", got, firstCount)
	}
}
