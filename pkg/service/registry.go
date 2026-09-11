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

package service

// Registry resolves per-(tenant, database) service instances for data-plane
// request routing. Each method returns the service scoped to the given
// (tenant, db) pair, or a typed error when the tenant or database does not
// exist. It is the transport-neutral seam shared by every API protocol
// (REST, gRPC, Unix, QUIC, MCP): each transport reads the (tenant, db) pair
// from the request and resolves the appropriate scoped service through this
// interface, giving every protocol the same per-database keyspace isolation.
//
// Implementations must be safe for concurrent use.
type Registry interface {
	// KVService returns the KVServicer for the given (tenant, db) scope.
	KVService(tenant, db string) (KVServicer, error)

	// MVCCService returns the MVCCKVServicer for the given scope.
	MVCCService(tenant, db string) (MVCCKVServicer, error)

	// WatchService returns the WatchServicer for the given scope.
	WatchService(tenant, db string) (WatchServicer, error)

	// LeaseService returns the LeaseServicer for the given scope.
	LeaseService(tenant, db string) (LeaseServicer, error)

	// IndexService returns the IndexServicer for the given scope.
	IndexService(tenant, db string) (IndexServicer, error)

	// TimeSeriesService returns the TimeSeriesServicer for the given scope.
	TimeSeriesService(tenant, db string) (TimeSeriesServicer, error)

	// VectorService returns the VectorServicer for the given scope.
	VectorService(tenant, db string) (VectorServicer, error)

	// TransactionService returns the TransactionServicer for the given scope.
	TransactionService(tenant, db string) (TransactionServicer, error)

	// BatchService returns the BatchServicer for the given scope.
	BatchService(tenant, db string) (BatchServicer, error)

	// TableService returns the TableServicer for the given (tenant, db)
	// scope -- the relational table catalog (CREATE TABLE and friends). Like
	// every other resolver on this interface, the returned TableServicer's
	// underlying KV/MVCC access is scoped to the caller's (tenant, db)
	// namespace, so a table created for one tenant is invisible to another.
	TableService(tenant, db string) (TableServicer, error)

	// SequenceService returns the SequenceServicer for the given (tenant,
	// db) scope. It is exposed independently of TableService because
	// AUTO_INCREMENT columns allocate from the same per-database sequence
	// space directly, without going through the table catalog.
	SequenceService(tenant, db string) (SequenceServicer, error)

	// PrivilegeService returns the PrivilegeServicer for the given
	// (tenant, db) scope -- the SQL table-privilege catalog GRANT, REVOKE,
	// and SHOW GRANTS operate on, and the SQL authorization handler
	// enforces from. Like the table catalog, its keyspace is scoped to the
	// caller's (tenant, db) namespace, so one tenant's grants are
	// invisible to another.
	PrivilegeService(tenant, db string) (PrivilegeServicer, error)

	// CollectionService returns the CollectionServicer for the given
	// (tenant, db) scope -- the document-collection surface. A collection
	// document key is "__col:{database}:{collection}/{id}" and the database
	// name travels in the request, so the returned servicer MUST key into
	// the caller's own namespace: two tenants naming the same database and
	// the same collection otherwise address the same documents
	// (go-qrdb#441). Every transport resolves collections through here
	// rather than holding one process-wide servicer.
	CollectionService(tenant, db string) (CollectionServicer, error)
}

// TenantNotFoundError is returned when the tenant ID from the authenticated
// identity does not match any provisioned tenant. Transports map it to their
// not-found status (HTTP 404 / gRPC NotFound).
type TenantNotFoundError struct {
	TenantID string
}

// Error returns a human-readable description.
func (e *TenantNotFoundError) Error() string {
	return "service: tenant not found: " + e.TenantID
}

// DatabaseNotFoundError is returned when the requested database name does not
// exist within the resolved tenant. Transports map it to their not-found
// status (HTTP 404 / gRPC NotFound).
type DatabaseNotFoundError struct {
	TenantID string
	Database string
}

// Error returns a human-readable description.
func (e *DatabaseNotFoundError) Error() string {
	return "service: database not found: " + e.Database + " (tenant: " + e.TenantID + ")"
}

// BarrierSealedError is returned when the tenant's active barrier is sealed,
// preventing data-plane access. Transports map it to their unavailable status
// (HTTP 503 / gRPC Unavailable).
type BarrierSealedError struct {
	TenantID string
}

// Error returns a human-readable description.
func (e *BarrierSealedError) Error() string {
	return "service: barrier sealed for tenant: " + e.TenantID
}

// IsolationBackendUnavailableError is returned when a tenant's configured
// isolation mode requires a dedicated storage backend (a per-tenant volume Host
// or a per-database Raft shard) that is not wired into this server instance.
// The data plane MUST fail closed rather than silently fall back to the shared
// system store, which would both downgrade the tenant's isolation guarantee and
// strand the tenant's data on the wrong backend. Transports map it to their
// unavailable status (HTTP 503 / gRPC Unavailable).
type IsolationBackendUnavailableError struct {
	TenantID  string
	Isolation string
}

// Error returns a human-readable description.
func (e *IsolationBackendUnavailableError) Error() string {
	return "service: isolation backend unavailable for tenant " + e.TenantID + " (isolation: " + e.Isolation + ")"
}
