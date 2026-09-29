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
	"context"
	"strings"

	sqle "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/entity"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/pgcatalog"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// Compile-time interface assertions.
var (
	_ sqle.DatabaseProvider        = (*Provider)(nil)
	_ sqle.MutableDatabaseProvider = (*Provider)(nil)
	_ sqle.Database                = (*Database)(nil)
)

// ProtocolType identifies a SQL wire protocol for filtering which
// databases are visible to a given listener.
type ProtocolType string

const (
	// ProtocolAny matches all databases regardless of SQL configuration.
	ProtocolAny ProtocolType = ""

	// ProtocolMySQL matches databases that have MySQL wire protocol enabled.
	ProtocolMySQL ProtocolType = "mysql"

	// ProtocolPostgreSQL matches databases that have PostgreSQL wire protocol enabled.
	ProtocolPostgreSQL ProtocolType = "postgresql"
)

// engineTables maps each engine type to the table names it exposes.
var engineTables = map[database.EngineType][]string{
	database.EngineKV:         {tableName},
	database.EngineTimeSeries: {datapointsTableName, metricsTableName},
	database.EngineVector:     {namespacesTableName, vectorsTableName},
}

// Provider implements sql.DatabaseProvider and sql.MutableDatabaseProvider
// for QRDB. In legacy mode it serves a single database (configured name,
// default "qrdb") whose tables are backed by the KV service layer. When
// backed by a database.Manager, it exposes all managed databases and
// supports CREATE DATABASE / DROP DATABASE operations.
type Provider struct {
	dbName        string                     // legacy single-database name
	kvSvc         service.KVServicer         // shared KV service for table access
	manager       *database.Manager          // nil when using legacy single-database mode
	protocol      ProtocolType               // empty means allow all databases
	engine        database.EngineType        // engine for single-database mode with scoping
	collectionSvc service.CollectionServicer // nil when collections are not available
	indexSvc      index.IndexServicer        // nil when index acceleration is not available
	readOnly      bool                       // true strips write capability from every served table
	prescopedKV   bool                       // true when kvSvc is already scoped to the caller's namespace
	registry      service.Registry           // nil when relational tables are not available
}

// WithProtocol returns a functional option that sets the protocol filter
// on the Provider. When set, only databases that have the corresponding
// SQL wire protocol enabled will be visible.
func WithProtocol(p ProtocolType) func(*Provider) {
	return func(prov *Provider) {
		prov.protocol = p
	}
}

// WithCollectionService returns a functional option that sets the
// CollectionServicer on the Provider. When set, collection tables
// are dynamically resolved alongside static engine tables.
func WithCollectionService(svc service.CollectionServicer) func(*Provider) {
	return func(prov *Provider) {
		prov.collectionSvc = svc
	}
}

// WithIndexService returns a functional option that sets the
// IndexServicer on the Provider. When set, KV and collection tables
// can use secondary indexes to accelerate SQL WHERE clause evaluation.
func WithIndexService(svc index.IndexServicer) func(*Provider) {
	return func(prov *Provider) {
		prov.indexSvc = svc
	}
}

// WithReadOnly returns a functional option that makes every table served by
// the Provider read-only: the InsertableTable, UpdatableTable,
// DeletableTable, and ReplaceableTable capability interfaces are stripped
// structurally (see readOnlyTable), regardless of what the underlying
// concrete table implementation supports. DML statements (INSERT/UPDATE/
// DELETE/REPLACE) therefore fail during analysis rather than reaching the
// KV/entity writers, independent of the caller's granted permission.
func WithReadOnly() func(*Provider) {
	return func(prov *Provider) {
		prov.readOnly = true
	}
}

// WithPrescopedKV returns a functional option indicating that kvSvc is
// already scoped to the caller's composite (tenant, database) namespace —
// for example, resolved through service.Registry.KVService, which enforces
// per-tenant barrier/quota/isolation and routes IsolationRaft/
// IsolationVolume tenants to their dedicated backend. When set, KV and
// entity table implementations use bare keys instead of composing their own
// "<tenant>/<db>/" prefix, avoiding a second (incorrect) layer of
// namespacing on top of the one the registry already applied.
func WithPrescopedKV() func(*Provider) {
	return func(prov *Provider) {
		prov.prescopedKV = true
	}
}

