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

// Package query provides the canonical service.SQLServicer implementation:
// ad-hoc SQL query execution over the service-layer KV surface via the
// go-mysql-server engine. It is the single execution core shared by the
// REST API handler (pkg/api/rest) and the Wails desktop SQL binding
// (cmd/qrdb). It lives under pkg/sql rather than pkg/service because
// pkg/sql/provider imports pkg/service.
package query

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	sqle "github.com/dolthub/go-mysql-server"
	"github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/catalog"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// engineKey identifies a cached go-mysql-server engine by the (tenant,
// database) scope its Provider was built against. Keying on tenant even
// when no Registry is configured is deliberately conservative: it costs a
// handful of duplicate cache entries for the unscoped/legacy case (every
// entry serves the same underlying kvSvc regardless of tenant; see the
// namespacePrefix doc comment on provider.Database), but it means the cache
// never needs a second code path to decide whether tenant is part of its
// identity.
type engineKey struct {
	tenant string
	db     string
}

// sqlService implements service.SQLServicer over the provider + engine
// composition. It is unexported; construct it with NewService.
type sqlService struct {
	kvSvc    service.KVServicer
	dbSvc    service.DatabaseServicer
	registry service.Registry // optional per-(tenant, db) scoped resolver
	readOnly bool             // true strips write capability from every served table

	// engines caches one go-mysql-server engine per (tenant, database)
	// scope so that repeated ad-hoc queries against the same scope reuse
	// the same analyzer catalog instead of rebuilding it on every
	// Execute call. See engineFor for the read/build split.
	engines atomic.Pointer[map[engineKey]*sqle.Engine]
	// buildMu serializes engine construction (the cache-miss path only);
	// see engineFor for why building cannot be raced.
	buildMu sync.Mutex
}

// Compile-time interface assertion.
var _ service.SQLServicer = (*sqlService)(nil)

// Option configures optional sqlService behavior.
type Option func(*sqlService)

// WithRegistry configures the service to resolve the per-(tenant, db)
// scoped KV service for every query through registry — the same
// transport-neutral seam the KV, MVCC, and batch data-plane paths use (see
// pkg/api/*/resolvers.go, scope.go) — instead of the shared kvSvc passed to
// NewService. Every query is scoped to the tenant carried on the request
// context (tenant.FromContext), which the transport's auth middleware binds
// from the authenticated identity before the request reaches Execute. This
// enforces per-tenant barrier/quota/isolation and correctly routes
// IsolationRaft/IsolationVolume tenants to their dedicated backend instead
// of the shared system store. Passing a nil registry is equivalent to not
// calling this option: the service falls back to the shared kvSvc.
func WithRegistry(registry service.Registry) Option {
	return func(s *sqlService) {
		s.registry = registry
	}
}

// WithReadOnly configures the service to expose the ad-hoc SQL query
// surface as read-only: served tables do not implement the
// InsertableTable/UpdatableTable/DeletableTable/ReplaceableTable capability
// interfaces (see provider.WithReadOnly), so DML statements (INSERT/
// UPDATE/DELETE/REPLACE) fail during analysis regardless of the caller's
// granted permission.
func WithReadOnly() Option {
	return func(s *sqlService) {
		s.readOnly = true
	}
}

