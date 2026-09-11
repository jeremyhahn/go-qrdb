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

// lockSvcFor resolves the LockServicer for the (tenant, db) scope in ctx.
//
// Before go-qrdb#455 the lock handlers called one process-wide servicer built
// over the raw, un-namespaced engines. A lock key is "__sys:lock/{key}" with
// the key supplied by the caller and no tenant segment, so under prefix
// isolation two tenants locking the same name took the same lock -- and
// List, Status and LockHeldError handed each of them the other's lock names
// and holder IDs. Resolving here puts every lock in the caller's own
// keyspace. When no registry is configured the singleton is returned
// unchanged: that is the single-tenant composition, where the whole store and
// the caller's own keyspace are the same set.
func (s *Server) lockSvcFor(w http.ResponseWriter, ctx context.Context) service.LockServicer {
	if s.serviceRegistry == nil {
		if s.lockSvc == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "lock"})
			return nil
		}
		return s.lockSvc
	}
	svc, err := s.serviceRegistry.LockService(tenantFromContext(ctx), databaseFromContext(ctx))
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return nil
	}
	return svc
}

// collectionSvcFor resolves the CollectionServicer for the (tenant, database)
// scope of this request. Unlike every other resolver here, the database is
// passed in rather than read from the scope context: the collection surface
// addresses its database in the URL path (or the request body / query string),
// not in the X-QRDB-Database header, and the database the operation names is
// the one whose keyspace it must resolve into.
//
// Before go-qrdb#441 this surface held one process-wide servicer built over
// the raw, un-namespaced engines, so two tenants naming the same database and
// collection read and wrote the same documents. When no registry is configured
// the singleton is still returned unchanged -- that is the single-tenant,
// registry-less composition, where the whole store and the caller's own
// keyspace are the same set.
// collectionSurfaceConfigured reports whether this server can serve the
// collection surface at all -- through a registry, or through the singleton.
// It is knowable WITHOUT the database, which is what lets the handlers keep
// answering "not implemented" ahead of parameter validation, as they did
// before the surface became per-(tenant, database) resolved.
func (s *Server) collectionSurfaceConfigured() bool {
	return s.serviceRegistry != nil || s.collectionSvc != nil
}

func (s *Server) collectionSvcFor(w http.ResponseWriter, ctx context.Context, database string) service.CollectionServicer {
	if s.serviceRegistry == nil {
		if s.collectionSvc == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "collection"})
			return nil
		}
		return s.collectionSvc
	}
	svc, err := s.serviceRegistry.CollectionService(tenantFromContext(ctx), database)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return nil
	}
	return svc
}
