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
// use by the Wails SQL binding and similar embedded contexts.
func NewProviderWithEngine(name string, kvSvc service.KVServicer, engine database.EngineType) *Provider {
	return &Provider{
		dbName: name,
		kvSvc:  kvSvc,
		engine: engine,
	}
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
		return &Database{name: name, kvSvc: p.kvSvc, engine: db.Engine(), collectionSvc: p.collectionSvc, indexSvc: p.indexSvc}, nil
	}
	// Legacy single-database mode.
	if !strings.EqualFold(name, p.dbName) {
		return nil, &DatabaseNotFoundError{Name: name}
	}
	return &Database{name: p.dbName, kvSvc: p.kvSvc, engine: p.engine, collectionSvc: p.collectionSvc, indexSvc: p.indexSvc}, nil
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
			dbs = append(dbs, &Database{
				name:          info.Name,
				kvSvc:         p.kvSvc,
				engine:        info.Engine,
				collectionSvc: p.collectionSvc,
				indexSvc:      p.indexSvc,
			})
		}
		return dbs
	}
	return []sqle.Database{
		&Database{name: p.dbName, kvSvc: p.kvSvc, engine: p.engine, collectionSvc: p.collectionSvc, indexSvc: p.indexSvc},
	}
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
// lookup. The KV table is scoped to the tenant from the session context.
// When the database has an engine type set, only tables belonging to
// that engine are accessible.
func (d *Database) GetTableInsensitive(ctx *sqle.Context, name string) (sqle.Table, bool, error) {
	lower := strings.ToLower(name)

	// Check static engine tables first.
	if d.hasTable(lower) {
		switch lower {
		case tableName:
			tenantID := tenant.SystemTenantID
			if sess, ok := ctx.Session.(*QRDBSession); ok {
				tenantID = sess.TenantID()
			}
			return NewKVTable(d.kvSvc, tenantID, d.indexSvc), true, nil
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
		tenantID := tenant.SystemTenantID
		if sess, ok := ctx.Session.(*QRDBSession); ok {
			tenantID = sess.TenantID()
		}
		return NewEntityTable(d.kvSvc, tenantID, reg, d.indexSvc), true, nil
	}

	// Fall back to dynamic collection lookup.
	if d.collectionSvc != nil {
		info, err := d.collectionSvc.GetCollectionInfo(ctx, d.name, lower)
		if err == nil && info != nil {
			return NewCollectionTable(d.collectionSvc, d.name, info.Name, info.Schema, d.indexSvc), true, nil
		}
	}

	return nil, false, nil
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
