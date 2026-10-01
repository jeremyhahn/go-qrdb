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

package provider

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"

	sqle "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Statement-scoped resolution
//
// go-mysql-server resolves a table by name several times while it plans and
// runs one statement, and information_schema resolves every table of the
// database. Each resolution asked the service registry for five servicers --
// each of which re-resolved the tenant with a linearizable read -- and read
// the table's definition with three more. Within one statement none of
// those answers can change in a way the statement may observe: the
// statement's plan is built once, and every write it makes is fenced on the
// definition and generation it resolved (see resolutionCompares). So a
// statement resolves each servicer and each definition once and reuses it.
//
// The scope is opened by QRDBSession.CommandBegin and closed by CommandEnd,
// which every SQL surface calls around every statement: go-mysql-server's
// MySQL handler, the PostgreSQL wire, the ad-hoc query service and the
// embedded driver. A caller that brackets nothing simply resolves fresh.
// Nothing outlives the statement, so DDL committed by another session is
// seen by this session's next statement.
//
// Only statements that cannot change a definition are memoized: a SELECT,
// INSERT, UPDATE, DELETE or REPLACE (and the read-only SHOW, DESCRIBE,
// EXPLAIN, TABLE and VALUES forms). A statement that may run DDL -- CREATE,
// ALTER, DROP, RENAME, TRUNCATE, a CALL of a procedure, EXECUTE of a
// prepared text -- re-resolves on every lookup, so a definition it changes
// part way through is seen by its own later lookups.
// ---------------------------------------------------------------------------

// memoizedStatements are the leading keywords of statements whose table
// definitions cannot change while they run.
var memoizedStatements = map[string]struct{}{
	"SELECT": {}, "WITH": {}, "INSERT": {}, "UPDATE": {}, "DELETE": {}, "REPLACE": {},
	"SHOW": {}, "DESCRIBE": {}, "DESC": {}, "EXPLAIN": {}, "TABLE": {}, "VALUES": {},
}

// statementScope memoizes service and catalog resolution for one statement.
// Its zero value is an open scope not yet bound to a statement.
type statementScope struct {
	// bound is the statement the scope was first used for. A lookup made
	// under different statement text -- a statement the engine runs on the
	// same session while this one is open -- does not use the scope.
	bound atomic.Pointer[boundStatement]

	services sync.Map // serviceKey -> resolved servicer
	catalog  sync.Map // catalogKey -> catalogEntry
}

// boundStatement is the statement text a scope serves and whether that
// statement's resolution may be memoized.
type boundStatement struct {
	query    string
	memoized bool
}

// serviceKey names one servicer resolved for one (tenant, database).
type serviceKey struct {
	kind, tenant, db string
}

// activeScope returns the statement scope open on ctx's session for the
// statement ctx is running, or nil when there is none or the statement is
// one whose resolution must not be memoized.
func activeScope(ctx *sqle.Context) *statementScope {
	if ctx == nil {
		return nil
	}
	sess, ok := ctx.Session.(*QRDBSession)
	if !ok {
		return nil
	}
	scope := sess.stmt.Load()
	if scope == nil {
		return nil
	}
	query := ctx.Query()
	if query == "" {
		// A context that does not carry its statement cannot be told apart
		// from another statement's, and must not bind the scope.
		return nil
	}
	b := scope.bound.Load()
	if b == nil {
		fresh := &boundStatement{query: query, memoized: memoizable(query)}
		if scope.bound.CompareAndSwap(nil, fresh) {
			b = fresh
		} else {
			b = scope.bound.Load()
		}
	}
	if !b.memoized || b.query != query {
		return nil
	}
	return scope
}

// memoizable reports whether query's resolution may be memoized: its first
// keyword, after leading whitespace, comments and parentheses, names a
// statement that cannot change a table definition. An executable comment
// (/*! ... */) is not skipped -- MySQL runs its contents -- so a statement
// that starts with one is not memoized.
func memoizable(query string) bool {
	i := 0
	for i < len(query) {
		switch c := query[i]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '(':
			i++
		case c == '#' || (c == '-' && strings.HasPrefix(query[i:], "-- ")):
			end := strings.IndexByte(query[i:], '\n')
			if end < 0 {
				return false
			}
			i += end + 1
		case strings.HasPrefix(query[i:], "/*"):
			if strings.HasPrefix(query[i:], "/*!") || strings.HasPrefix(query[i:], "/*+") {
				return false
			}
			end := strings.Index(query[i+2:], "*/")
			if end < 0 {
				return false
			}
			i += end + 4
		default:
			j := i
			for j < len(query) && isKeywordByte(query[j]) {
				j++
			}
			_, ok := memoizedStatements[strings.ToUpper(query[i:j])]
			return ok
		}
	}
	return false
}

// isKeywordByte reports whether c can appear in a SQL keyword.
func isKeywordByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// registryFor returns the registry d resolves servicers through for the
// statement ctx is running: d.registry, memoized for the statement when a
// statement scope is open (see statementScope).
func (d *Database) registryFor(ctx *sqle.Context) service.Registry {
	scope := activeScope(ctx)
	if scope == nil || d.registry == nil {
		return d.registry
	}
	return &memoRegistry{Registry: d.registry, scope: scope}
}

// memoRegistry resolves each servicer a statement asks for once. Methods it
// does not override -- servicers the SQL provider does not resolve per
// table lookup -- go straight to the registry.
type memoRegistry struct {
	service.Registry
	scope *statementScope
}

