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

package grpc

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// MetadataDatabase is the incoming gRPC metadata key that selects the target
// database within the authenticated tenant's keyspace. It mirrors the REST
// X-QRDB-Database header. Metadata keys are lower-cased by gRPC, so this is
// the canonical form clients must use with metadata.AppendToOutgoingContext.
const MetadataDatabase = "x-qrdb-database"

// DefaultDatabaseScope is the fallback database name when the database
// metadata is absent or empty. It is the well-known bootstrap database the
// daemon auto-creates at boot, keeping single-database deployments and
// pre-existing clients working unchanged.
const DefaultDatabaseScope = "system"

// dbContextKey is an unexported type for the database context key, preventing
// collisions with keys from other packages.
type dbContextKey struct{}

// withDatabase stores the resolved database name in ctx.
func withDatabase(ctx context.Context, db string) context.Context {
	return context.WithValue(ctx, dbContextKey{}, db)
}

// databaseFromContext returns the database name bound by the scope interceptor.
// Returns DefaultDatabaseScope when no database is set.
func databaseFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(dbContextKey{}).(string); ok && v != "" {
		return v
	}
	return DefaultDatabaseScope
}

// databaseFromMetadata reads the x-qrdb-database value from incoming gRPC
// metadata, defaulting to DefaultDatabaseScope when absent or empty.
func databaseFromMetadata(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return DefaultDatabaseScope
	}
	vals := md.Get(MetadataDatabase)
	if len(vals) == 0 || vals[0] == "" {
		return DefaultDatabaseScope
	}
	return vals[0]
}

