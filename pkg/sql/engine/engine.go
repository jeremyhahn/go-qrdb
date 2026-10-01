// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package engine provides a shared SQL execution engine backed by
// go-mysql-server. It is used by both the MySQL and PostgreSQL wire
// protocol implementations to parse, analyze, and execute SQL queries
// against the QRDB storage layer.
package engine

import (
	sqle "github.com/dolthub/go-mysql-server"
	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/analyzer"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/pgcatalog"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/privilege"
)

// Dialect selects the SQL dialect an engine presents to its clients. It is
// a property of the wire protocol front end that builds the engine, not of
// the data underneath: the same provider, tables, and rows are served
// either way, and only the metadata a client introspects them through
// differs.
type Dialect string

const (
	// DialectMySQL serves go-mysql-server's own metadata unchanged. It is
	// the zero value, so an engine built without naming a dialect is the
	// MySQL engine that has always existed.
	DialectMySQL Dialect = ""

	// DialectPostgreSQL additionally serves the PostgreSQL system
	// catalogue a PostgreSQL client introspects a schema through -- see
	// pkg/sql/pgcatalog.
	DialectPostgreSQL Dialect = "postgresql"
)

// infoSchemaByDialect maps a dialect to the information_schema database an
// engine of that dialect serves. A dialect absent from the map keeps
// analyzer.NewCatalog's default, which is why DialectMySQL has no entry:
// the MySQL endpoint's information_schema must remain byte-for-byte what
// go-mysql-server ships.
var infoSchemaByDialect = map[Dialect]func() sql.Database{
	DialectPostgreSQL: pgcatalog.InfoSchema,
}

// functionsByDialect maps a dialect to the SQL functions that dialect serves
// beyond go-mysql-server's built-ins. Registration is per-CATALOG, exactly
// like infoSchemaByDialect above, so a PostgreSQL engine's extra functions are
// invisible to the MySQL engine built alongside it -- these are PostgreSQL
// system-catalog functions, and teaching them to the MySQL endpoint would be
// wrong rather than merely untidy.
//
// format_type is not optional for the PostgreSQL wire: gorm.io/driver/postgres's
// Migrator resolves every column's type with
//
//	SELECT a.attname, format_type(a.atttypid, a.atttypmod) FROM pg_attribute a ...
//
// so without it AutoMigrate cannot read a table's columns at all. Losing this
// registration produced `function: 'format_type' not found` on 78 of the pgsql
// conformance failures -- the single largest cluster -- because every
// migration that reached column introspection died there.
var functionsByDialect = map[Dialect][]sql.Function{
	DialectPostgreSQL: {
		pgcatalog.NewFormatType(),
		// Random-UUID generation under both PostgreSQL spellings --
		// gorm's uuid-typed primary keys default to uuid_generate_v4()
		// (via CREATE EXTENSION "uuid-ossp", a no-op on this wire) or
		// gen_random_uuid().
		pgcatalog.NewGenRandomUUID(),
		pgcatalog.NewUUIDGenerateV4(),
	},
}

// Config holds configuration for the SQL engine.
type Config struct {
	// Provider is the QRDB database provider that supplies table and
	// schema metadata to go-mysql-server.
	Provider sql.DatabaseProvider

	// Dialect selects the SQL dialect the engine presents. The zero value
	// is DialectMySQL.
	Dialect Dialect
}

// New creates a new go-mysql-server Engine backed by the given database
// provider. The provider supplies catalog, schema, and table information
// so that go-mysql-server can parse, analyze, and execute SQL queries.
//
// The analyzer is built from go-mysql-server's default rule set plus one
// qrdb post-analyze rule, privilege.Rewrite, which replaces the GRANT,
// REVOKE, and SHOW GRANTS plan nodes with nodes backed by qrdb's own
// per-database privilege catalog. Without it those statements reach
// go-mysql-server's execution builder, which hard-asserts the `mysql` schema
// to *mysql_db.MySQLDb -- grant tables qrdb does not implement. Every other
// batch is go-mysql-server's default, so this is additive: no default rule
// is removed, reordered, or replaced.
//
// Note for anyone adding a rule here: the post-analyze batch is NOT reached
// by every statement. go-mysql-server short-circuits an INSERT whose source
// is a VALUES list of literals to a reduced, hard-coded batch list that
// omits the integrator batches entirely (sql/analyzer/node_batches.go,
// getBatchesForNode, keyed on plan.InsertInto.LiteralValueSource); the same
// is true of a single-relation UPDATE. A rule that must run for those
// statements cannot be installed through this extension point at all, so
// verify a new rule actually fires for the statements it targets rather than
// assuming registration is enough.
//
// Enforcement of the privileges the rule administers is installed
// separately, and process-wide, by pkg/sql/privilege's init (see
// sql.SetAuthorizationHandlerFactory); importing the package here is what
// guarantees that init has run before any engine exists.
//
// When cfg.Dialect names a dialect with its own information_schema (see
// infoSchemaByDialect), the catalog's InfoSchema is replaced after the
// analyzer is built. That field is the documented seam for exactly this --
// analyzer.NewCatalog assigns go-mysql-server's default into it and nothing
// else reads it before a query arrives -- and it is per-catalog, so a
// PostgreSQL engine's information_schema is invisible to the MySQL engine
// built alongside it. Both wire protocol servers build their own engine
// (see pkg/sql/{mysql,pgsql}.Server.Start), so this is a genuine per-
// endpoint choice rather than a process-wide one.
func New(cfg Config) *sqle.Engine {
	a := analyzer.NewBuilder(cfg.Provider).
		AddPostAnalyzeRule(privilege.RewriteRuleID, privilege.Rewrite).
		AddOverrides(sql.EngineOverrides{Builder: sql.BuilderOverrides{
			// Repair the bind-time HAVING-subquery aggregation collision on
			// every entry path; see having_subquery.go.
			Parser: newLockClauseParser(newHavingSubqueryParser(sql.NewMysqlParser())),
		}}).
		Build()
	if newInfoSchema, ok := infoSchemaByDialect[cfg.Dialect]; ok {
		a.Catalog.InfoSchema = newInfoSchema()
	}
	for _, fn := range functionsByDialect[cfg.Dialect] {
		a.Catalog.RegisterFunction(sql.NewEmptyContext(), fn)
	}
	// nextval is registered for EVERY dialect, unlike the map above,
	// because it can be baked into a table: a serial column translated on
	// the PostgreSQL wire carries DEFAULT (nextval('...')), and an INSERT
	// into that same table arriving over the MySQL wire must evaluate the
	// default rather than fail on an unknown function. (MariaDB ships the
	// same function for its own sequences, so the name is not foreign to
	// the MySQL dialect either.) It is built here rather than in
	// functionsByDialect because it closes over the provider.
	a.Catalog.RegisterFunction(sql.NewEmptyContext(), newNextvalFunction(cfg.Provider))
	// Repair the two upstream DML-RETURNING defects through the exec
	// builder's priority hook; see dml_returning.go. Installed for every
	// dialect: the broken shapes are the MySQL-dialect statements the
	// PostgreSQL translator emits, and the MySQL wire serves them too.
	a.ExecBuilder.PriorityBuilder = &dmlReturningBuilder{base: a.ExecBuilder}
	return sqle.New(a, nil)
}
