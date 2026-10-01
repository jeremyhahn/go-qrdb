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

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// scopeMiddleware reads X-QRDB-Database from the incoming request header and
// injects the value into the request context. Missing or empty database header
// defaults to defaultDatabaseScope.
//
// The tenant dimension is NOT sourced from a header. The authenticated mTLS
// identity is the sole source of tenant — the auth middleware binds it via
// pkg/tenant.WithTenantID before this middleware runs, and downstream handlers
// read it through tenantFromContext (which delegates to tenant.FromContext).
// Accepting a tenant from an unauthenticated header was a spoofing vector and
// has been removed.
//
// It is positioned between CORS and auth in the handler chain so that both
// the auth middleware and all downstream handlers can read the database scope
// from context without re-parsing headers.
func scopeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dbName := r.Header.Get(HeaderDatabase)
		if dbName == "" {
			dbName = defaultDatabaseScope
		}

		ctx := r.Context()
		ctx = context.WithValue(ctx, dbNameKey, dbName)
		// Bind the canonical database scope as well, so a service that reads
		// the (tenant, database) pair from the context -- the RAG store, for
		// one -- sees the same database on REST as on QUIC and MCP.
		ctx = tenant.WithDatabase(ctx, dbName)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
