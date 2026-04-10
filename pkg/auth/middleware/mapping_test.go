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

package middleware

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// ---------------------------------------------------------------------------
// LookupGRPC tests
// ---------------------------------------------------------------------------

func TestLookupGRPC_Found(t *testing.T) {
	tests := []struct {
		method string
		want   permission.Permission
	}{
		{"/qrdb.v1.QRDBService/Put", permission.KVPut},
		{"/qrdb.v1.QRDBService/Get", permission.KVGet},
		{"/qrdb.v1.QRDBService/Delete", permission.KVDelete},
		{"/qrdb.v1.QRDBService/List", permission.KVList},
		{"/qrdb.v1.QRDBService/Scan", permission.KVRange},
		{"/qrdb.v1.QRDBService/PutWithLease", permission.MVCCPutWithLease},
		{"/qrdb.v1.QRDBService/GetRevision", permission.MVCCGetRevision},
		{"/qrdb.v1.QRDBService/GetAtRevision", permission.MVCCGetAtRevision},
		{"/qrdb.v1.QRDBService/Range", permission.MVCCRange},
		{"/qrdb.v1.QRDBService/Compact", permission.MVCCCompact},
		{"/qrdb.v1.QRDBService/CurrentRevision", permission.MVCCCurrentRevision},
		{"/qrdb.v1.QRDBService/Transaction", permission.TransactionExecute},
		{"/qrdb.v1.QRDBService/Batch", permission.BatchExecute},
		{"/qrdb.v1.QRDBService/Watch", permission.WatchWatch},
		{"/qrdb.v1.QRDBService/LeaseGrant", permission.LeaseGrant},
		{"/qrdb.v1.QRDBService/LeaseRevoke", permission.LeaseRevoke},
		{"/qrdb.v1.QRDBService/LeaseKeepAlive", permission.LeaseKeepAlive},
		{"/qrdb.v1.QRDBService/LeaseTimeToLive", permission.LeaseTTL},
		{"/qrdb.v1.QRDBService/LeaseList", permission.LeaseList},
		{"/qrdb.v1.QRDBService/IndexCreate", permission.IndexCreate},
		{"/qrdb.v1.QRDBService/IndexDrop", permission.IndexDrop},
		{"/qrdb.v1.QRDBService/IndexQuery", permission.IndexQuery},
		{"/qrdb.v1.QRDBService/IndexScan", permission.IndexScan},
		{"/qrdb.v1.QRDBService/TSWrite", permission.TSWrite},
		{"/qrdb.v1.QRDBService/TSWriteBatch", permission.TSWriteBatch},
		{"/qrdb.v1.QRDBService/TSQuery", permission.TSQuery},
		{"/qrdb.v1.QRDBService/TSAggregate", permission.TSAggregate},
		{"/qrdb.v1.QRDBService/VectorCreateNamespace", permission.VectorNamespaceCreate},
		{"/qrdb.v1.QRDBService/VectorDeleteNamespace", permission.VectorNamespaceDelete},
		{"/qrdb.v1.QRDBService/VectorListNamespaces", permission.VectorNamespaceList},
		{"/qrdb.v1.QRDBService/VectorGetNamespaceInfo", permission.VectorNamespaceInfo},
		{"/qrdb.v1.QRDBService/VectorUpsert", permission.VectorUpsert},
		{"/qrdb.v1.QRDBService/VectorGet", permission.VectorGet},
		{"/qrdb.v1.QRDBService/VectorDelete", permission.VectorDelete},
		{"/qrdb.v1.QRDBService/VectorBatchUpsert", permission.VectorBatchUpsert},
		{"/qrdb.v1.QRDBService/VectorBatchDelete", permission.VectorBatchDelete},
		{"/qrdb.v1.QRDBService/VectorList", permission.VectorList},
		{"/qrdb.v1.QRDBService/VectorUpdateMetadata", permission.VectorUpdateMetadata},
		{"/qrdb.v1.QRDBService/VectorSearchSimilar", permission.VectorSearchSimilar},
		{"/qrdb.v1.QRDBService/VectorSearchByMetadata", permission.VectorSearchByMetadata},
		{"/qrdb.v1.QRDBService/VectorHybridSearch", permission.VectorHybridSearch},
		{"/qrdb.v1.QRDBService/VectorSearchByID", permission.VectorSearchByID},
		{"/qrdb.v1.QRDBService/VectorRebuildIndex", permission.VectorRebuildIndex},
		{"/qrdb.v1.QRDBService/VectorGetRebuildProgress", permission.VectorGetRebuildProg},
		{"/qrdb.v1.QRDBService/VectorGetIndexStats", permission.VectorGetIndexStats},
		{"/qrdb.v1.QRDBService/VectorCompactNamespace", permission.VectorCompact},
		{"/qrdb.v1.QRDBService/VectorGetCompactionProgress", permission.VectorGetCompactProg},
		{"/qrdb.v1.QRDBService/Health", permission.AdminHealth},
		{"/qrdb.v1.QRDBService/Status", permission.AdminStatus},
		{"/qrdb.v1.QRDBService/Peers", permission.AdminPeers},
		{"/qrdb.v1.QRDBService/AddMember", permission.AdminAddMember},
		{"/qrdb.v1.QRDBService/RemoveMember", permission.AdminRemoveMember},
		{"/qrdb.v1.QRDBService/RequestSnapshot", permission.AdminSnapshot},
		{"/qrdb.v1.QRDBService/Drain", permission.AdminDrain},
		{"/qrdb.v1.QRDBService/Capabilities", permission.AdminCapabilities},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			got, ok := LookupGRPC(tt.method)
			if !ok {
				t.Fatalf("expected mapping for %q", tt.method)
			}
			if got != tt.want {
				t.Errorf("LookupGRPC(%q) = %q, want %q", tt.method, got, tt.want)
			}
		})
	}
}

