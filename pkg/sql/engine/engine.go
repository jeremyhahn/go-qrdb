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
		Build()
	if newInfoSchema, ok := infoSchemaByDialect[cfg.Dialect]; ok {
		a.Catalog.InfoSchema = newInfoSchema()
	}
	return sqle.New(a, nil)
}