// NewService creates the canonical SQL query service backed by the given
// KV service. When dbSvc is non-nil, each query resolves the target
// database through it so that only engine-appropriate tables are exposed
// and unknown databases fail with the resolver's typed not-found error.
// When dbSvc is nil (legacy single-database mode), queries run unscoped
// with all tables visible. Optional functional options (WithRegistry,
// WithReadOnly) configure per-tenant scoping and write capability; see
// their doc comments for the security implications of each.
func NewService(kvSvc service.KVServicer, dbSvc service.DatabaseServicer, opts ...Option) service.SQLServicer {
	s := &sqlService{kvSvc: kvSvc, dbSvc: dbSvc}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Execute runs query against the named database and returns the result set.
// The query is scoped to the tenant authenticated on ctx (tenant.FromContext
// / auth.IdentityFromContext, both bound by the transport's auth middleware
// before Execute is called): when a Registry is configured, the KV service
// is resolved per-(tenant, db) through it, enforcing the same barrier/quota/
// isolation guarantees as every other data-plane path; otherwise the shared
// kvSvc is used with the composite "<tenant>/<db>/" namespace prefix every
// protocol uses, so an unauthenticated or system-scoped caller still
// resolves to the system tenant exactly as before.
func (s *sqlService) Execute(ctx context.Context, dbName, query string) (*service.SQLQueryResult, error) {
	if query == "" {
		return nil, &service.SQLEmptyQueryError{}
	}
	if dbName == "" {
		return nil, &service.SQLEmptyDatabaseError{}
	}

	// Resolve the database engine for table scoping. The resolver's typed
	// error (e.g. *database.NotFoundError) is returned unwrapped so that
	// transports can map it to their not-found status.
	var engineType database.EngineType
	if s.dbSvc != nil {
		info, err := s.dbSvc.Get(dbName)
		if err != nil {
			return nil, err
		}
		engineType = info.Engine
	}

	tenantID := tenant.FromContext(ctx)

	kvSvc := s.kvSvc
	var provOpts []func(*provider.Provider)
	if s.readOnly {
		provOpts = append(provOpts, provider.WithReadOnly())
	}
	if s.registry != nil {
		// Fail closed: the typed error the registry returns (tenant not
		// found, database not found, barrier sealed, or the isolation
		// backend for a dedicated-backend tenant not being wired into this
		// server) is propagated unwrapped rather than silently falling back
		// to the shared store, which would downgrade the tenant's isolation
		// guarantee.
		scoped, err := s.registry.KVService(tenantID, dbName)
		if err != nil {
			return nil, err
		}
		kvSvc = scoped
		provOpts = append(provOpts, provider.WithPrescopedKV())
		// Also resolve user-defined relational tables (CREATE TABLE) through
		// the registry's TableService/MVCCService, scoped exactly like the
		// KV service above -- see provider.WithRegistry.
		provOpts = append(provOpts, provider.WithRegistry(s.registry))
	}

	// Reuse the cached engine for this (tenant, database) scope when one
	// exists, building and publishing it exactly once otherwise. Rebuilding
	// the provider (a lightweight struct of already-resolved services) on
	// every call is unavoidable -- kvSvc above is re-resolved per call, and
	// the cache key must match the provider it fronts -- but the expensive
	// part, the analyzer catalog inside sqle.Engine, is built at most once
	// per scope.
	prov := provider.NewProviderWithEngine(dbName, kvSvc, engineType, provOpts...)
	eng := s.engineFor(engineKey{tenant: tenantID, db: dbName}, prov)

	start := time.Now()
	result, err := run(ctx, eng, dbName, query)
	if err != nil {
		return nil, &service.SQLExecutionError{Query: query, Err: err}
	}
	result.Duration = time.Since(start).String()
	return result, nil
}

// engineFor returns the cached go-mysql-server engine for key, building and
// publishing one over prov via copy-on-write if key is not yet cached.
//
// The cache-hit path -- the overwhelmingly common case once a (tenant,
// database) scope is warm -- is fully lock-free: a single atomic.Pointer
// load plus a map read, with no lock contention between concurrent callers
// regardless of how many distinct scopes are active or how many goroutines
// are reading them simultaneously.
//
// Building a new engine (the cold path, bounded by the number of distinct
// scopes ever queried) is serialized on buildMu instead of raced via
// compare-and-swap, because engine.New ultimately calls go-mysql-server's
// sqle.New, which mutates package-global state on every invocation
// (variables.InitStatusVariables) with no synchronization of its own --
// two concurrent sqle.New calls race on that shared state regardless of
// what key each is building for. Serializing construction is therefore not
// a caching-design choice but a correctness requirement imposed by the
// upstream library; every other aspect of the cache (reads, the swap
// itself) stays lock-free.
func (s *sqlService) engineFor(key engineKey, prov *provider.Provider) *sqle.Engine {
	if ptr := s.engines.Load(); ptr != nil {
		if eng, ok := (*ptr)[key]; ok {
			return eng
		}
	}

	s.buildMu.Lock()
	defer s.buildMu.Unlock()

	// Re-check under the lock: another goroutine may have built and
	// published this exact key while this one was waiting.
	oldPtr := s.engines.Load()
	var oldMap map[engineKey]*sqle.Engine
	if oldPtr != nil {
		oldMap = *oldPtr
	}
	if eng, ok := oldMap[key]; ok {
		return eng
	}

	eng := engine.New(engine.Config{Provider: prov})

	newMap := make(map[engineKey]*sqle.Engine, len(oldMap)+1)
	for k, v := range oldMap {
		newMap[k] = v
	}
	newMap[key] = eng
	s.engines.Store(&newMap)
	return eng
}

// run creates a SQL context bound to the caller's context and carrying a
// QRDBSession seeded with the caller's authenticated identity — mirroring
// the session the MySQL/PostgreSQL wire protocol servers build per
// connection (pkg/sql/mysql/auth.go, pkg/sql/pgsql/parse.go) — so that
// provider.Database.GetTableInsensitive scopes KV/entity table access to
// the caller's tenant instead of falling back to the system tenant. It then
// executes the query and materializes the result rows.
func run(ctx context.Context, eng *sqle.Engine, dbName, query string) (*service.SQLQueryResult, error) {
	identity := auth.IdentityFromContext(ctx)
	sess := provider.NewSession(sql.NewBaseSession(), identity)
	sqlCtx := sql.NewContext(ctx, sql.WithSession(sess))
	sqlCtx.SetCurrentDatabase(dbName)

	schema, iter, _, err := eng.Query(sqlCtx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = iter.Close(sqlCtx) }()

	// Build column names from the schema.
	columns := make([]string, len(schema))
	for i, col := range schema {
		columns[i] = col.Name
	}

	rows := []map[string]any{}
	for {
		row, err := iter.Next(sqlCtx)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		rowMap := make(map[string]any, len(columns))
		for i, col := range columns {
			// Resolve against the column's declared type before formatting:
			// an ENUM cell is an ordinal and a SET cell is a bitmask, so
			// formatting either without consulting the schema renders an
			// integer where the caller stored a label (see
			// catalog.ResolveColumnValue).
			v, err := catalog.ResolveColumnValue(sqlCtx, schema[i].Type, row[i])
			if err != nil {
				return nil, err
			}
			rowMap[col] = formatValue(v)
		}
		rows = append(rows, rowMap)
	}

	return &service.SQLQueryResult{
		Columns:  columns,
		Rows:     rows,
		RowCount: len(rows),
	}, nil
}

// formatValue converts a SQL row cell value to a string suitable for JSON
// serialization. Byte slices are decoded as UTF-8 strings (so wire clients
// receive plaintext rather than base64), nil cells become empty strings,
// and all other types are formatted with fmt.Sprintf.
func formatValue(v any) string {
	switch val := v.(type) {
	case []byte:
		return string(val)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", val)
	}
}
