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
)

// scopeMiddleware reads X-QRDB-Tenant and X-QRDB-Database from the incoming
// request headers exactly once and injects their values into the request
// context. Missing or empty headers default to "default".
//
// It is positioned between CORS and auth in the handler chain so that both
// the auth middleware and all downstream handlers can read the scope from
// context without re-parsing headers.
func scopeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantID := r.Header.Get(HeaderTenant)
		if tenantID == "" {
			tenantID = defaultScope
		}

		dbName := r.Header.Get(HeaderDatabase)
		if dbName == "" {
			dbName = defaultScope
		}

		ctx := r.Context()
		ctx = context.WithValue(ctx, tenantIDKey, tenantID)
		ctx = context.WithValue(ctx, dbNameKey, dbName)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
