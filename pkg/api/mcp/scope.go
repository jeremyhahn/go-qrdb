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

package mcp

import (
	"context"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// databaseToolArg is the optional tool argument that selects the target
// database within the authenticated tenant's keyspace. It is the MCP peer of
// the REST X-QRDB-Database header and the gRPC x-qrdb-database metadata. When
// absent or empty the server uses the implicit bootstrap database.
const databaseToolArg = "database"

// registryContextKey is an unexported context key carrying the per-(tenant,
// database) service registry into the data-plane tool handlers. The registry
// is bound by databaseScopeMiddleware so the resolveX helpers can route each
// call to the correct database keyspace without changing the tool handler
// signatures (which capture a singleton service for the no-registry and
// embedded/test paths).
type registryContextKey struct{}

// withRegistry returns a context carrying the service registry.
func withRegistry(ctx context.Context, reg service.Registry) context.Context {
	return context.WithValue(ctx, registryContextKey{}, reg)
}

// registryFromContext returns the service registry bound to ctx, or nil when
// none is set (the single-database / embedded path).
func registryFromContext(ctx context.Context) service.Registry {
	reg, _ := ctx.Value(registryContextKey{}).(service.Registry)
	return reg
}

// databaseScopeMiddleware returns an mcp-go tool-handler middleware that binds
// the server's service registry and the request's database scope (read from
// the optional "database" tool argument) into the per-call context. The
// data-plane tool handlers then resolve their servicer per (tenant, database)
// via the resolveX helpers. The tenant dimension flows separately from the
// authenticated identity (bound by authHTTPContext); only the database is
// sourced from the tool argument. It is installed only when a registry is
// configured, so the no-registry path is unaffected.
func (s *Server) databaseScopeMiddleware() mcpserver.ToolHandlerMiddleware {
	return func(next mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
		return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			ctx = withRegistry(ctx, s.registry)
			ctx = tenant.WithDatabase(ctx, mcpgo.ParseString(req, databaseToolArg, ""))
			return next(ctx, req)
		}
	}
}

// ---------------------------------------------------------------------------
// Per-(tenant, database) service resolvers
//
// Each resolver reads the tenant (from the authenticated identity) and the
// database (from the "database" tool argument, bound to ctx by
// databaseScopeMiddleware) out of ctx and delegates to the configured
// Registry. When no registry is bound to ctx the resolver returns the captured
// singleton service unchanged, preserving the single-database, embedded, and
// unit-test paths (which invoke the tool handlers directly without the
// middleware). Mirrors the gRPC/QUIC scope resolvers so every transport
// resolves the same per-(tenant, database) keyspace.
// ---------------------------------------------------------------------------

// resolveKV resolves the KVServicer for the (tenant, db) scope in ctx.
func resolveKV(ctx context.Context, fallback service.KVServicer) (service.KVServicer, error) {
	reg := registryFromContext(ctx)
	if reg == nil {
		return fallback, nil
	}
	return reg.KVService(tenant.FromContext(ctx), tenant.DatabaseFromContext(ctx))
}

// resolveMVCC resolves the MVCCKVServicer for the (tenant, db) scope in ctx.
func resolveMVCC(ctx context.Context, fallback service.MVCCKVServicer) (service.MVCCKVServicer, error) {
	reg := registryFromContext(ctx)
	if reg == nil {
		if fallback == nil {
			return nil, &ServiceNotConfiguredError{Service: "mvcc"}
		}
		return fallback, nil
	}
	return reg.MVCCService(tenant.FromContext(ctx), tenant.DatabaseFromContext(ctx))
}

// resolveTx resolves the TransactionServicer for the (tenant, db) scope in ctx.
func resolveTx(ctx context.Context, fallback service.TransactionServicer) (service.TransactionServicer, error) {
	reg := registryFromContext(ctx)
	if reg == nil {
		return fallback, nil
	}
	return reg.TransactionService(tenant.FromContext(ctx), tenant.DatabaseFromContext(ctx))
}

// resolveBatch resolves the BatchServicer for the (tenant, db) scope in ctx.
func resolveBatch(ctx context.Context, fallback service.BatchServicer) (service.BatchServicer, error) {
	reg := registryFromContext(ctx)
	if reg == nil {
		return fallback, nil
	}
	return reg.BatchService(tenant.FromContext(ctx), tenant.DatabaseFromContext(ctx))
}