// WithRegistry returns a functional option that sets the service.Registry on
// the Provider. When set, GetTableInsensitive additionally resolves
// user-defined relational tables (the tables CREATE TABLE produces) through
// reg.TableService/MVCCService/KVService, scoped to the SQL session's
// tenant. Resolution happens lazily, per call to GetTableInsensitive, rather
// than once at Provider/Database construction time: unlike the ad-hoc SQL
// query service (pkg/sql/query), which builds one Provider per query and
// therefore already knows its caller's tenant up front, a Provider serving
// the MySQL/PostgreSQL wire-protocol listeners is long-lived and multiplexes
// many connections -- and therefore many different tenants -- over its
// lifetime, so the tenant is only known once a session (via *sqle.Context)
// is available. See Database.relationalTable.
func WithRegistry(reg service.Registry) func(*Provider) {
	return func(prov *Provider) {
		prov.registry = reg
	}
}

// NewProvider creates a new Provider that serves a single database with
// the given name, backed by the provided KV service. This is the legacy
// constructor that does not support CREATE/DROP DATABASE.
func NewProvider(dbName string, kvSvc service.KVServicer) *Provider {
	return &Provider{
		dbName: dbName,
		kvSvc:  kvSvc,
	}
}

// NewManagerProvider creates a Provider backed by a database.Manager.
// It exposes all databases from the manager as SQL databases and supports
// CREATE DATABASE / DROP DATABASE via the MutableDatabaseProvider interface.
// Optional functional options (e.g., WithProtocol) can be used to configure
// protocol-level filtering.
func NewManagerProvider(manager *database.Manager, kvSvc service.KVServicer, opts ...func(*Provider)) *Provider {
	p := &Provider{
		manager: manager,
		kvSvc:   kvSvc,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// NewProviderWithEngine creates a single-database Provider with engine
// scoping. The database exposes only tables matching the given engine type.
// This constructor does not require a database.Manager and is intended for
// use by the Wails SQL binding, the ad-hoc SQL query service, and similar
// embedded contexts. Optional functional options (e.g. WithReadOnly,
// WithPrescopedKV) configure the served tables' capabilities and namespace
// scoping.
func NewProviderWithEngine(name string, kvSvc service.KVServicer, engine database.EngineType, opts ...func(*Provider)) *Provider {
	p := &Provider{
		dbName: name,
		kvSvc:  kvSvc,
		engine: engine,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// isAllowed reports whether the given database is visible under the
// provider's current protocol filter. ProtocolAny allows all databases.
func (p *Provider) isAllowed(db *database.Database) bool {
	switch p.protocol {
	case ProtocolAny:
		return true
	case ProtocolMySQL:
		return db.ProtocolConfig().MySQL
	case ProtocolPostgreSQL:
		return db.ProtocolConfig().PostgreSQL
	default:
		return true
	}
}

// newDatabase builds the sql.Database served for name over the given engine,
// carrying the provider's service wiring and scoping flags. When the provider
// is read-only the result is wrapped so that DDL-capability interfaces are
// structurally unreachable (see readOnlyDatabase); this is the single place
// that decision is made, so no construction site can forget it.
func (p *Provider) newDatabase(name string, engine database.EngineType) sqle.Database {
	db := &Database{
		name:          name,
		kvSvc:         p.kvSvc,
		engine:        engine,
		collectionSvc: p.collectionSvc,
		indexSvc:      p.indexSvc,
		readOnly:      p.readOnly,
		prescopedKV:   p.prescopedKV,
		registry:      p.registry,
		postgres:      p.servesPGCatalog(),
	}
	if p.readOnly {
		return newReadOnlyDatabase(db)
	}
	return db
}

// Registry returns the service.Registry the provider was built with, or nil
// when relational tables are not available (see WithRegistry).
//
// It exists so that components which are handed the go-mysql-server catalog
// rather than the Provider -- the SQL authorization handler and the
// GRANT/REVOKE rewrite rule, both of which reach the provider through
// analyzer.Catalog.DbProvider and a narrow local interface -- can resolve the
// per-(tenant, database) privilege catalog without importing this package.
func (p *Provider) Registry() service.Registry {
	return p.registry
}

// Database returns a sql.Database for the given name. When backed by a
// manager, it looks up the database by name and applies protocol filtering;
// otherwise it falls back to legacy single-database mode.
//
// MySQLSchemaName is reserved and always resolves to the empty stand-in
// schema (see mysql_schema.go), never to a user database: go-mysql-server's
// plan builder resolves it while building every GRANT, REVOKE, and SHOW
// GRANTS statement. CreateDatabase refuses the name for the same reason, so
// the reservation cannot shadow a database a caller was allowed to create.
func (p *Provider) Database(_ *sqle.Context, name string) (sqle.Database, error) {
	if strings.EqualFold(name, MySQLSchemaName) {
		return mysqlSchema{}, nil
	}
	if p.servesPGCatalog() && strings.EqualFold(name, pgcatalog.SchemaName) {
		return pgcatalog.New(), nil
	}
	if p.manager != nil {
		db, err := p.manager.Get(name)
		if err != nil {
			return nil, &DatabaseNotFoundError{Name: name}
		}
		if !p.isAllowed(db) {
			return nil, &DatabaseNotFoundError{Name: name}
		}
		return p.newDatabase(name, db.Engine()), nil
	}
	// Legacy single-database mode.
	if !strings.EqualFold(name, p.dbName) {
		return nil, &DatabaseNotFoundError{Name: name}
	}
	return p.newDatabase(p.dbName, p.engine), nil
}

// servesPGCatalog reports whether this provider serves the PostgreSQL
// system catalogue (see pkg/sql/pgcatalog).
//
// It is gated on the protocol filter rather than served unconditionally so
// that the MySQL endpoint's namespace is untouched: pg_type is a
// PostgreSQL relation, and a MySQL client has no more business resolving
// it than a PostgreSQL client has resolving MySQL's grant tables. The
// pgcatalog relations exist to answer PostgreSQL clients' introspection
// queries, and only the PostgreSQL listener has any.
func (p *Provider) servesPGCatalog() bool {
	return p.protocol == ProtocolPostgreSQL
}

// HasDatabase reports whether the named database exists. When backed by
// a manager, it checks all managed databases; otherwise it checks the
// single legacy database name.
//
// pg_catalog answers true on a PostgreSQL provider for the same reason
// Database resolves it: a schema a caller can select from is a schema that
// exists.
func (p *Provider) HasDatabase(_ *sqle.Context, name string) bool {
	if p.servesPGCatalog() && strings.EqualFold(name, pgcatalog.SchemaName) {
		return true
	}
	if p.manager != nil {
		_, err := p.manager.Get(name)
		return err == nil
	}
	return strings.EqualFold(name, p.dbName)
}

// AllDatabases returns all available databases. When backed by a manager,
// it returns all managed databases filtered by the provider's protocol
// setting; otherwise it returns just the single legacy database.
//
// pg_catalog is deliberately absent, exactly as MySQLSchemaName is: this
// method is what SHOW DATABASES and information_schema enumerate, and
// pg_catalog is an implicit per-session schema qrdb serves so PostgreSQL
// clients can introspect, not a database anything created or can drop.
// Listing it would also make pg_class report on pg_class.
//
// The result is scoped to the session's tenant. The database manager is
// node-local and holds every tenant's databases side by side, so an
// unfiltered enumeration disclosed one tenant's database names to every
// other tenant through SHOW DATABASES, information_schema, and
// pg_catalog.pg_namespace/pg_class -- the last of which also names their
// tables. Data access was never reachable that way (service.Registry
// resolves every read and write against the session's own tenant, so a
// cross-tenant name 404s), but the names themselves were, and a tenant's
// schema is its own business.
func (p *Provider) AllDatabases(ctx *sqle.Context) []sqle.Database {
	if p.manager != nil {
		tenantID := providerSessionTenantID(ctx)
		infos := p.manager.List()
		dbs := make([]sqle.Database, 0, len(infos))
		for _, info := range infos {
			if !strings.EqualFold(databaseOwner(info), tenantID) {
				continue
			}
			if p.protocol != ProtocolAny {
				db, err := p.manager.Get(info.Name)
				if err != nil {
					continue
				}
				if !p.isAllowed(db) {
					continue
				}
			}
			dbs = append(dbs, p.newDatabase(info.Name, info.Engine))
		}
		return dbs
	}
	return []sqle.Database{p.newDatabase(p.dbName, p.engine)}
}

// databaseOwner returns the tenant a managed database belongs to, treating an
// empty TenantID as the system tenant.
//
// The bootstrap databases a node creates before any tenant exists carry no
// TenantID at all. Reading that as "owned by nobody" would hide them from
// every session including the operator's; reading it as "owned by everyone"
// is the disclosure AllDatabases exists to prevent. System-owned is the only
// reading that is both safe and true, and it matches the fallback
// Database.sessionTenantID already applies at the other end of the same
// comparison.
func databaseOwner(info database.DatabaseInfo) string {
	if info.TenantID == "" {
		return tenant.SystemTenantID
	}
	return info.TenantID
}

// providerSessionTenantID returns the tenant of the session driving ctx,
// falling back to the system tenant for a context carrying no QRDB session --
// the same fallback, for the same reason, as Database.sessionTenantID.
func providerSessionTenantID(ctx *sqle.Context) string {
	if ctx == nil || ctx.Session == nil {
		return tenant.SystemTenantID
	}
	if sess, ok := ctx.Session.(*QRDBSession); ok {
		return sess.TenantID()
	}
	return tenant.SystemTenantID
}

// CreateDatabase creates a new database via the manager. The database
// defaults to the KV engine with pebble backend (SQL syntax for engine
// and backend selection is not yet supported). Returns
// MutableOperationNotSupportedError in legacy single-database mode.
func (p *Provider) CreateDatabase(_ *sqle.Context, name string) error {
	if strings.EqualFold(name, MySQLSchemaName) || strings.EqualFold(name, pgcatalog.SchemaName) {
		return &ReservedDatabaseNameError{Name: name}
	}
	if p.manager == nil {
		return &MutableOperationNotSupportedError{Operation: "CREATE DATABASE"}
	}
	cfg := database.DatabaseConfig{
		Name:    name,
		Engine:  database.EngineKV,
		Backend: database.BackendPebble,
	}
	if _, err := p.manager.Create(cfg); err != nil {
		return &CreateDatabaseError{Name: name, Err: err}
	}
	return nil
}

// DropDatabase drops an existing database via the manager. Returns
// MutableOperationNotSupportedError in legacy single-database mode, and
// ReservedDatabaseNameError for the reserved system schemas -- the only
// write against pg_catalog that is syntactically reachable at all, since
// none of its relations implements a write-capability interface.
//
// The SQL context's own context.Context is threaded through to the manager,
// because the drop reclaims the database's keys from the shared store and
// that is a replicated operation. DROP DATABASE over the MySQL and
// PostgreSQL wire protocols reaches the manager directly rather than through
// DatabaseServicer, so passing a background context here would leave the one
// drop path an operator is most likely to use uncancellable.
func (p *Provider) DropDatabase(ctx *sqle.Context, name string) error {
	if strings.EqualFold(name, MySQLSchemaName) || strings.EqualFold(name, pgcatalog.SchemaName) {
		return &ReservedDatabaseNameError{Name: name}
	}
	if p.manager == nil {
		return &MutableOperationNotSupportedError{Operation: "DROP DATABASE"}
	}
	if err := p.manager.Drop(providerContext(ctx), name); err != nil {
		return &DropDatabaseError{Name: name, Err: err}
	}
	return nil
}

// Database is a sql.Database backed by the QRDB KV service. When the
// engine field is set, it tracks which database engine type this database
// uses, allowing engine-specific table exposure in future updates. When
// collectionSvc is set, collection names are dynamically resolved as
// additional SQL tables. When indexSvc is set, KV and collection tables
// use secondary index acceleration for WHERE clause evaluation.
type Database struct {
	name          string
	kvSvc         service.KVServicer
	engine        database.EngineType        // empty string = legacy KV-only
	collectionSvc service.CollectionServicer // nil when collections are not available
	indexSvc      index.IndexServicer        // nil when index acceleration is not available
	readOnly      bool                       // true strips write capability from served tables
	prescopedKV   bool                       // true when kvSvc is already scoped to the caller's namespace
	registry      service.Registry           // nil when relational tables are not available

	// postgres is true when this database is served over the PostgreSQL
	// wire protocol (provider.protocol == ProtocolPostgreSQL). It drives
	// two dialect-specific behaviors: unqualified pg_catalog relations
	// resolve here (see GetTableInsensitive), and relational tables built
	// from this database follow PostgreSQL's TRUNCATE semantics rather
	// than MySQL's (see RelationalTable.Truncate / WithPostgreSQLDialect).
	postgres bool
}

// Name returns the database name.
func (d *Database) Name() string {
	return d.name
}

// Engine returns the database engine type. Returns an empty string for
// legacy databases created without a manager.
func (d *Database) Engine() database.EngineType {
	return d.engine
}

// allTableNames lists all table names served by the database, in
// alphabetical order.
var allTableNames = []string{
	datapointsTableName,
	tableName,
	metricsTableName,
	namespacesTableName,
	vectorsTableName,
}

// hasTable reports whether the given table name is allowed for this
// database's engine type. When engine is empty (legacy), all tables
// are allowed.
func (d *Database) hasTable(name string) bool {
	if d.engine == "" {
		return true
	}
	tables, ok := engineTables[d.engine]
	if !ok {
		return false
	}
	for _, t := range tables {
		if t == name {
			return true
		}
	}
	return false
}

// GetTableInsensitive returns the named table using case-insensitive
// lookup. The KV and entity tables are scoped to namespacePrefix — normally
// the composite (tenant, database) namespace derived from the session's
// tenant and d.name, using the same physical key prefix every other
// protocol uses (see tenant.ComposeNamespace); or the empty string when
// d.kvSvc is already scoped to the caller's namespace (prescopedKV). When
// the database has an engine type set, only tables belonging to that engine
// are accessible. When d.readOnly is set, every returned table has its
// write-capability interfaces stripped (see readOnlyTable).
//
// Resolution order is the PostgreSQL system catalogue (PostgreSQL listener
// only), then static engine tables, then DAO-registered entity types, then
// the relational catalog (see relationalTable), then dynamic collections.
// Relational tables take precedence over collections so that a CREATE TABLE
// deterministically shadows a legacy same-named collection rather than
// silently continuing to serve the collection's inferred document schema.
//
// pg_catalog comes first because PostgreSQL puts it first on every
// session's implicit search_path: an unqualified pg_type arrives here as an
// ordinary lookup in whatever database the session is connected to, and
// resolving it to a user table of that name would be the wrong answer on
// the surface that spelling belongs to. These names are not listed by
// GetTableNames, for the same reason PostgreSQL does not list pg_catalog's
// relations among the current schema's.
func (d *Database) GetTableInsensitive(ctx *sqle.Context, name string) (sqle.Table, bool, error) {
	lower := strings.ToLower(name)

	if d.postgres {
		if tbl, ok := pgcatalog.Relation(lower); ok {
			return tbl, true, nil
		}
	}

	// Check static engine tables first.
	if d.hasTable(lower) {
		switch lower {
		case tableName:
			kv, prefix, err := d.kvService(ctx)
			if err != nil {
				return nil, false, err
			}
			tbl := NewKVTable(kv, prefix, d.indexSvc)
			return d.maybeReadOnly(tbl), true, nil
		case datapointsTableName:
			return NewDatapointsTable(), true, nil
		case metricsTableName:
			return NewMetricsTable(), true, nil
		case namespacesTableName:
			return NewNamespacesTable(), true, nil
		case vectorsTableName:
			return NewVectorsTable(), true, nil
		}
	}

	// Check entity registry for DAO-registered entity types.
	if reg, ok := entity.Global().Get(lower); ok {
		kv, prefix, err := d.kvService(ctx)
		if err != nil {
			return nil, false, err
		}
		tbl := NewEntityTable(kv, prefix, reg, d.indexSvc)
		return d.maybeReadOnly(tbl), true, nil
	}

	// Check the relational catalog for a user-defined table.
	if d.registry != nil {
		tbl, found, err := d.relationalTable(ctx, lower)
		if err != nil {
			return nil, false, err
		}
		if found {
			return d.maybeReadOnly(tbl), true, nil
		}
	}

	// Fall back to dynamic collection lookup. A refused RESOLUTION fails the
	// lookup; a failed LOOKUP means the collection is not there, which is the
	// not-found this function reports by returning found=false.
	collectionSvc, err := d.collectionService(ctx)
	if err != nil {
		return nil, false, err
	}
	if collectionSvc != nil {
		info, infoErr := collectionSvc.GetCollectionInfo(ctx, d.name, lower)
		if infoErr == nil && info != nil {
			tbl := NewCollectionTable(collectionSvc, d.name, info.Name, info.Schema, d.indexSvc)
			return d.maybeReadOnly(tbl), true, nil
		}
	}

	return nil, false, nil
}

// sessionTenantID returns the tenant carried by the QRDBSession installed on
// ctx, or tenant.SystemTenantID when no such session is present. It is a
// thin wrapper over the package-level sessionTenantIDOf, which exists
// because RelationalTable (relational_writer.go's Truncate, which has no
// *Database to call this method on) needs the identical fallback rule.
func (d *Database) sessionTenantID(ctx *sqle.Context) string {
	return sessionTenantIDOf(ctx)
}

// sessionTenantIDOf returns the tenant carried by the QRDBSession installed
// on ctx, or tenant.SystemTenantID when no such session is present. It is
// the single place that fallback is applied, so every tenant-scoped
// resolution path (Database.sessionTenantID, RelationalTable.Truncate)
// agrees on it.
func sessionTenantIDOf(ctx *sqle.Context) string {
	if sess, ok := ctx.Session.(*QRDBSession); ok {
		return sess.TenantID()
	}
	return tenant.SystemTenantID
}

// collectionService returns the CollectionServicer this session's collection
// tables are served from, or a nil servicer with a nil error when collections
// are not available at all.
//
// When a registry is configured it is resolved per (session tenant, database),
// exactly as relationalTable resolves the relational catalog. The
// provider-level d.collectionSvc is a PROCESS-WIDE servicer built over the raw,
// un-namespaced engines, so serving collection tables from it let two tenants
// connected to the same database name read and write each other's documents
// over the MySQL and PostgreSQL wire protocols (go-qrdb#441) -- the relational
// half of the same database was already tenant-scoped, and the collection half
// was not.
//
// It falls back to the provider-level servicer when no registry is configured,
// which is the single-tenant, legacy-provider composition.
//
// A REFUSED resolution is returned, not swallowed. An earlier draft returned a
// nil servicer on error, which turned "this tenant's barrier is sealed" and
// "this database is not yours" into "there is no such table" -- the fail-OPEN
// answer, given by a keyspace that was never consulted, in the one PR whose
// whole subject is not answering for a keyspace you did not resolve. It also
// matches relationalTable one frame above, which propagates its own resolution
// errors and swallows only the specific not-found that means "no such table".
func (d *Database) collectionService(ctx *sqle.Context) (service.CollectionServicer, error) {
	if d.registry == nil {
		return d.collectionSvc, nil
	}
	return d.registry.CollectionService(d.sessionTenantID(ctx), d.name)
}

// kvService returns the KV servicer the kv and entity tables of this session
// are served from, and the key prefix they apply on top of it.
//
// When a registry is configured the servicer is resolved per (session
// tenant, database) on every table lookup, exactly as collectionService and
// relationalTable resolve theirs, and the prefix is empty: the registry has
// already scoped the servicer to the database's keyspace, and resolving it is
// what applies the database's seal, ownership, quota and isolation routing.
// Serving these tables from the provider-level d.kvSvc under a composed
// prefix skipped all four -- a sealed database stayed readable and writable
// over the MySQL and PostgreSQL wire, and a volume- or raft-isolated tenant's
// kv table was served from the shared store instead of its own. A refused
// resolution is returned, never swallowed into "no such table".
//
// Without a registry -- the single-tenant, legacy-provider composition -- the
// provider-level servicer and the composed prefix are used as before.
func (d *Database) kvService(ctx *sqle.Context) (service.KVServicer, string, error) {
	if d.registry == nil {
		return d.kvSvc, d.namespacePrefix(ctx), nil
	}
	kv, err := d.registry.KVService(d.sessionTenantID(ctx), d.name)
	if err != nil {
		return nil, "", err
	}
	return kv, "", nil
}

// namespacePrefix returns the composite key prefix used to scope KV and
// entity table access. When prescopedKV is set, d.kvSvc has already been
// resolved to the caller's (tenant, database) namespace by the caller (e.g.
// via service.Registry.KVService), so no additional prefix is applied and
// bare keys pass straight through. Otherwise the prefix is derived from
// sessionTenantID composed with this database's name.
func (d *Database) namespacePrefix(ctx *sqle.Context) string {
	if d.prescopedKV {
		return ""
	}
	return tenant.ComposeNamespace(d.sessionTenantID(ctx), d.name)
}

// maybeReadOnly wraps t so that write-capability interfaces implemented by
// the underlying concrete table are not reachable when the database is
// configured read-only. Otherwise t is returned unchanged.
func (d *Database) maybeReadOnly(t sqle.Table) sqle.Table {
	if d.readOnly {
		return newReadOnlyTable(t)
	}
	return t
}

// GetTableNames returns available table names. When the database has an
// engine type set, only tables belonging to that engine are returned.
// Legacy databases (empty engine) return all tables.
func (d *Database) GetTableNames(ctx *sqle.Context) ([]string, error) {
	var tables []string
	if d.engine == "" {
		tables = make([]string, len(allTableNames))
		copy(tables, allTableNames)
	} else {
		engineTbls, ok := engineTables[d.engine]
		if !ok {
			tables = nil
		} else {
			tables = make([]string, len(engineTbls))
			copy(tables, engineTbls)
		}
	}

	// Append entity type names from the global entity registry.
	for _, reg := range entity.Global().ListForDatabase(d.name) {
		tables = append(tables, reg.EntityType)
	}
	// Include entities registered without a database scope (legacy mode).
	if d.name != "" {
		for _, reg := range entity.Global().ListForDatabase("") {
			tables = append(tables, reg.EntityType)
		}
	}

	// Append relational table names from the catalog, scoped to the
	// session's tenant exactly as GetTableInsensitive scopes lookups.
	if d.registry != nil {
		if tableSvc, err := d.registry.TableService(d.sessionTenantID(ctx), d.name); err == nil {
			if names, lerr := tableSvc.ListTables(ctx, d.name); lerr == nil {
				tables = append(tables, names...)
			}
		}
	}

	// Append collection names if the service is available. A refused
	// resolution fails the listing rather than returning a silently
	// incomplete one -- see collectionService.
	collectionSvc, err := d.collectionService(ctx)
	if err != nil {
		return nil, err
	}
	if collectionSvc != nil {
		resp, lerr := collectionSvc.ListCollections(ctx, &service.ListCollectionsRequest{
			Database: d.name,
		})
		if lerr == nil {
			for _, col := range resp.Collections {
				tables = append(tables, col.Name)
			}
		}
	}

	return tables, nil
}

// providerContext returns the context.Context a *sqle.Context carries, or
// context.Background() when the engine handed us a nil one. go-mysql-server
// embeds a context.Context in every *sqle.Context, so the fallback is for the
// test and analyser paths that construct one without.
func providerContext(ctx *sqle.Context) context.Context {
	if ctx == nil || ctx.Context == nil {
		return context.Background()
	}
	return ctx.Context
}
