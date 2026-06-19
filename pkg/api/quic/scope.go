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
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// ---------------------------------------------------------------------------
// Per-(tenant, database) service resolvers
//
// Each resolver reads the tenant (from the authenticated identity) and the
// database (from the request frame, bound to ctx in handleStream) out of ctx
// and delegates to the configured Registry. When no registry is configured the
// resolver falls back to the singleton service field, preserving backward
// compatibility with single-database and embedded deployments. When the
// singleton is also nil a ServiceNotConfiguredError is returned (mapped to
// StatusUnimplemented). This mirrors the gRPC scope resolvers so every
// transport resolves the same per-(tenant, database) keyspace.
// ---------------------------------------------------------------------------

// kvSvcFor resolves the KVServicer for the (tenant, db) scope in ctx.
func (s *Server) kvSvcFor(ctx context.Context) (service.KVServicer, error) {
	if s.registry == nil {
		if s.kvSvc == nil {
			return nil, &ServiceNotConfiguredError{Service: "kv"}
		}
		return s.kvSvc, nil
	}
	return s.registry.KVService(tenant.FromContext(ctx), tenant.DatabaseFromContext(ctx))
}

// mvccSvcFor resolves the MVCCKVServicer for the (tenant, db) scope in ctx.
func (s *Server) mvccSvcFor(ctx context.Context) (service.MVCCKVServicer, error) {
	if s.registry == nil {
		if s.mvccSvc == nil {
			return nil, &ServiceNotConfiguredError{Service: "mvcc"}
		}
		return s.mvccSvc, nil
	}
	return s.registry.MVCCService(tenant.FromContext(ctx), tenant.DatabaseFromContext(ctx))
}

// txSvcFor resolves the TransactionServicer for the (tenant, db) scope in ctx.
func (s *Server) txSvcFor(ctx context.Context) (service.TransactionServicer, error) {
	if s.registry == nil {
		if s.txSvc == nil {
			return nil, &ServiceNotConfiguredError{Service: "transaction"}
		}
		return s.txSvc, nil
	}
	return s.registry.TransactionService(tenant.FromContext(ctx), tenant.DatabaseFromContext(ctx))
}

// batchSvcFor resolves the BatchServicer for the (tenant, db) scope in ctx.
func (s *Server) batchSvcFor(ctx context.Context) (service.BatchServicer, error) {
	if s.registry == nil {
		if s.batchSvc == nil {
			return nil, &ServiceNotConfiguredError{Service: "batch"}
		}
		return s.batchSvc, nil
	}
	return s.registry.BatchService(tenant.FromContext(ctx), tenant.DatabaseFromContext(ctx))
}

// watchSvcFor resolves the WatchServicer for the (tenant, db) scope in ctx.
func (s *Server) watchSvcFor(ctx context.Context) (service.WatchServicer, error) {
	if s.registry == nil {
		if s.watchSvc == nil {
			return nil, &ServiceNotConfiguredError{Service: "watch"}
		}
		return s.watchSvc, nil
	}
	return s.registry.WatchService(tenant.FromContext(ctx), tenant.DatabaseFromContext(ctx))
}

// leaseSvcFor resolves the LeaseServicer for the (tenant, db) scope in ctx.
func (s *Server) leaseSvcFor(ctx context.Context) (lease.LeaseServicer, error) {
	if s.registry == nil {
		if s.leaseSvc == nil {
			return nil, &ServiceNotConfiguredError{Service: "lease"}
		}
		return s.leaseSvc, nil
	}
	return s.registry.LeaseService(tenant.FromContext(ctx), tenant.DatabaseFromContext(ctx))
}

// indexSvcFor resolves the IndexServicer for the (tenant, db) scope in ctx.
func (s *Server) indexSvcFor(ctx context.Context) (index.IndexServicer, error) {
	if s.registry == nil {
		if s.indexSvc == nil {
			return nil, &ServiceNotConfiguredError{Service: "index"}
		}
		return s.indexSvc, nil
	}
	return s.registry.IndexService(tenant.FromContext(ctx), tenant.DatabaseFromContext(ctx))
}

// tsSvcFor resolves the TimeSeriesServicer for the (tenant, db) scope in ctx.
func (s *Server) tsSvcFor(ctx context.Context) (timeseries.TimeSeriesServicer, error) {
	if s.registry == nil {
		if s.tsSvc == nil {
			return nil, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		return s.tsSvc, nil
	}
	return s.registry.TimeSeriesService(tenant.FromContext(ctx), tenant.DatabaseFromContext(ctx))
}

// vectorSvcFor resolves the VectorServicer for the (tenant, db) scope in ctx.
func (s *Server) vectorSvcFor(ctx context.Context) (service.VectorServicer, error) {
	if s.registry == nil {
		if s.vectorSvc == nil {
			return nil, &ServiceNotConfiguredError{Service: "vector"}
		}
		return s.vectorSvc, nil
	}
	return s.registry.VectorService(tenant.FromContext(ctx), tenant.DatabaseFromContext(ctx))
}
