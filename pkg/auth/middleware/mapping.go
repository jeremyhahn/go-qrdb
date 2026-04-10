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
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// grpcPermissions maps gRPC full method names to the permission required
// to invoke them. Methods not present in this map are considered public
// (no authorization required).
var grpcPermissions = map[string]permission.Permission{

	// KV
	pb.QRDBService_Put_FullMethodName:    permission.KVPut,
	pb.QRDBService_Get_FullMethodName:    permission.KVGet,
	pb.QRDBService_Delete_FullMethodName: permission.KVDelete,
	pb.QRDBService_List_FullMethodName:   permission.KVList,
	pb.QRDBService_Scan_FullMethodName:   permission.KVRange,

	// MVCC
	pb.QRDBService_PutWithLease_FullMethodName:    permission.MVCCPutWithLease,
	pb.QRDBService_GetRevision_FullMethodName:     permission.MVCCGetRevision,
	pb.QRDBService_GetAtRevision_FullMethodName:   permission.MVCCGetAtRevision,
	pb.QRDBService_Range_FullMethodName:           permission.MVCCRange,
	pb.QRDBService_Compact_FullMethodName:         permission.MVCCCompact,
	pb.QRDBService_CurrentRevision_FullMethodName: permission.MVCCCurrentRevision,

	// Transaction
	pb.QRDBService_Transaction_FullMethodName: permission.TransactionExecute,

	// Batch
	pb.QRDBService_Batch_FullMethodName: permission.BatchExecute,

	// Watch
	pb.QRDBService_Watch_FullMethodName: permission.WatchWatch,

	// Lease
	pb.QRDBService_LeaseGrant_FullMethodName:      permission.LeaseGrant,
	pb.QRDBService_LeaseRevoke_FullMethodName:     permission.LeaseRevoke,
	pb.QRDBService_LeaseKeepAlive_FullMethodName:  permission.LeaseKeepAlive,
	pb.QRDBService_LeaseTimeToLive_FullMethodName: permission.LeaseTTL,
	pb.QRDBService_LeaseList_FullMethodName:       permission.LeaseList,

	// Index
	pb.QRDBService_IndexCreate_FullMethodName: permission.IndexCreate,
	pb.QRDBService_IndexDrop_FullMethodName:   permission.IndexDrop,
	pb.QRDBService_IndexQuery_FullMethodName:  permission.IndexQuery,
	pb.QRDBService_IndexScan_FullMethodName:   permission.IndexScan,

	// TimeSeries
	pb.QRDBService_TSWrite_FullMethodName:        permission.TSWrite,
	pb.QRDBService_TSWriteBatch_FullMethodName:   permission.TSWriteBatch,
	pb.QRDBService_TSQuery_FullMethodName:        permission.TSQuery,
	pb.QRDBService_TSAggregate_FullMethodName:    permission.TSAggregate,
	pb.QRDBService_TSWritePoint_FullMethodName:   permission.TSWritePoint,
	pb.QRDBService_TSRichQuery_FullMethodName:    permission.TSRichQuery,
	pb.QRDBService_TSDeleteSeries_FullMethodName: permission.TSDeleteSeries,
	pb.QRDBService_TSGetRetention_FullMethodName: permission.TSGetRetention,
	pb.QRDBService_TSSetRetention_FullMethodName: permission.TSSetRetention,
	pb.QRDBService_TSListMetrics_FullMethodName:  permission.TSListMetrics,

	// Vector
	pb.QRDBService_VectorCreateNamespace_FullMethodName:       permission.VectorNamespaceCreate,
	pb.QRDBService_VectorDeleteNamespace_FullMethodName:       permission.VectorNamespaceDelete,
	pb.QRDBService_VectorListNamespaces_FullMethodName:        permission.VectorNamespaceList,
	pb.QRDBService_VectorGetNamespaceInfo_FullMethodName:      permission.VectorNamespaceInfo,
	pb.QRDBService_VectorUpsert_FullMethodName:                permission.VectorUpsert,
	pb.QRDBService_VectorGet_FullMethodName:                   permission.VectorGet,
	pb.QRDBService_VectorDelete_FullMethodName:                permission.VectorDelete,
	pb.QRDBService_VectorBatchUpsert_FullMethodName:           permission.VectorBatchUpsert,
	pb.QRDBService_VectorBatchDelete_FullMethodName:           permission.VectorBatchDelete,
	pb.QRDBService_VectorList_FullMethodName:                  permission.VectorList,
	pb.QRDBService_VectorUpdateMetadata_FullMethodName:        permission.VectorUpdateMetadata,
	pb.QRDBService_VectorSearchSimilar_FullMethodName:         permission.VectorSearchSimilar,
	pb.QRDBService_VectorSearchByMetadata_FullMethodName:      permission.VectorSearchByMetadata,
	pb.QRDBService_VectorHybridSearch_FullMethodName:          permission.VectorHybridSearch,
	pb.QRDBService_VectorSearchByID_FullMethodName:            permission.VectorSearchByID,
	pb.QRDBService_VectorRebuildIndex_FullMethodName:          permission.VectorRebuildIndex,
	pb.QRDBService_VectorGetRebuildProgress_FullMethodName:    permission.VectorGetRebuildProg,
	pb.QRDBService_VectorGetIndexStats_FullMethodName:         permission.VectorGetIndexStats,
	pb.QRDBService_VectorCompactNamespace_FullMethodName:      permission.VectorCompact,
	pb.QRDBService_VectorGetCompactionProgress_FullMethodName: permission.VectorGetCompactProg,

	// Admin
	pb.QRDBService_Health_FullMethodName:          permission.AdminHealth,
	pb.QRDBService_Status_FullMethodName:          permission.AdminStatus,
	pb.QRDBService_Peers_FullMethodName:           permission.AdminPeers,
	pb.QRDBService_AddMember_FullMethodName:       permission.AdminAddMember,
	pb.QRDBService_RemoveMember_FullMethodName:    permission.AdminRemoveMember,
	pb.QRDBService_RequestSnapshot_FullMethodName: permission.AdminSnapshot,
	pb.QRDBService_Drain_FullMethodName:           permission.AdminDrain,
	pb.QRDBService_Capabilities_FullMethodName:    permission.AdminCapabilities,

	// User management
	pb.QRDBService_CreateUser_FullMethodName:  permission.UsersCreate,
	pb.QRDBService_GetUser_FullMethodName:     permission.UsersRead,
	pb.QRDBService_UpdateUser_FullMethodName:  permission.UsersUpdate,
	pb.QRDBService_DeleteUser_FullMethodName:  permission.UsersDelete,
	pb.QRDBService_ListUsers_FullMethodName:   permission.UsersList,
	pb.QRDBService_EnableUser_FullMethodName:  permission.UsersEnable,
	pb.QRDBService_DisableUser_FullMethodName: permission.UsersDisable,

	// Role management
	pb.QRDBService_CreateRole_FullMethodName:      permission.RolesCreate,
	pb.QRDBService_GetRole_FullMethodName:         permission.RolesRead,
	pb.QRDBService_UpdateRole_FullMethodName:      permission.RolesUpdate,
	pb.QRDBService_DeleteRole_FullMethodName:      permission.RolesDelete,
	pb.QRDBService_ListRoles_FullMethodName:       permission.RolesList,
	pb.QRDBService_AssignRole_FullMethodName:      permission.RolesAssign,
	pb.QRDBService_UnassignRole_FullMethodName:    permission.RolesUnassign,
	pb.QRDBService_ListPermissions_FullMethodName: permission.PermissionsList,

	// Certificate management
	pb.QRDBService_GenerateCert_FullMethodName: permission.CertsGenerate,
	pb.QRDBService_ExportCert_FullMethodName:   permission.CertsExport,
	pb.QRDBService_RevokeCert_FullMethodName:   permission.CertsRevoke,
	pb.QRDBService_ListCerts_FullMethodName:    permission.CertsList,
}

