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

package rest

import (
	"context"
	"net/http"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Per-(tenant, database) service resolvers
//
// Each resolver reads tenant and database from the request context (populated
// by scopeMiddleware) and delegates to the ServiceRegistry when one is
// configured. When no registry is set, the resolver falls back to the singleton
// service field, preserving backward compatibility with single-DB deployments.
// ---------------------------------------------------------------------------

// kvSvcFor resolves the KVServicer for the (tenant, db) scope in ctx. When
// the registry returns an error it writes the appropriate HTTP response and
// returns nil — callers must check for nil and return immediately.
func (s *Server) kvSvcFor(w http.ResponseWriter, ctx context.Context) service.KVServicer {
	if s.serviceRegistry == nil {
		return s.kvSvc
	}
	tenant := tenantFromContext(ctx)
	db := databaseFromContext(ctx)
	svc, err := s.serviceRegistry.KVService(tenant, db)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return nil
	}
	return svc
}

// mvccSvcFor resolves the MVCCKVServicer for the (tenant, db) scope in ctx.
func (s *Server) mvccSvcFor(w http.ResponseWriter, ctx context.Context) service.MVCCKVServicer {
	if s.serviceRegistry == nil {
		return s.mvccSvc
	}
	tenant := tenantFromContext(ctx)
	db := databaseFromContext(ctx)
	svc, err := s.serviceRegistry.MVCCService(tenant, db)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return nil
	}
	return svc
}

// watchSvcFor resolves the WatchServicer for the (tenant, db) scope in ctx.
func (s *Server) watchSvcFor(w http.ResponseWriter, ctx context.Context) service.WatchServicer {
	if s.serviceRegistry == nil {
		return s.watchSvc
	}
	tenant := tenantFromContext(ctx)
	db := databaseFromContext(ctx)
	svc, err := s.serviceRegistry.WatchService(tenant, db)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return nil
	}
	return svc
}

// leaseSvcFor resolves the LeaseServicer for the (tenant, db) scope in ctx.
func (s *Server) leaseSvcFor(w http.ResponseWriter, ctx context.Context) service.LeaseServicer {
	if s.serviceRegistry == nil {
		return s.leaseSvc
	}
	tenant := tenantFromContext(ctx)
	db := databaseFromContext(ctx)
	svc, err := s.serviceRegistry.LeaseService(tenant, db)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return nil
	}
	return svc
}

// indexSvcFor resolves the IndexServicer for the (tenant, db) scope in ctx.
func (s *Server) indexSvcFor(w http.ResponseWriter, ctx context.Context) service.IndexServicer {
	if s.serviceRegistry == nil {
		return s.indexSvc
	}
	tenant := tenantFromContext(ctx)
	db := databaseFromContext(ctx)
	svc, err := s.serviceRegistry.IndexService(tenant, db)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return nil
	}
	return svc
}

// tsSvcFor resolves the TimeSeriesServicer for the (tenant, db) scope in ctx.
func (s *Server) tsSvcFor(w http.ResponseWriter, ctx context.Context) service.TimeSeriesServicer {
	if s.serviceRegistry == nil {
		return s.tsSvc
	}
	tenant := tenantFromContext(ctx)
	db := databaseFromContext(ctx)
	svc, err := s.serviceRegistry.TimeSeriesService(tenant, db)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return nil
	}
	return svc
}

// vectorSvcFor resolves the VectorServicer for the (tenant, db) scope in ctx.
func (s *Server) vectorSvcFor(w http.ResponseWriter, ctx context.Context) service.VectorServicer {
	if s.serviceRegistry == nil {
		return s.vectorSvc
	}
	tenant := tenantFromContext(ctx)
	db := databaseFromContext(ctx)
	svc, err := s.serviceRegistry.VectorService(tenant, db)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return nil
	}
	return svc
}

// txSvcFor resolves the TransactionServicer for the (tenant, db) scope in ctx.
func (s *Server) txSvcFor(w http.ResponseWriter, ctx context.Context) service.TransactionServicer {
	if s.serviceRegistry == nil {
		return s.txSvc
	}
	tenant := tenantFromContext(ctx)
	db := databaseFromContext(ctx)
	svc, err := s.serviceRegistry.TransactionService(tenant, db)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return nil
	}
	return svc
}

// batchSvcFor resolves the BatchServicer for the (tenant, db) scope in ctx.
func (s *Server) batchSvcFor(w http.ResponseWriter, ctx context.Context) service.BatchServicer {
	if s.serviceRegistry == nil {
		return s.batchSvc
	}
	tenant := tenantFromContext(ctx)
	db := databaseFromContext(ctx)
	svc, err := s.serviceRegistry.BatchService(tenant, db)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return nil
	}
	return svc
}
