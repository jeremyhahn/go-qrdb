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

package tenant

import "context"

// DefaultDatabase is the reserved name of the platform's implicit bootstrap
// database. It is the database the daemon auto-creates at boot and the
// fallback returned by DatabaseFromContext when no database scope is bound to
// a context, keeping single-database deployments and pre-existing clients
// working unchanged. It mirrors the REST X-QRDB-Database default and the
// gRPC/Unix x-qrdb-database default so every transport resolves the same
// (tenant, database) pair.
const DefaultDatabase = "system"

// databaseContextKey is an unexported type used as the key for the database
// scope value in context.Context. Using a dedicated type prevents collisions
// with keys from other packages. It is the shared accessor used by the QUIC
// and MCP transports so per-(tenant, database) resolution is uniform across
// protocols (the database scope is the peer of the tenant scope carried by
// WithTenantID / FromContext).
type databaseContextKey struct{}

// WithDatabase returns a new context carrying the given database scope. An
// empty database is stored as-is; DatabaseFromContext maps it back to
// DefaultDatabase on read.
func WithDatabase(ctx context.Context, database string) context.Context {
	return context.WithValue(ctx, databaseContextKey{}, database)
}

// DatabaseFromContext extracts the database scope from the context. It returns
// DefaultDatabase when no database is set or the bound value is empty, so code
// paths without an explicit database binding execute against the bootstrap
// database.
func DatabaseFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(databaseContextKey{}).(string); ok && v != "" {
		return v
	}
	return DefaultDatabase
}

// ComposeNamespace returns the composite key-namespace prefix "<tenant>/<db>/"
// for a (tenant, db) pair. A database is a key-namespace layered over the
// tenant's already-resolved store, so the physical key prefix is the
// concatenation of a tenant segment and a database segment. The prefix is
// ALWAYS non-empty and uniform — including for the bootstrap (SystemTenantID,
// DefaultDatabase) pair, which resolves to "system/system/" rather than an
// unprefixed keyspace. Special-casing any pair to an empty prefix would give
// it the entire shared store root as its keyspace, leaking every other
// database's and every other tenant's keys into a scan.
//
// An empty tenantID or db falls back to SystemTenantID / DefaultDatabase, so
// the result is always a complete "<tenant>/<db>/" with no empty or doubled
// segment.
//
// ComposeNamespace is the single canonical formula for per-(tenant, database)
// physical key isolation, shared by every transport that keys into the
// shared store: REST, gRPC, QUIC, Unix, MCP (via the composite prefix applied
// by their service.Registry), and the SQL wire protocols (MySQL/PostgreSQL)
// and ad-hoc SQL query service, which key into the store directly through
// pkg/sql/provider.
func ComposeNamespace(tenantID, db string) string {
	if tenantID == "" {
		tenantID = SystemTenantID
	}
	if db == "" {
		db = DefaultDatabase
	}
	return tenantID + "/" + db + "/"
}