// restPermissions maps "METHOD /path" to the permission required for
// that REST endpoint. Routes not present in this map are considered
// public (no authorization required).
var restPermissions = map[string]permission.Permission{

	// KV -- the /kv path is method-dispatched in the REST server.
	"PUT /kv":       permission.KVPut,
	"GET /kv":       permission.KVGet,
	"DELETE /kv":    permission.KVDelete,
	"GET /kv/scan":  permission.KVList,
	"GET /kv/range": permission.KVRange,

	// MVCC
	"PUT /kv/lease":    permission.MVCCPutWithLease,
	"GET /kv/revision": permission.MVCCCurrentRevision,
	"POST /kv/compact": permission.MVCCCompact,

	// Transaction
	"POST /tx": permission.TransactionExecute,

	// Batch
	"POST /batch": permission.BatchExecute,

	// Watch
	"GET /watch": permission.WatchWatch,

	// Lease
	"POST /lease/grant":     permission.LeaseGrant,
	"POST /lease/revoke":    permission.LeaseRevoke,
	"POST /lease/keepalive": permission.LeaseKeepAlive,
	"GET /lease/ttl":        permission.LeaseTTL,
	"GET /lease/list":       permission.LeaseList,

	// Index
	"POST /index/create": permission.IndexCreate,
	"POST /index/drop":   permission.IndexDrop,
	"GET /index/query":   permission.IndexQuery,
	"GET /index/scan":    permission.IndexScan,

	// TimeSeries
	"POST /ts/write":       permission.TSWrite,
	"POST /ts/write-batch": permission.TSWriteBatch,
	"POST /ts/write-point": permission.TSWritePoint,
	"GET /ts/query":        permission.TSQuery,
	"POST /ts/query-rich":  permission.TSRichQuery,
	"GET /ts/aggregate":    permission.TSAggregate,

	// Admin
	// /health is public (exempted in RESTMiddleware before authentication).
	"GET /status":             permission.AdminStatus,
	"GET /peers":              permission.AdminPeers,
	"POST /membership/add":    permission.AdminAddMember,
	"POST /membership/remove": permission.AdminRemoveMember,
	"POST /snapshot":          permission.AdminSnapshot,
	"POST /drain":             permission.AdminDrain,
	"GET /capabilities":       permission.AdminCapabilities,

	// Vector
	"POST /vector/namespace":             permission.VectorNamespaceCreate,
	"POST /vector/namespace/delete":      permission.VectorNamespaceDelete,
	"GET /vector/namespaces":             permission.VectorNamespaceList,
	"GET /vector/namespace/info":         permission.VectorNamespaceInfo,
	"POST /vector/upsert":                permission.VectorUpsert,
	"GET /vector/get":                    permission.VectorGet,
	"POST /vector/delete":                permission.VectorDelete,
	"POST /vector/batch/upsert":          permission.VectorBatchUpsert,
	"POST /vector/batch/delete":          permission.VectorBatchDelete,
	"GET /vector/list":                   permission.VectorList,
	"POST /vector/metadata":              permission.VectorUpdateMetadata,
	"POST /vector/search/similar":        permission.VectorSearchSimilar,
	"POST /vector/search/metadata":       permission.VectorSearchByMetadata,
	"POST /vector/search/hybrid":         permission.VectorHybridSearch,
	"POST /vector/search/by-id":          permission.VectorSearchByID,
	"POST /vector/index/rebuild":         permission.VectorRebuildIndex,
	"GET /vector/index/rebuild/progress": permission.VectorGetRebuildProg,
	"GET /vector/index/stats":            permission.VectorGetIndexStats,
	"POST /vector/compact":               permission.VectorCompact,
	"GET /vector/compact/progress":       permission.VectorGetCompactProg,

	// User management
	"POST /users":     permission.UsersCreate,
	"GET /users/list": permission.UsersList,

	// Role management
	"POST /roles":          permission.RolesCreate,
	"GET /roles/list":      permission.RolesList,
	"POST /roles/assign":   permission.RolesAssign,
	"POST /roles/unassign": permission.RolesUnassign,
	"GET /permissions":     permission.PermissionsList,

	// Certificate management
	"POST /certs/generate": permission.CertsGenerate,
	"POST /certs/export":   permission.CertsExport,
	"POST /certs/revoke":   permission.CertsRevoke,
	"GET /certs/list":      permission.CertsList,
}