func TestLookupGRPC_NotFound(t *testing.T) {
	methods := []string{
		"/qrdb.v1.QRDBService/UnknownMethod",
		"/other.Service/Put",
		"",
		"/",
	}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			_, ok := LookupGRPC(method)
			if ok {
				t.Errorf("expected no mapping for %q", method)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// LookupREST tests
// ---------------------------------------------------------------------------

func TestLookupREST_Found(t *testing.T) {
	tests := []struct {
		method string
		path   string
		want   permission.Permission
	}{
		{"PUT", "/kv", permission.KVPut},
		{"GET", "/kv", permission.KVGet},
		{"DELETE", "/kv", permission.KVDelete},
		{"GET", "/kv/scan", permission.KVList},
		{"GET", "/kv/range", permission.KVRange},
		{"PUT", "/kv/lease", permission.MVCCPutWithLease},
		{"GET", "/kv/revision", permission.MVCCCurrentRevision},
		{"POST", "/kv/compact", permission.MVCCCompact},
		{"POST", "/tx", permission.TransactionExecute},
		{"POST", "/batch", permission.BatchExecute},
		{"GET", "/watch", permission.WatchWatch},
		{"POST", "/lease/grant", permission.LeaseGrant},
		{"POST", "/lease/revoke", permission.LeaseRevoke},
		{"POST", "/lease/keepalive", permission.LeaseKeepAlive},
		{"GET", "/lease/ttl", permission.LeaseTTL},
		{"GET", "/lease/list", permission.LeaseList},
		{"POST", "/index/create", permission.IndexCreate},
		{"POST", "/index/drop", permission.IndexDrop},
		{"GET", "/index/query", permission.IndexQuery},
		{"GET", "/index/scan", permission.IndexScan},
		{"POST", "/ts/write", permission.TSWrite},
		{"POST", "/ts/write-batch", permission.TSWriteBatch},
		{"POST", "/ts/write-point", permission.TSWritePoint},
		{"GET", "/ts/query", permission.TSQuery},
		{"POST", "/ts/query-rich", permission.TSRichQuery},
		{"GET", "/ts/aggregate", permission.TSAggregate},
		// /health is public (exempt from auth in RESTMiddleware).
		{"GET", "/status", permission.AdminStatus},
		{"GET", "/peers", permission.AdminPeers},
		{"POST", "/membership/add", permission.AdminAddMember},
		{"POST", "/membership/remove", permission.AdminRemoveMember},
		{"POST", "/snapshot", permission.AdminSnapshot},
		{"POST", "/drain", permission.AdminDrain},
		{"GET", "/capabilities", permission.AdminCapabilities},
		{"POST", "/vector/namespace", permission.VectorNamespaceCreate},
		{"POST", "/vector/namespace/delete", permission.VectorNamespaceDelete},
		{"GET", "/vector/namespaces", permission.VectorNamespaceList},
		{"GET", "/vector/namespace/info", permission.VectorNamespaceInfo},
		{"POST", "/vector/upsert", permission.VectorUpsert},
		{"GET", "/vector/get", permission.VectorGet},
		{"POST", "/vector/delete", permission.VectorDelete},
		{"POST", "/vector/batch/upsert", permission.VectorBatchUpsert},
		{"POST", "/vector/batch/delete", permission.VectorBatchDelete},
		{"GET", "/vector/list", permission.VectorList},
		{"POST", "/vector/metadata", permission.VectorUpdateMetadata},
		{"POST", "/vector/search/similar", permission.VectorSearchSimilar},
		{"POST", "/vector/search/metadata", permission.VectorSearchByMetadata},
		{"POST", "/vector/search/hybrid", permission.VectorHybridSearch},
		{"POST", "/vector/search/by-id", permission.VectorSearchByID},
		{"POST", "/vector/index/rebuild", permission.VectorRebuildIndex},
		{"GET", "/vector/index/rebuild/progress", permission.VectorGetRebuildProg},
		{"GET", "/vector/index/stats", permission.VectorGetIndexStats},
		{"POST", "/vector/compact", permission.VectorCompact},
		{"GET", "/vector/compact/progress", permission.VectorGetCompactProg},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			got, ok := LookupREST(tt.method, tt.path)
			if !ok {
				t.Fatalf("expected mapping for %s %s", tt.method, tt.path)
			}
			if got != tt.want {
				t.Errorf("LookupREST(%q, %q) = %q, want %q", tt.method, tt.path, got, tt.want)
			}
		})
	}
}

