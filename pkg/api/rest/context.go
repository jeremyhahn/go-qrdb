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

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// contextKey is an unexported integer type for context keys scoped to this
// package. Using a typed int prevents collisions with keys from other packages.
type contextKey int

const (
	// dbNameKey is the context key for the scoped database name.
	dbNameKey contextKey = iota
)

// Header name constants for the scoping dimension. Exported so that
// middleware, tests, and SDK documentation share a single source of truth.
const (
	// HeaderDatabase is the HTTP header that carries the target database name.
	// Defaults to defaultDatabaseScope when absent.
	HeaderDatabase = "X-QRDB-Database"

	// defaultDatabaseScope is the fallback database name when the database
	// header is absent or empty. It is the well-known bootstrap database the
	// daemon auto-creates at boot. Single-database deployments use this name.
	defaultDatabaseScope = "system"
)

// databaseFromContext returns the database name stored in ctx by
// scopeMiddleware. Returns defaultDatabaseScope when no database is set.
func databaseFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(dbNameKey).(string); ok && v != "" {
		return v
	}
	return defaultDatabaseScope
}

// tenantFromContext returns the tenant ID from the canonical tenant context
// bound by the auth middleware from the authenticated mTLS identity. Returns
// tenantid.System when no tenant is bound (unauthenticated endpoints,
// embedded mode).
func tenantFromContext(ctx context.Context) string {
	return tenant.FromContext(ctx)
}