// quicPermissions maps native QUIC method names to the permission required
// to invoke them. Methods not present in this map are considered public
// (no authorization required).
var quicPermissions = map[string]permission.Permission{

	// KV
	"kv.Put":    permission.KVPut,
	"kv.Get":    permission.KVGet,
	"kv.Delete": permission.KVDelete,
	"kv.List":   permission.KVList,
	"kv.Scan":   permission.KVRange,

	// MVCC
	"mvcc.PutWithLease":    permission.MVCCPutWithLease,
	"mvcc.GetRevision":     permission.MVCCGetRevision,
	"mvcc.GetAtRevision":   permission.MVCCGetAtRevision,
	"mvcc.Range":           permission.MVCCRange,
	"mvcc.Compact":         permission.MVCCCompact,
	"mvcc.CurrentRevision": permission.MVCCCurrentRevision,

	// Transaction
	"tx.Transaction": permission.TransactionExecute,

	// Batch
	"batch.Batch": permission.BatchExecute,

	// Watch
	"watch.Watch":       permission.WatchWatch,
	"watch.WatchPrefix": permission.WatchWatch,

	// Lease
	"lease.Grant":      permission.LeaseGrant,
	"lease.Revoke":     permission.LeaseRevoke,
	"lease.KeepAlive":  permission.LeaseKeepAlive,
	"lease.TimeToLive": permission.LeaseTTL,
	"lease.List":       permission.LeaseList,

	// Index
	"index.Create": permission.IndexCreate,
	"index.Drop":   permission.IndexDrop,
	"index.Query":  permission.IndexQuery,
	"index.Scan":   permission.IndexScan,

	// TimeSeries
	"ts.Write":        permission.TSWrite,
	"ts.WriteBatch":   permission.TSWriteBatch,
	"ts.Query":        permission.TSQuery,
	"ts.Aggregate":    permission.TSAggregate,
	"ts.WritePoint":   permission.TSWritePoint,
	"ts.RichQuery":    permission.TSRichQuery,
	"ts.DeleteSeries": permission.TSDeleteSeries,
	"ts.GetRetention": permission.TSGetRetention,
	"ts.SetRetention": permission.TSSetRetention,
	"ts.ListMetrics":  permission.TSListMetrics,

	// Vector
	"vector.CreateNamespace":       permission.VectorNamespaceCreate,
	"vector.DeleteNamespace":       permission.VectorNamespaceDelete,
	"vector.ListNamespaces":        permission.VectorNamespaceList,
	"vector.GetNamespaceInfo":      permission.VectorNamespaceInfo,
	"vector.Upsert":                permission.VectorUpsert,
	"vector.Get":                   permission.VectorGet,
	"vector.Delete":                permission.VectorDelete,
	"vector.BatchUpsert":           permission.VectorBatchUpsert,
	"vector.BatchDelete":           permission.VectorBatchDelete,
	"vector.List":                  permission.VectorList,
	"vector.UpdateMetadata":        permission.VectorUpdateMetadata,
	"vector.SearchSimilar":         permission.VectorSearchSimilar,
	"vector.SearchByMetadata":      permission.VectorSearchByMetadata,
	"vector.HybridSearch":          permission.VectorHybridSearch,
	"vector.SearchByID":            permission.VectorSearchByID,
	"vector.RebuildIndex":          permission.VectorRebuildIndex,
	"vector.GetRebuildProgress":    permission.VectorGetRebuildProg,
	"vector.GetIndexStats":         permission.VectorGetIndexStats,
	"vector.CompactNamespace":      permission.VectorCompact,
	"vector.GetCompactionProgress": permission.VectorGetCompactProg,

	// Admin
	"admin.Health":          permission.AdminHealth,
	"admin.Status":          permission.AdminStatus,
	"admin.Peers":           permission.AdminPeers,
	"admin.AddMember":       permission.AdminAddMember,
	"admin.RemoveMember":    permission.AdminRemoveMember,
	"admin.RequestSnapshot": permission.AdminSnapshot,
	"admin.Drain":           permission.AdminDrain,
	"admin.Capabilities":    permission.AdminCapabilities,

	// User management
	"user.Create":  permission.UsersCreate,
	"user.Get":     permission.UsersRead,
	"user.Update":  permission.UsersUpdate,
	"user.Delete":  permission.UsersDelete,
	"user.List":    permission.UsersList,
	"user.Enable":  permission.UsersEnable,
	"user.Disable": permission.UsersDisable,

	// Role management
	"role.Create":          permission.RolesCreate,
	"role.Get":             permission.RolesRead,
	"role.Update":          permission.RolesUpdate,
	"role.Delete":          permission.RolesDelete,
	"role.List":            permission.RolesList,
	"role.Assign":          permission.RolesAssign,
	"role.Unassign":        permission.RolesUnassign,
	"role.ListPermissions": permission.PermissionsList,

	// Certificate management
	"cert.Generate": permission.CertsGenerate,
	"cert.Export":   permission.CertsExport,
	"cert.Revoke":   permission.CertsRevoke,
	"cert.List":     permission.CertsList,
}

