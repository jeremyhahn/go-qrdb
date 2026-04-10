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

package vtab

import (
	"context"
	"database/sql"
	"strings"
)

// TableType identifies the kind of virtual table that a TableMeta describes.
// Each constant corresponds to a distinct qrdb data model with a matching
// SQLite virtual table module name.
type TableType uint8

const (
	// TableKV is a flat key-value namespace backed by the qrdb_kv module.
	TableKV TableType = iota

	// TableEntity is a structured entity table backed by the qrdb_entity module.
	TableEntity

	// TableCollection is a document-oriented collection backed by qrdb_collection.
	TableCollection

	// TableTimeSeries is a time-series dataset backed by qrdb_datapoints.
	TableTimeSeries

	// TableVector is a vector-search namespace backed by qrdb_vectors.
	TableVector
)

// Unexported aliases kept for internal package use to avoid churn across the
// existing vtab sub-files that already reference the lowercase constants.
const (
	tableKV         = TableKV
	tableEntity     = TableEntity
	tableCollection = TableCollection
	tableTimeSeries = TableTimeSeries
	tableVector     = TableVector
)

// tableType is an unexported alias of TableType retained for internal use.
type tableType = TableType

// ColumnMeta describes a single column in a discoverable table.
type ColumnMeta struct {
	// Name is the column name as it will appear in SQLite.
	Name string

	// SQLiteType is one of the SQLite type affinity keywords:
	// TEXT, INTEGER, REAL, BLOB, or NUMERIC.
	SQLiteType string

	// PrimaryKey is true when this column is (part of) the table's primary key.
	PrimaryKey bool

	// Nullable is true when the column may hold NULL.
	Nullable bool
}

// columnMeta is an unexported alias retained for internal package use.
type columnMeta = ColumnMeta

// IndexMeta describes a server-side index that can accelerate queries on the
// virtual table.
type IndexMeta struct {
	// Name is the index identifier as returned by the server.
	Name string

	// FieldName is the column or entity field the index covers.
	FieldName string

	// Unique is true when the index enforces a uniqueness constraint.
	Unique bool
}

// indexMeta is an unexported alias retained for internal package use.
type indexMeta = IndexMeta

// TableMeta carries all metadata needed to generate a CREATE VIRTUAL TABLE
// statement for one discoverable qrdb table. It is used both by the vtab
// package internally and by the main package when building per-table adapters
// before registering the SQLite driver.
type TableMeta struct {
	// Name is the SQLite table name that will be used in DDL (e.g. "kv", "users").
	Name string

	// Type identifies the qrdb data model backing this table.
	Type TableType

	// Database is the qrdb database scope for multi-database deployments.
	// An empty string means the default database.
	Database string

	// Columns is the ordered list of column definitions.
	Columns []ColumnMeta

	// Indexes is the list of available server-side indexes.
	Indexes []IndexMeta
}

// tableMeta is an unexported alias of TableMeta retained so that existing
// internal code that uses tableMeta continues to compile without modification.
type tableMeta = TableMeta

// MetadataProvider fetches table metadata from a qrdb server.
// The main package satisfies this interface by wrapping the SDK client.
type MetadataProvider interface {
	// DiscoverTables returns metadata for every table available in the
	// connected qrdb instance. Implementations must respect context
	// cancellation and deadlines.
	DiscoverTables(ctx context.Context) ([]TableMeta, error)
}

// metadataProvider is an unexported adapter interface that maps to
// MetadataProvider. It exists so that existing internal code (tests, helpers)
// that calls discoverTables continues to compile without modification.
type metadataProvider interface {
	DiscoverTables(ctx context.Context) ([]TableMeta, error)
}

// moduleNameForType maps a TableType to its SQLite virtual-table module name.
// All module names are registered before discovery runs.
var moduleNameForType = map[TableType]string{
	TableKV:         "qrdb_kv",
	TableEntity:     "qrdb_entity",
	TableCollection: "qrdb_collection",
	TableTimeSeries: "qrdb_datapoints",
	TableVector:     "qrdb_vectors",
}

// DiscoverAndRegister fetches table metadata from provider and executes a
// CREATE VIRTUAL TABLE statement for every discovered table against db.
//
// It is called once during shell initialisation, after all virtual-table
// modules have been registered with the SQLite driver via the ConnectHook.
// The returned slice contains the names of every table that was successfully
// created; it is empty (not nil) when no tables are discovered.
//
// An error from provider.DiscoverTables is wrapped in a DiscoveryError and
// returned immediately. Errors from individual DDL executions are also
// wrapped and returned; tables created before the failure remain registered.
func DiscoverAndRegister(ctx context.Context, db *sql.DB, provider MetadataProvider) ([]string, error) {
	return discoverAndRegister(ctx, db, provider)
}

// discoverAndRegister is the package-internal implementation called by
// DiscoverAndRegister and by tests within this package.
func discoverAndRegister(ctx context.Context, db *sql.DB, provider metadataProvider) ([]string, error) {
	tables, err := provider.DiscoverTables(ctx)
	if err != nil {
		return nil, &DiscoveryError{Cause: err}
	}

	created := make([]string, 0, len(tables))

	for _, meta := range tables {
		ddl := generateCreateVTable(meta)
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			return created, &DiscoveryError{Cause: err}
		}
		created = append(created, meta.Name)
	}

	return created, nil
}

// generateCreateVTable produces a CREATE VIRTUAL TABLE IF NOT EXISTS statement
// for the given TableMeta. The statement format varies by table type:
//
//   - KV:         CREATE VIRTUAL TABLE IF NOT EXISTS <name> USING qrdb_kv
//   - Entity:     CREATE VIRTUAL TABLE IF NOT EXISTS <name> USING qrdb_entity(entityType=<name>)
//   - Collection: CREATE VIRTUAL TABLE IF NOT EXISTS <name> USING qrdb_collection(database=<db>,collection=<name>)
//   - TimeSeries: CREATE VIRTUAL TABLE IF NOT EXISTS <name> USING qrdb_datapoints
//   - Vector:     CREATE VIRTUAL TABLE IF NOT EXISTS <name> USING qrdb_vectors
func generateCreateVTable(meta TableMeta) string {
	module := moduleNameForType[meta.Type]

	var b strings.Builder
	b.WriteString("CREATE VIRTUAL TABLE IF NOT EXISTS ")
	b.WriteString(meta.Name)
	b.WriteString(" USING ")
	b.WriteString(module)

	switch meta.Type {
	case TableEntity:
		b.WriteString("(entityType=")
		b.WriteString(meta.Name)
		b.WriteByte(')')

	case TableCollection:
		b.WriteString("(")
		if meta.Database != "" {
			b.WriteString("database=")
			b.WriteString(meta.Database)
			b.WriteString(",")
		}
		b.WriteString("collection=")
		b.WriteString(meta.Name)
		b.WriteByte(')')
	}

	return b.String()
}

// sqliteTypeFromMeta returns the SQLite type affinity string for col.
// When the column already carries an explicit SQLiteType it is returned
// unchanged. An empty SQLiteType defaults to TEXT, which is the most
// permissive SQLite affinity and correct for untyped KV values.
func sqliteTypeFromMeta(col ColumnMeta) string {
	switch strings.ToUpper(col.SQLiteType) {
	case "INTEGER", "INT":
		return "INTEGER"
	case "REAL", "FLOAT", "DOUBLE":
		return "REAL"
	case "BLOB":
		return "BLOB"
	case "NUMERIC":
		return "NUMERIC"
	default:
		return "TEXT"
	}
}
