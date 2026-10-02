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

	// Admin (14)
	"admin.Health",
	"admin.Status",
	"admin.Peers",
	"admin.AddMember",
	"admin.RemoveMember",
	"admin.RequestSnapshot",
	"admin.Drain",
	"admin.Capabilities",
	"admin.StartShard",
	"admin.StopShard",
	"admin.GetShardState",
	"admin.GetTopology",
	"admin.GetNodeCompaction",
	"admin.SetNodeCompaction",

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

	// Batch (2)
	"batch.Batch",
	"batch.Import",

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
	"role.ReconcileBuiltIn",

	// Certificate management (4)
	"cert.Generate",
	"cert.Export",
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

	// SQL (1)
	"sql.Query",

	// Barrier (23)
	"barrier.Initialize",
	"barrier.Unseal",
	"barrier.Seal",
	"barrier.Status",
	"barrier.ShamirInitialize",
	"barrier.ShamirUnsealShare",
	"barrier.ShamirUnsealShares",
	"barrier.Rekey",
	"barrier.EnableEncryption",
	"barrier.RecoveryGenerate",
	"barrier.RecoveryRecover",
	"barrier.RecoveryDelete",
	"barrier.RecoveryStatus",
	"barrier.RootToken",
	"barrier.OfficerRegister",
	"barrier.OfficerList",
	"barrier.ShareClaimBegin",
	"barrier.ShareClaimComplete",
	"barrier.OfficerClaimStatus",
	"barrier.RecoveryOfficerRegister",
	"barrier.RecoveryShareClaimBegin",
	"barrier.RecoveryShareClaimComplete",
	"barrier.ShamirSealVote",

	// Ceremony (9)
	"ceremony.Initialize",
	"ceremony.Status",
	"ceremony.ClaimCertBegin",
	"ceremony.ClaimCertComplete",
	"ceremony.BeginClaimShare",
	"ceremony.CompleteClaimShare",
	"ceremony.SignCSR",
	"ceremony.ClaimShareBegin",
	"ceremony.ClaimShareComplete",
	"ceremony.SubmitUnsealShare",

	// Tenant (6)
	"tenant.Create",
	"tenant.Get",
	"tenant.List",
	"tenant.Update",
	"tenant.Delete",
	"tenant.UpdateQuotas",
	"tenant.UpdateEntitlements",
	"tenant.ListDeleted",
	"tenant.ReconcileDeleted",
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

	// 172 = 170 + admin.GetNodeCompaction and admin.SetNodeCompaction, the
	// platform's node-keyspace compaction policy (docs/key-value).
	// 170 = 169 + db.UpdateCompaction, a database's scheduled-compaction
	// policy (docs/key-value).
	// 169 = 168 + db.UpdateSQL, a database's SQL concurrency mode
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
	const wantCount = 172
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
		"admin":    14,
		"mvcc":     7,
		"tx":       1,
		"batch":    2,
		"lease":    5,
		"lock":     6,
		"index":    5,
		"ts":       10,
		"vector":   20,
		"user":     12,
		"role":     11,
		"cert":     4,
		"barrier":  23,
		"ceremony": 10,
		"tenant":   9,
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