// LookupGRPC returns the permission required for a gRPC full method name.
// Returns an empty Permission and false if the method has no mapping,
// indicating a public endpoint that does not require authorization.
func LookupGRPC(fullMethod string) (permission.Permission, bool) {
	p, ok := grpcPermissions[fullMethod]
	return p, ok
}

// LookupREST returns the permission required for a REST route identified
// by its HTTP method and path. The lookup key is "METHOD /path". Returns
// an empty Permission and false if the route has no mapping, indicating a
// public endpoint that does not require authorization.
func LookupREST(method, path string) (permission.Permission, bool) {
	p, ok := restPermissions[method+" "+path]
	return p, ok
}

// sqlPermissions maps SQL statement patterns to the permission required.
// The key format is "VERB table" (e.g., "SELECT kv", "INSERT kv").
// Statements not present in this map are considered public (no
// authorization required), covering SET, SHOW, USE, BEGIN, COMMIT.
var sqlPermissions = map[string]permission.Permission{

	// KV data operations
	"SELECT kv": permission.KVRange,
	"INSERT kv": permission.KVPut,
	"UPDATE kv": permission.KVPut,
	"DELETE kv": permission.KVDelete,

	// Admin read-only tables
	"SELECT status": permission.AdminStatus,
	"SELECT peers":  permission.AdminPeers,
}

// LookupSQL returns the permission required for a SQL operation identified
// by its verb and table name. The lookup key is "VERB table". Returns an
// empty Permission and false if the operation has no mapping, indicating a
// public operation that does not require authorization.
func LookupSQL(key string) (permission.Permission, bool) {
	p, ok := sqlPermissions[key]
	return p, ok
}