func TestLookupREST_NotFound(t *testing.T) {
	tests := []struct {
		method string
		path   string
	}{
		{"GET", "/unknown"},
		{"POST", "/kv"},
		{"DELETE", "/health"},
		{"GET", "/admin"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			_, ok := LookupREST(tt.method, tt.path)
			if ok {
				t.Errorf("expected no mapping for %s %s", tt.method, tt.path)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// LookupQUIC tests
// ---------------------------------------------------------------------------

func TestLookupQUIC_Found(t *testing.T) {
	tests := []struct {
		method string
		want   permission.Permission
	}{
		{"kv.Put", permission.KVPut},
		{"kv.Get", permission.KVGet},
		{"kv.Delete", permission.KVDelete},
		{"kv.List", permission.KVList},
		{"kv.Scan", permission.KVRange},
		{"mvcc.PutWithLease", permission.MVCCPutWithLease},
		{"mvcc.GetRevision", permission.MVCCGetRevision},
		{"mvcc.GetAtRevision", permission.MVCCGetAtRevision},
		{"mvcc.Range", permission.MVCCRange},
		{"mvcc.Compact", permission.MVCCCompact},
		{"mvcc.CurrentRevision", permission.MVCCCurrentRevision},
		{"tx.Transaction", permission.TransactionExecute},
		{"batch.Batch", permission.BatchExecute},
		{"watch.Watch", permission.WatchWatch},
		{"watch.WatchPrefix", permission.WatchWatch},
		{"lease.Grant", permission.LeaseGrant},
		{"lease.Revoke", permission.LeaseRevoke},
		{"lease.KeepAlive", permission.LeaseKeepAlive},
		{"lease.TimeToLive", permission.LeaseTTL},
		{"lease.List", permission.LeaseList},
		{"index.Create", permission.IndexCreate},
		{"index.Drop", permission.IndexDrop},
		{"index.Query", permission.IndexQuery},
		{"index.Scan", permission.IndexScan},
		{"ts.Write", permission.TSWrite},
		{"ts.WriteBatch", permission.TSWriteBatch},
		{"ts.Query", permission.TSQuery},
		{"ts.Aggregate", permission.TSAggregate},
		{"vector.CreateNamespace", permission.VectorNamespaceCreate},
		{"vector.DeleteNamespace", permission.VectorNamespaceDelete},
		{"vector.ListNamespaces", permission.VectorNamespaceList},
		{"vector.GetNamespaceInfo", permission.VectorNamespaceInfo},
		{"vector.Upsert", permission.VectorUpsert},
		{"vector.Get", permission.VectorGet},
		{"vector.Delete", permission.VectorDelete},
		{"vector.BatchUpsert", permission.VectorBatchUpsert},
		{"vector.BatchDelete", permission.VectorBatchDelete},
		{"vector.List", permission.VectorList},
		{"vector.UpdateMetadata", permission.VectorUpdateMetadata},
		{"vector.SearchSimilar", permission.VectorSearchSimilar},
		{"vector.SearchByMetadata", permission.VectorSearchByMetadata},
		{"vector.HybridSearch", permission.VectorHybridSearch},
		{"vector.SearchByID", permission.VectorSearchByID},
		{"vector.RebuildIndex", permission.VectorRebuildIndex},
		{"vector.GetRebuildProgress", permission.VectorGetRebuildProg},
		{"vector.GetIndexStats", permission.VectorGetIndexStats},
		{"vector.CompactNamespace", permission.VectorCompact},
		{"vector.GetCompactionProgress", permission.VectorGetCompactProg},
		{"admin.Health", permission.AdminHealth},
		{"admin.Status", permission.AdminStatus},
		{"admin.Peers", permission.AdminPeers},
		{"admin.AddMember", permission.AdminAddMember},
		{"admin.RemoveMember", permission.AdminRemoveMember},
		{"admin.RequestSnapshot", permission.AdminSnapshot},
		{"admin.Drain", permission.AdminDrain},
		{"admin.Capabilities", permission.AdminCapabilities},
		{"user.Create", permission.UsersCreate},
		{"user.Get", permission.UsersRead},
		{"user.Update", permission.UsersUpdate},
		{"user.Delete", permission.UsersDelete},
		{"user.List", permission.UsersList},
		{"user.Enable", permission.UsersEnable},
		{"user.Disable", permission.UsersDisable},
		{"role.Create", permission.RolesCreate},
		{"role.Get", permission.RolesRead},
		{"role.Update", permission.RolesUpdate},
		{"role.Delete", permission.RolesDelete},
		{"role.List", permission.RolesList},
		{"role.Assign", permission.RolesAssign},
		{"role.Unassign", permission.RolesUnassign},
		{"cert.Generate", permission.CertsGenerate},
		{"cert.Revoke", permission.CertsRevoke},
		{"cert.List", permission.CertsList},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			got, ok := LookupQUIC(tt.method)
			if !ok {
				t.Fatalf("expected mapping for %q", tt.method)
			}
			if got != tt.want {
				t.Errorf("LookupQUIC(%q) = %q, want %q", tt.method, got, tt.want)
			}
		})
	}
}

func TestLookupQUIC_NotFound(t *testing.T) {
	methods := []string{
		"unknown.Method",
		"kv.Unknown",
		"",
		".",
	}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			_, ok := LookupQUIC(method)
			if ok {
				t.Errorf("expected no mapping for %q", method)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Map integrity tests
// ---------------------------------------------------------------------------

func TestGRPCPermissions_NoDuplicateValues(t *testing.T) {
	seen := make(map[permission.Permission]string, len(grpcPermissions))
	for method, perm := range grpcPermissions {
		if prev, ok := seen[perm]; ok {
			t.Errorf("duplicate gRPC permission %q: %q and %q", perm, prev, method)
		}
		seen[perm] = method
	}
}

func TestRESTPermissions_NoDuplicateValues(t *testing.T) {
	seen := make(map[permission.Permission]string, len(restPermissions))
	for route, perm := range restPermissions {
		if prev, ok := seen[perm]; ok {
			t.Errorf("duplicate REST permission %q: %q and %q", perm, prev, route)
		}
		seen[perm] = route
	}
}

func TestMappingConsistency_RESTPermissionsHaveGRPCCounterparts(t *testing.T) {
	// Build a set of all gRPC permissions.
	grpcPerms := make(map[permission.Permission]struct{}, len(grpcPermissions))
	for _, perm := range grpcPermissions {
		grpcPerms[perm] = struct{}{}
	}

	// Every REST permission should have a corresponding gRPC permission.
	for route, perm := range restPermissions {
		if _, ok := grpcPerms[perm]; !ok {
			t.Errorf("REST route %q maps to %q which has no gRPC counterpart", route, perm)
		}
	}
}

func TestGRPCPermissions_AllKeysUseCorrectPrefix(t *testing.T) {
	prefix := "/qrdb.v1.QRDBService/"
	for method := range grpcPermissions {
		if len(method) <= len(prefix) {
			t.Errorf("gRPC method key too short: %q", method)
			continue
		}
		if method[:len(prefix)] != prefix {
			t.Errorf("gRPC method key %q does not start with %q", method, prefix)
		}
	}
}

func TestGRPCPermissions_AllPermissionsRegistered(t *testing.T) {
	for method, perm := range grpcPermissions {
		if !permission.IsRegistered(perm) {
			t.Errorf("gRPC %q maps to unregistered permission %q", method, perm)
		}
	}
}

func TestRESTPermissions_AllPermissionsRegistered(t *testing.T) {
	for route, perm := range restPermissions {
		if !permission.IsRegistered(perm) {
			t.Errorf("REST %q maps to unregistered permission %q", route, perm)
		}
	}
}

func TestQUICPermissions_AllPermissionsRegistered(t *testing.T) {
	for method, perm := range quicPermissions {
		if !permission.IsRegistered(perm) {
			t.Errorf("QUIC %q maps to unregistered permission %q", method, perm)
		}
	}
}

func TestQUICPermissions_AllKeysUseDotNotation(t *testing.T) {
	for method := range quicPermissions {
		found := false
		for i := range method {
			if method[i] == '.' {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("QUIC method key %q does not use dot notation", method)
		}
	}
}

func TestMappingConsistency_QUICPermissionsHaveGRPCCounterparts(t *testing.T) {
	grpcPerms := make(map[permission.Permission]struct{}, len(grpcPermissions))
	for _, perm := range grpcPermissions {
		grpcPerms[perm] = struct{}{}
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
		permission.CertsGenerate: {},
		permission.CertsRevoke:   {},
		permission.CertsList:     {},
		// Rich TS operations use JSON encoding (no proto messages).
		permission.TSWritePoint:   {},
		permission.TSRichQuery:    {},
		permission.TSDeleteSeries: {},
		permission.TSGetRetention: {},
		permission.TSSetRetention: {},
		permission.TSListMetrics:  {},
	}

	for method, perm := range quicPermissions {
		if _, ok := grpcPerms[perm]; !ok {
			if _, skip := quicOnly[perm]; skip {
				continue
			}
			t.Errorf("QUIC method %q maps to %q which has no gRPC counterpart", method, perm)
		}
	}
}