// memo returns the servicer kind resolved for (tenant, db), resolving it
// with resolve the first time. A failed resolution is not memoized: the next
// lookup asks again and reports its own outcome.
func memo[T any](s *statementScope, kind, tenant, db string, resolve func(tenant, db string) (T, error)) (T, error) {
	key := serviceKey{kind: kind, tenant: tenant, db: db}
	if v, ok := s.services.Load(key); ok {
		// A registry may resolve a servicer to nil (one the store does not
		// offer); the comma-ok form hands that nil back rather than panic.
		svc, _ := v.(T)
		return svc, nil
	}
	svc, err := resolve(tenant, db)
	if err != nil {
		return svc, err
	}
	s.services.Store(key, svc)
	return svc, nil
}

// KVService implements service.Registry.
func (m *memoRegistry) KVService(tenant, db string) (service.KVServicer, error) {
	return memo(m.scope, "kv", tenant, db, m.Registry.KVService)
}

// MVCCService implements service.Registry.
func (m *memoRegistry) MVCCService(tenant, db string) (service.MVCCKVServicer, error) {
	return memo(m.scope, "mvcc", tenant, db, m.Registry.MVCCService)
}

// TransactionService implements service.Registry.
func (m *memoRegistry) TransactionService(tenant, db string) (service.TransactionServicer, error) {
	return memo(m.scope, "txn", tenant, db, m.Registry.TransactionService)
}

// TableService implements service.Registry.
func (m *memoRegistry) TableService(tenant, db string) (service.TableServicer, error) {
	return memo(m.scope, "table", tenant, db, m.Registry.TableService)
}

// SequenceService implements service.Registry.
func (m *memoRegistry) SequenceService(tenant, db string) (service.SequenceServicer, error) {
	return memo(m.scope, "seq", tenant, db, m.Registry.SequenceService)
}

// LeaseService implements service.Registry.
func (m *memoRegistry) LeaseService(tenant, db string) (service.LeaseServicer, error) {
	return memo(m.scope, "lease", tenant, db, m.Registry.LeaseService)
}

// WatchService implements service.Registry.
func (m *memoRegistry) WatchService(tenant, db string) (service.WatchServicer, error) {
	return memo(m.scope, "watch", tenant, db, m.Registry.WatchService)
}

// CollectionService implements service.Registry.
func (m *memoRegistry) CollectionService(tenant, db string) (service.CollectionServicer, error) {
	return memo(m.scope, "collection", tenant, db, m.Registry.CollectionService)
}

// catalogKey names one catalog answer -- a table definition, a view
// definition, or the foreign keys naming a parent -- for one (tenant,
// database).
type catalogKey struct {
	kind, tenant, db, name string
}

// catalogEntry is a memoized catalog answer: the value, or the "does not
// exist" error it was answered with.
type catalogEntry struct {
	value any
	err   error
}

// memoCatalog returns the catalog answer kind/name of (tenant, db) for the
// statement ctx is running, resolving it with resolve once per statement.
// An answer that the thing does not exist (absent reports which errors say
// so) is memoized like a found one; any other failure is not, so the next
// lookup asks again. Without an active scope every call resolves.
func memoCatalog[T any](ctx *sqle.Context, kind, tenant, db, name string, resolve func() (T, error), absent func(error) bool) (T, error) {
	scope := activeScope(ctx)
	if scope == nil {
		return resolve()
	}
	key := catalogKey{kind: kind, tenant: tenant, db: db, name: name}
	if v, ok := scope.catalog.Load(key); ok {
		if e, ok := v.(catalogEntry); ok {
			value, _ := e.value.(T)
			return value, e.err
		}
	}
	value, err := resolve()
	if err == nil || absent(err) {
		scope.catalog.Store(key, catalogEntry{value: value, err: err})
	}
	return value, err
}

// neverAbsent is the absent predicate of a lookup with no "does not exist"
// answer.
func neverAbsent(error) bool { return false }

// isTableNotFound reports whether err says the table does not exist.
func isTableNotFound(err error) bool {
	var notFound *service.TableNotFoundError
	return errors.As(err, &notFound)
}

// isViewNotFound reports whether err says the view does not exist.
func isViewNotFound(err error) bool {
	var notFound *service.ViewNotFoundError
	return errors.As(err, &notFound)
}

// tableDef returns the definition of (tenant, db).name, resolved once per
// statement (see memoCatalog).
func tableDef(ctx *sqle.Context, tenant, db, name string, resolve func() (*service.TableDef, error)) (*service.TableDef, error) {
	return memoCatalog(ctx, "table", tenant, db, name, resolve, isTableNotFound)
}

// viewDef returns the view (tenant, db).name, resolved once per statement.
func viewDef(ctx *sqle.Context, tenant, db, name string, resolve func() (*service.ViewDef, error)) (*service.ViewDef, error) {
	return memoCatalog(ctx, "view", tenant, db, name, resolve, isViewNotFound)
}

// referencingForeignKeys returns the foreign keys in (tenant, db) that name
// parent, resolved once per statement.
func referencingForeignKeys(ctx *sqle.Context, tenant, db, parent string, resolve func() ([]service.TableForeignKey, error)) ([]service.TableForeignKey, error) {
	return memoCatalog(ctx, "fk-parent", tenant, db, parent, resolve, neverAbsent)
}
