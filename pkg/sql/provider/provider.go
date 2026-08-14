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
	"strings"

	sqle "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/entity"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
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
	}
	if p.readOnly {
		return newReadOnlyDatabase(db)
	}
	return db
}

// Database returns a sql.Database for the given name. When backed by a
// manager, it looks up the database by name and applies protocol filtering;
// otherwise it falls back to legacy single-database mode.
func (p *Provider) Database(_ *sqle.Context, name string) (sqle.Database, error) {
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

// HasDatabase reports whether the named database exists. When backed by
// a manager, it checks all managed databases; otherwise it checks the
// single legacy database name.
func (p *Provider) HasDatabase(_ *sqle.Context, name string) bool {
	if p.manager != nil {
		_, err := p.manager.Get(name)
		return err == nil
	}
	return strings.EqualFold(name, p.dbName)
}

// AllDatabases returns all available databases. When backed by a manager,
// it returns all managed databases filtered by the provider's protocol
// setting; otherwise it returns just the single legacy database.
func (p *Provider) AllDatabases(_ *sqle.Context) []sqle.Database {
	if p.manager != nil {
		infos := p.manager.List()
		dbs := make([]sqle.Database, 0, len(infos))
		for _, info := range infos {
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

// CreateDatabase creates a new database via the manager. The database
// defaults to the KV engine with pebble backend (SQL syntax for engine
// and backend selection is not yet supported). Returns
// MutableOperationNotSupportedError in legacy single-database mode.
func (p *Provider) CreateDatabase(_ *sqle.Context, name string) error {
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
// MutableOperationNotSupportedError in legacy single-database mode.
func (p *Provider) DropDatabase(_ *sqle.Context, name string) error {
	if p.manager == nil {
		return &MutableOperationNotSupportedError{Operation: "DROP DATABASE"}
	}
	if err := p.manager.Drop(name); err != nil {
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
// Resolution order is static engine tables, then DAO-registered entity
// types, then the relational catalog (see relationalTable), then dynamic
// collections. Relational tables take precedence over collections so that a
// CREATE TABLE deterministically shadows a legacy same-named collection
// rather than silently continuing to serve the collection's inferred
// document schema.
func (d *Database) GetTableInsensitive(ctx *sqle.Context, name string) (sqle.Table, bool, error) {
	lower := strings.ToLower(name)

	// Check static engine tables first.
	if d.hasTable(lower) {
		switch lower {
		case tableName:
			tbl := NewKVTable(d.kvSvc, d.namespacePrefix(ctx), d.indexSvc)
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
		tbl := NewEntityTable(d.kvSvc, d.namespacePrefix(ctx), reg, d.indexSvc)
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

	// Fall back to dynamic collection lookup.
	if d.collectionSvc != nil {
		info, err := d.collectionSvc.GetCollectionInfo(ctx, d.name, lower)
		if err == nil && info != nil {
			tbl := NewCollectionTable(d.collectionSvc, d.name, info.Name, info.Schema, d.indexSvc)
			return d.maybeReadOnly(tbl), true, nil
		}
	}

	return nil, false, nil
}

// sessionTenantID returns the tenant carried by the QRDBSession installed on
// ctx, or tenant.SystemTenantID when no such session is present. It is the
// single place that fallback is applied, so every tenant-scoped resolution
// path (namespacePrefix, relationalTable) agrees on it.
func (d *Database) sessionTenantID(ctx *sqle.Context) string {
	if sess, ok := ctx.Session.(*QRDBSession); ok {
		return sess.TenantID()
	}
	return tenant.SystemTenantID
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

	// Append collection names if the service is available.
	if d.collectionSvc != nil {
		resp, err := d.collectionSvc.ListCollections(ctx, &service.ListCollectionsRequest{
			Database: d.name,
		})
		if err == nil {
			for _, col := range resp.Collections {
				tables = append(tables, col.Name)
			}
		}
	}

	return tables, nil
}
