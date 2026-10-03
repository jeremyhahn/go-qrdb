//go:build qrdb_sql

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

// Package privilege enforces qrdb's SQL table privileges -- the DATA plane --
// and serves the GRANT, REVOKE, and SHOW GRANTS statements that administer
// them.
//
// # Two disjoint authorization planes
//
// qrdb authorizes SQL over two planes with disjoint resources:
//
//	service RBAC (pkg/auth/rbac)  the CONTROL plane: may this identity
//	                              invoke this API operation at all, and may
//	                              it create or drop a database.
//	SQL privileges (this package) the DATA plane: may this identity touch
//	                              THIS table, or THIS column.
//
// The seam is one-way. RBAC gates reaching the SQL endpoint and gates the
// database lifecycle; privileges govern everything inside a database. This
// package never consults pkg/auth/rbac and there is deliberately no mapping
// between the two: a role is not a grant and a grant is not a role.
//
// # Where enforcement happens
//
// go-mysql-server asks its sql.AuthorizationHandler about every statement it
// builds, through the AuthInformation the Vitess parser attaches to each
// node. That is the property this package is built on: coverage comes from
// the engine asking, not from a list maintained here. A statement kind nobody
// enumerated still arrives, and an AuthType this package does not recognise
// is DENIED on a latched database rather than waved through.
//
// # Where administration happens
//
// GRANT, REVOKE, and SHOW GRANTS are real MySQL syntax, parsed by the real
// parser into real plan nodes, and then rewritten by an analyzer post-analyze
// rule (rewrite.go) onto qrdb's own catalog before execution. Nothing reaches
// go-mysql-server's mysql_db grant tables, which qrdb does not implement.
package privilege

