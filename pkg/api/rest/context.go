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

import "context"

// contextKey is an unexported integer type for context keys scoped to this
// package. Using a typed int prevents collisions with keys from other packages.
type contextKey int

const (
	// dbNameKey is the context key for the scoped database name.
	dbNameKey contextKey = iota

	// tenantIDKey is the context key for the scoped tenant ID.
	tenantIDKey
)

// Header name constants for the two scoping dimensions. Exported so that
// middleware, tests, and SDK documentation share a single source of truth.
const (
	// HeaderDatabase is the HTTP header that carries the target database name.
	// Defaults to "default" when absent.
	HeaderDatabase = "X-QRDB-Database"

	// HeaderTenant is the HTTP header that carries the target tenant ID.
	// Defaults to "default" when absent.
	HeaderTenant = "X-QRDB-Tenant"

	// defaultScope is the fallback value for both tenant and database when
	// the corresponding header is absent or empty.
	defaultScope = "default"
)

// databaseFromContext returns the database name stored in ctx by
// scopeMiddleware. Returns "default" when no database is set.
func databaseFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(dbNameKey).(string); ok && v != "" {
		return v
	}
	return defaultScope
}

// tenantFromContext returns the tenant ID stored in ctx by scopeMiddleware.
// Returns "default" when no tenant is set.
func tenantFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(tenantIDKey).(string); ok && v != "" {
		return v
	}
	return defaultScope
}