// DatabaseScopeUnaryInterceptor copies the x-qrdb-database metadata value into
// the request context so that downstream handlers can resolve the per-(tenant,
// db) service via the configured registry. The tenant dimension flows
// separately from the authenticated identity (auth interceptor binds it via
// tenant.WithTenantID); only the database is sourced from metadata. The Unix
// socket transport reuses this interceptor.
func DatabaseScopeUnaryInterceptor(
	ctx context.Context,
	req any,
	_ *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (any, error) {
	return handler(withDatabase(ctx, databaseFromMetadata(ctx)), req)
}

// DatabaseScopeStreamInterceptor is the streaming counterpart of
// DatabaseScopeUnaryInterceptor, used by streaming RPCs such as Watch.
func DatabaseScopeStreamInterceptor(
	srv any,
	ss grpc.ServerStream,
	_ *grpc.StreamServerInfo,
	handler grpc.StreamHandler,
) error {
	ctx := withDatabase(ss.Context(), databaseFromMetadata(ss.Context()))
	return handler(srv, &scopedStream{ServerStream: ss, ctx: ctx})
}

// scopedStream overrides the stream Context with one carrying the resolved
// database scope.
type scopedStream struct {
	grpc.ServerStream
	ctx context.Context
}

// Context returns the database-scoped context.
func (w *scopedStream) Context() context.Context { return w.ctx }

// ---------------------------------------------------------------------------
// Per-(tenant, database) service resolvers
//
// Each resolver reads the tenant (from the authenticated identity) and database
// (from the scope interceptor) out of ctx and delegates to the configured
// Registry. When no registry is configured the resolver falls back to the
// singleton service field, preserving backward compatibility with single-DB
// and embedded deployments. When the singleton is also nil a
// ServiceNotConfiguredError is returned (mapped to codes.Unimplemented).
// ---------------------------------------------------------------------------

// kvSvcFor resolves the KVServicer for the (tenant, db) scope in ctx.
func (s *qrdbServiceServer) kvSvcFor(ctx context.Context) (service.KVServicer, error) {
	if s.registry == nil {
		if s.kvSvc == nil {
			return nil, &ServiceNotConfiguredError{Service: "kv"}
		}
		return s.kvSvc, nil
	}
	return s.registry.KVService(tenant.FromContext(ctx), databaseFromContext(ctx))
}

// mvccSvcFor resolves the MVCCKVServicer for the (tenant, db) scope in ctx.
func (s *qrdbServiceServer) mvccSvcFor(ctx context.Context) (service.MVCCKVServicer, error) {
	if s.registry == nil {
		if s.mvccSvc == nil {
			return nil, &ServiceNotConfiguredError{Service: "mvcc"}
		}
		return s.mvccSvc, nil
	}
	return s.registry.MVCCService(tenant.FromContext(ctx), databaseFromContext(ctx))
}

// txSvcFor resolves the TransactionServicer for the (tenant, db) scope in ctx.
func (s *qrdbServiceServer) txSvcFor(ctx context.Context) (service.TransactionServicer, error) {
	if s.registry == nil {
		if s.txSvc == nil {
			return nil, &ServiceNotConfiguredError{Service: "transaction"}
		}
		return s.txSvc, nil
	}
	return s.registry.TransactionService(tenant.FromContext(ctx), databaseFromContext(ctx))
}

// batchSvcFor resolves the BatchServicer for the (tenant, db) scope in ctx.
func (s *qrdbServiceServer) batchSvcFor(ctx context.Context) (service.BatchServicer, error) {
	if s.registry == nil {
		if s.batchSvc == nil {
			return nil, &ServiceNotConfiguredError{Service: "batch"}
		}
		return s.batchSvc, nil
	}
	return s.registry.BatchService(tenant.FromContext(ctx), databaseFromContext(ctx))
}

// watchSvcFor resolves the WatchServicer for the (tenant, db) scope in ctx.
func (s *qrdbServiceServer) watchSvcFor(ctx context.Context) (service.WatchServicer, error) {
	if s.registry == nil {
		if s.watchSvc == nil {
			return nil, &ServiceNotConfiguredError{Service: "watch"}
		}
		return s.watchSvc, nil
	}
	return s.registry.WatchService(tenant.FromContext(ctx), databaseFromContext(ctx))
}

// leaseSvcFor resolves the LeaseServicer for the (tenant, db) scope in ctx.
func (s *qrdbServiceServer) leaseSvcFor(ctx context.Context) (lease.LeaseServicer, error) {
	if s.registry == nil {
		if s.leaseSvc == nil {
			return nil, &ServiceNotConfiguredError{Service: "lease"}
		}
		return s.leaseSvc, nil
	}
	return s.registry.LeaseService(tenant.FromContext(ctx), databaseFromContext(ctx))
}

// indexSvcFor resolves the IndexServicer for the (tenant, db) scope in ctx.
func (s *qrdbServiceServer) indexSvcFor(ctx context.Context) (index.IndexServicer, error) {
	if s.registry == nil {
		if s.indexSvc == nil {
			return nil, &ServiceNotConfiguredError{Service: "index"}
		}
		return s.indexSvc, nil
	}
	return s.registry.IndexService(tenant.FromContext(ctx), databaseFromContext(ctx))
}

// tsSvcFor resolves the TimeSeriesServicer for the (tenant, db) scope in ctx.
func (s *qrdbServiceServer) tsSvcFor(ctx context.Context) (timeseries.TimeSeriesServicer, error) {
	if s.registry == nil {
		if s.tsSvc == nil {
			return nil, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		return s.tsSvc, nil
	}
	return s.registry.TimeSeriesService(tenant.FromContext(ctx), databaseFromContext(ctx))
}

// vectorSvcFor resolves the VectorServicer for the (tenant, db) scope in ctx.
func (s *qrdbServiceServer) vectorSvcFor(ctx context.Context) (service.VectorServicer, error) {
	if s.registry == nil {
		if s.vectorSvc == nil {
			return nil, &ServiceNotConfiguredError{Service: "vector"}
		}
		return s.vectorSvc, nil
	}
	return s.registry.VectorService(tenant.FromContext(ctx), databaseFromContext(ctx))
}