import (
	"strings"
	"sync"
	"sync/atomic"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/analyzer"
	ast "github.com/dolthub/vitess/go/vt/sqlparser"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// init installs qrdb's authorization handler as the process-wide factory.
//
// The factory is a package-level global in go-mysql-server and is read when a
// sql.Catalog is constructed, so it must be set before any engine is built.
// A plain init is sufficient and no blank-import ordering hack is needed:
// go-mysql-server installs its own default from
// sql/planbuilder/auth_default.go's init, and qrdb imports planbuilder
// transitively through every path that reaches an engine, so package
// initialisation order guarantees planbuilder's init runs first and this one
// overwrites it.
func init() {
	sqle.SetAuthorizationHandlerFactory(handlerFactory{})
}

// mySQLSchemaName is MySQL's reserved system schema, served by
// pkg/sql/provider as an empty stand-in purely so that the plan builder can
// resolve GRANT, REVOKE, and SHOW GRANTS. It is spelled out here rather than
// imported so that this package stays independent of which provider built the
// catalog it was installed into; the name is fixed by MySQL, not by qrdb, so
// the two spellings cannot drift.
const mySQLSchemaName = "mysql"

// pgCatalogSchemaName is PostgreSQL's reserved system catalogue schema,
// served by pkg/sql/pgcatalog. Like mySQLSchemaName it is spelled out rather
// than imported: the name is fixed by PostgreSQL, and this package stays
// independent of which provider built the catalog it was installed into.
const pgCatalogSchemaName = "pg_catalog"

// reservedSchemas are never privilege-checked. information_schema is metadata
// every session may read, the mysql stand-in has no tables at all, and
// pg_catalog is information_schema's PostgreSQL counterpart -- computed
// metadata over relations that implement no write interface at all. None
// holds user data.
//
// pg_catalog has to be here for the same structural reason information_schema
// does. This plane resolves a privilege snapshot per (tenant, database), and
// a system schema is not a tenant database: asking the registry for one
// yields DatabaseNotFoundError, which surfaced as a hard query failure rather
// than a permission decision. That is what made every PostgreSQL schema
// introspection query fail -- gorm.io/driver/postgres's Migrator joins
// information_schema.columns against pg_type and pg_catalog.pg_description on
// every ColumnTypes call, so the unqualified half resolved and the qualified
// half did not.
//
// Exempting these schemas is not a hole in the two-plane model: it is the
// statement that they sit outside the data plane entirely, which is why
// neither of the other two entries is privilege-checked either.
var reservedSchemas = map[string]struct{}{
	sqle.InformationSchemaDatabaseName: {},
	mySQLSchemaName:                    {},
	pgCatalogSchemaName:                {},
}

// isReservedSchema reports whether name is a schema this plane does not
// govern.
func isReservedSchema(name string) bool {
	_, ok := reservedSchemas[strings.ToLower(name)]
	return ok
}

// handlerFactory implements sql.AuthorizationHandlerFactory.
type handlerFactory struct{}

// Compile-time interface assertions.
var (
	_ sqle.AuthorizationHandlerFactory = handlerFactory{}
	_ sqle.AuthorizationHandler        = (*handler)(nil)
	_ sqle.AuthorizationQueryState     = (*queryState)(nil)
)

// CreateHandler implements sql.AuthorizationHandlerFactory. One handler is
// created per catalog, i.e. per engine, and lives as long as it does -- which
// is what lets it own the snapshot cache.
func (handlerFactory) CreateHandler(cat sqle.Catalog) sqle.AuthorizationHandler {
	return &handler{registry: registryOf(cat)}
}

// registryOf extracts the per-(tenant, database) service registry from the
// catalog's database provider, or nil when there is none -- a legacy KV-only
// provider, or any provider that is not qrdb's. A nil registry means there is
// no privilege catalog to consult and every statement is permitted, which is
// the posture of every engine qrdb built before privileges existed.
func registryOf(cat sqle.Catalog) service.Registry {
	analyzerCat, ok := cat.(*analyzer.Catalog)
	if !ok {
		return nil
	}
	src, ok := analyzerCat.DbProvider.(registrySource)
	if !ok {
		return nil
	}
	return src.Registry()
}

// handler implements sql.AuthorizationHandler over qrdb's per-database
// privilege catalog.
type handler struct {
	// registry resolves the privilege catalog for one (tenant, database)
	// pair. Nil disables enforcement entirely.
	registry service.Registry

	// snapshots caches one resolved privilege set per (tenant, database,
	// grantee), gated on the catalog generation the service maintains.
	//
	// The read path never takes a mutex: sync.Map's Load is atomic in the
	// steady state, and each slot publishes its snapshot through an
	// atomic.Pointer. Entries are never evicted, which bounds the cache at
	// the deployment's real (tenant x database x grantee) cardinality --
	// the same cardinality the privilege catalog itself has.
	snapshots sync.Map
}

// snapshot is one resolved privilege set together with the generations that
// validate it.
type snapshot struct {
	// latched reports whether the database has a privilege catalog at all.
	// False means unrestricted and set is nil.
	latched bool

	// generation is the database-wide catalog generation this snapshot was
	// resolved at.
	generation uint64

	// epoch is the grantee's own record generation.
	epoch uint64

	// set is the grantee's privilege record, nil when the grantee holds no
	// record. Every accessor on *service.PrivilegeSet tolerates nil.
	set *service.PrivilegeSet
}

// permits reports whether the snapshot allows want on table, treating an
// unlatched database as unrestricted.
func (s *snapshot) permits(table string, want service.PrivilegeMask) bool {
	if s == nil || !s.latched {
		return true
	}
	return s.set.Table(table).Has(want)
}

// permitsColumn reports whether the snapshot allows want on one column.
func (s *snapshot) permitsColumn(table, column string, want service.PrivilegeMask) bool {
	if s == nil || !s.latched {
		return true
	}
	return s.set.Column(table, column).Has(want)
}

// permitsDatabase reports whether the snapshot allows want at the database
// tier, which is the check for a statement that names no table.
func (s *snapshot) permitsDatabase(want service.PrivilegeMask) bool {
	if s == nil || !s.latched {
		return true
	}
	return s.set.OnDatabase.Has(want)
}

// cacheKey identifies one cached snapshot.
type cacheKey struct {
	tenant   string
	database string
	grantee  string
}

// slot returns the publication cell for key, creating it on first use.
func (h *handler) slot(key cacheKey) *atomic.Pointer[snapshot] {
	if cell, ok := h.snapshots.Load(key); ok {
		return cell.(*atomic.Pointer[snapshot]) //nolint:errcheck // type assertion on a value this container only ever holds
	}
	cell, _ := h.snapshots.LoadOrStore(key, &atomic.Pointer[snapshot]{})
	return cell.(*atomic.Pointer[snapshot]) //nolint:errcheck // type assertion on a value this container only ever holds
}

// resolve returns the current snapshot for one (tenant, database, grantee),
// re-reading only what the cached snapshot cannot still vouch for.
//
// The staged read lives in service.PrivilegeServicer.Resolve; this side
// supplies what it already holds and republishes the result.
//
// The publication is an unconditional Store rather than a compare-and-swap,
// and that is safe rather than sloppy. The service reads the catalog
// generation FIRST, so the privilege set it returns is never older than the
// generation it returns alongside it: an entry filed under generation N holds
// a set from N or later. Two concurrent queries can therefore land their
// stores in either order without any outcome except an extra read -- an entry
// overwritten by an older generation simply fails the next query's generation
// check and is re-resolved. No ordering makes a stale set look current.
func (h *handler) resolve(ctx *sqle.Context, p principal, database string) (*snapshot, error) {
	svc, err := h.registry.PrivilegeService(p.tenant, database)
	if err != nil {
		return nil, err
	}

	cell := h.slot(cacheKey{tenant: p.tenant, database: database, grantee: p.grantee})
	cached := cell.Load()

	req := &service.ResolvePrivilegesRequest{Database: database, Grantee: p.grantee}
	if cached != nil && cached.latched {
		req.KnownGeneration = cached.generation
		req.KnownEpoch = cached.epoch
	}

	res, err := svc.Resolve(ctx, req)
	if err != nil {
		return nil, err
	}

	next := &snapshot{latched: res.Latched, generation: res.Generation, epoch: res.Epoch}
	if res.Latched {
		if res.Unchanged && cached != nil {
			next.set = cached.set
		} else {
			next.set = res.Set
		}
	}
	cell.Store(next)
	return next, nil
}

// ---------------------------------------------------------------------------
// sql.AuthorizationQueryState
// ---------------------------------------------------------------------------

// queryState is the per-query authorization state go-mysql-server resolves
// once and threads through every check of a single statement.
type queryState struct {
	h   *handler
	p   principal
	err error

	// database is the session's current database at query start, and
	// resolved is its snapshot. A check naming any other database resolves
	// through the handler's cache instead.
	database string
	resolved *snapshot
}

// Error implements sql.AuthorizationQueryState.
func (s *queryState) Error() error { return s.err }

// AuthorizationQueryStateImpl implements sql.AuthorizationQueryState.
func (s *queryState) AuthorizationQueryStateImpl() {}

// exempt reports whether this state performs no checks at all.
func (s *queryState) exempt() bool { return s.h == nil || s.h.registry == nil || s.p.exempt }

// snapshotFor returns the snapshot governing database, resolving it if it is
// not the one already held for the session's current database. An empty name
// means the session's current database.
func (s *queryState) snapshotFor(ctx *sqle.Context, database string) (*snapshot, error) {
	if database == "" {
		database = s.database
	}
	if database == "" || isReservedSchema(database) {
		return nil, nil
	}
	if database == s.database && s.resolved != nil {
		return s.resolved, nil
	}
	return s.h.resolve(ctx, s.p, database)
}

// NewQueryState implements sql.AuthorizationHandler. It resolves the
// session's privilege set exactly once per query; every subsequent check for
// the same database answers from the in-memory snapshot without touching the
// store.
//
// A session belonging to the system tenant, or carrying no identity, or
// running on an engine with no service registry, resolves nothing at all and
// costs no reads -- which is every deployment that has not armed a privilege
// catalog, and the operator escape hatch for those that have.
func (h *handler) NewQueryState(ctx *sqle.Context) sqle.AuthorizationQueryState {
	state := &queryState{h: h, p: principalOf(ctx.Session)}
	if state.exempt() {
		return state
	}

	state.database = ctx.GetCurrentDatabase()
	if state.database == "" || isReservedSchema(state.database) {
		return state
	}

	resolved, err := h.resolve(ctx, state.p, state.database)
	if err != nil {
		state.err = err
		return state
	}
	state.resolved = resolved
	return state
}

// stateOf normalises the AuthorizationQueryState go-mysql-server hands back.
// It may legitimately be nil (the interface allows it) or, in a mixed-engine
// process, one belonging to another handler; either way a fresh state is
// built rather than trusted.
func (h *handler) stateOf(ctx *sqle.Context, aqs sqle.AuthorizationQueryState) *queryState {
	if state, ok := aqs.(*queryState); ok && state.h == h {
		return state
	}
	return h.NewQueryState(ctx).(*queryState) //nolint:errcheck // type assertion on a value this container only ever holds
}

// ---------------------------------------------------------------------------
// sql.AuthorizationHandler
// ---------------------------------------------------------------------------

// HandleAuth implements sql.AuthorizationHandler. It is called for every
// statement go-mysql-server builds, with the AuthInformation the parser
// attached to the node; see authRules for how the (AuthType, TargetType) pair
// is dispatched.
func (h *handler) HandleAuth(ctx *sqle.Context, aqs sqle.AuthorizationQueryState, auth ast.AuthInformation) error {
	state := h.stateOf(ctx, aqs)
	if state.err != nil {
		return state.err
	}
	if state.exempt() {
		return nil
	}

	rule, ok := authRules[auth.AuthType]
	if !ok {
		// Fail closed. A latched database grants only what its records
		// name, and this plane can name nothing that would satisfy an
		// authorization type it does not model, so there is no set of
		// grants under which permitting it would be correct.
		return errDatabaseAccessDenied(state.p.grantee, state.database)
	}
	return rule(ctx, state, auth)
}

// HandleAuthNode implements sql.AuthorizationHandler. go-mysql-server routes
// integrator table functions here (sql/planbuilder/from.go). qrdb registers
// none, so on a latched database there is no grant that could authorise one
// and the node is refused.
func (h *handler) HandleAuthNode(ctx *sqle.Context, aqs sqle.AuthorizationQueryState, _ sqle.AuthorizationCheckerNode) error {
	state := h.stateOf(ctx, aqs)
	if state.err != nil {
		return state.err
	}
	if state.exempt() {
		return nil
	}

	snap, err := state.snapshotFor(ctx, "")
	if err != nil {
		return err
	}
	if snap == nil || !snap.latched {
		return nil
	}
	return errDatabaseAccessDenied(state.p.grantee, state.database)
}

// CheckDatabase implements sql.AuthorizationHandler: a database is reachable
// when the grantee holds any privilege anywhere inside it.
func (h *handler) CheckDatabase(ctx *sqle.Context, aqs sqle.AuthorizationQueryState, dbName string) error {
	state := h.stateOf(ctx, aqs)
	if state.err != nil {
		return state.err
	}
	if state.exempt() {
		return nil
	}
	return checkDatabaseReachable(ctx, state, dbName)
}

// CheckSchema implements sql.AuthorizationHandler. MySQL has no schema tier
// between database and table, so this is CheckDatabase.
func (h *handler) CheckSchema(ctx *sqle.Context, aqs sqle.AuthorizationQueryState, dbName string, _ string) error {
	return h.CheckDatabase(ctx, aqs, dbName)
}

// CheckTable implements sql.AuthorizationHandler: a table is reachable when
// the grantee holds any privilege on it, on one of its columns, or on the
// database that contains it.
func (h *handler) CheckTable(ctx *sqle.Context, aqs sqle.AuthorizationQueryState, dbName string, _ string, tableName string) error {
	state := h.stateOf(ctx, aqs)
	if state.err != nil {
		return state.err
	}
	if state.exempt() {
		return nil
	}
	return checkTableReachable(ctx, state, dbName, tableName)
}

// checkDatabaseReachable refuses a database in which the grantee holds
// nothing at all.
func checkDatabaseReachable(ctx *sqle.Context, state *queryState, dbName string) error {
	snap, err := state.snapshotFor(ctx, dbName)
	if err != nil {
		return err
	}
	if snap == nil || !snap.latched {
		return nil
	}
	if snap.set.Any() == 0 {
		return errDatabaseAccessDenied(state.p.grantee, effectiveDatabase(state, dbName))
	}
	return nil
}

// checkTableReachable refuses a table on which the grantee holds nothing,
// reporting the database as inaccessible when it holds nothing there either
// -- the distinction MySQL draws between ER_DBACCESS_DENIED_ERROR and
// ER_TABLEACCESS_DENIED_ERROR.
func checkTableReachable(ctx *sqle.Context, state *queryState, dbName, tableName string) error {
	snap, err := state.snapshotFor(ctx, dbName)
	if err != nil {
		return err
	}
	if snap == nil || !snap.latched {
		return nil
	}
	if snap.set.Any() == 0 {
		return errDatabaseAccessDenied(state.p.grantee, effectiveDatabase(state, dbName))
	}
	if tableName != "" && snap.set.AnyOnTable(tableName) == 0 {
		return errTableAccessDenied("SELECT", state.p.grantee, tableName)
	}
	return nil
}

// effectiveDatabase resolves an empty database reference to the session's
// current database, for use in error messages.
func effectiveDatabase(state *queryState, dbName string) string {
	if dbName != "" {
		return dbName
	}
	return state.database
}