// resolveWatch resolves the WatchServicer for the (tenant, db) scope in ctx.
func resolveWatch(ctx context.Context, fallback service.WatchServicer) (service.WatchServicer, error) {
	reg := registryFromContext(ctx)
	if reg == nil {
		if fallback == nil {
			return nil, &ServiceNotConfiguredError{Service: "watch"}
		}
		return fallback, nil
	}
	return reg.WatchService(tenant.FromContext(ctx), tenant.DatabaseFromContext(ctx))
}

// resolveLease resolves the LeaseServicer for the (tenant, db) scope in ctx.
func resolveLease(ctx context.Context, fallback service.LeaseServicer) (service.LeaseServicer, error) {
	reg := registryFromContext(ctx)
	if reg == nil {
		if fallback == nil {
			return nil, &ServiceNotConfiguredError{Service: "lease"}
		}
		return fallback, nil
	}
	return reg.LeaseService(tenant.FromContext(ctx), tenant.DatabaseFromContext(ctx))
}

// resolveIndex resolves the IndexServicer for the (tenant, db) scope in ctx.
func resolveIndex(ctx context.Context, fallback service.IndexServicer) (service.IndexServicer, error) {
	reg := registryFromContext(ctx)
	if reg == nil {
		return fallback, nil
	}
	return reg.IndexService(tenant.FromContext(ctx), tenant.DatabaseFromContext(ctx))
}

// resolveTS resolves the TimeSeriesServicer for the (tenant, db) scope in ctx.
func resolveTS(ctx context.Context, fallback service.TimeSeriesServicer) (service.TimeSeriesServicer, error) {
	reg := registryFromContext(ctx)
	if reg == nil {
		return fallback, nil
	}
	return reg.TimeSeriesService(tenant.FromContext(ctx), tenant.DatabaseFromContext(ctx))
}

// resolveVector resolves the VectorServicer for the (tenant, db) scope in ctx.
func resolveVector(ctx context.Context, fallback service.VectorServicer) (service.VectorServicer, error) {
	reg := registryFromContext(ctx)
	if reg == nil {
		return fallback, nil
	}
	return reg.VectorService(tenant.FromContext(ctx), tenant.DatabaseFromContext(ctx))
}

// resolveLock resolves the LockServicer for the (tenant, db) scope in ctx.
//
// Before go-qrdb#455 the lock tools closed over one process-wide servicer
// built over the raw, un-namespaced engines. A lock key is "__sys:lock/{key}"
// with the key supplied by the caller and no tenant segment, so under prefix
// isolation two tenants locking the same name took the same lock -- and List,
// Status and LockHeldError handed each of them the other's lock names and
// holder IDs. With no registry bound to ctx the captured singleton is
// returned unchanged, which is the single-database, embedded and unit-test
// path.
func resolveLock(ctx context.Context, fallback service.LockServicer) (service.LockServicer, error) {
	reg := registryFromContext(ctx)
	if reg == nil {
		if fallback == nil {
			return nil, &ServiceNotConfiguredError{Service: "lock"}
		}
		return fallback, nil
	}
	return reg.LockService(tenant.FromContext(ctx), tenant.DatabaseFromContext(ctx))
}

// resolveCollection resolves the CollectionServicer for the (tenant, database)
// scope of this tool call. Unlike its neighbours the database is passed in
// rather than read from the "database" tool argument via the scope context:
// every collection tool already requires and parses that argument itself, and
// the database the tool names is the one whose keyspace it must resolve into.
//
// Before go-qrdb#441 the collection tools closed over one process-wide
// servicer built over the raw, un-namespaced engines, so two tenants naming
// the same database and collection read and wrote the same documents. With no
// registry bound to ctx the captured singleton is returned unchanged, which is
// the single-database, embedded and unit-test path.
func resolveCollection(ctx context.Context, fallback service.CollectionServicer, database string) (service.CollectionServicer, error) {
	reg := registryFromContext(ctx)
	if reg == nil {
		if fallback == nil {
			return nil, &ServiceNotConfiguredError{Service: "collection"}
		}
		return fallback, nil
	}
	return reg.CollectionService(tenant.FromContext(ctx), database)
}
