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

package gorm

import (
	"context"
	"database/sql"
	"math"
	"regexp"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"

	qrdbdriver "github.com/jeremyhahn/go-qrdb/sdk/go/driver"
)

// DefaultDriverName is the database/sql driver name this dialector opens
// through sql.Open when Config.DriverName is empty. sdk/go/driver registers
// itself under this name as a side effect of being imported, which this
// package does, so a caller needs no separate blank import to use it.
const DefaultDriverName = "qrdb"

// DefaultDialectName is the value Name reports when Config.DialectName is
// empty. See the package doc comment for why this is "mysql" rather than
// "qrdb".
const DefaultDialectName = "mysql"

// defaultStringSize is the varchar width New and Open apply to
// Config.DefaultStringSize when the caller leaves it unset.
const defaultStringSize = 256

// defaultDatetimePrecision is the fractional-second precision applied to
// DATETIME columns and to Config.NowFunc when the caller does not set
// Config.DefaultDatetimePrecision.
//
// Microseconds, the most MySQL's DATETIME offers and exactly what qrdb
// stores at that width. The precision is load-bearing in two directions and
// 6 is the only value that satisfies both. It must not exceed what the
// column keeps, or Apply's NowFunc hands GORM a timestamp with digits the
// write silently truncates and a read-back comparison fails; and it must
// not be coarser than necessary, or two writes close together round to the
// same instant and a caller that asks "did UpdatedAt change?" is told no.
// At millisecond precision that collision is common enough to be observed
// routinely; at microsecond it takes a thousandfold coincidence.
const defaultDatetimePrecision = 6

// savepointNamePattern matches a bare SQL identifier: a letter or underscore
// followed by letters, digits, or underscores. SavePoint and RollbackTo
// build "SAVEPOINT <name>" and "ROLLBACK TO SAVEPOINT <name>" by string
// concatenation — the statement carries no bind parameter for the name — so
// this is the injection guard for callers that supply their own name instead
// of GORM's generated "sp%d".
var savepointNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Config configures a Dialector. The zero value is not ready to use; build
// one with Open or New, both of which fill in the documented defaults for
// any field the caller leaves unset.
type Config struct {
	// DriverName is the database/sql driver name passed to sql.Open.
	// Defaults to DefaultDriverName ("qrdb").
	DriverName string

	// DSN is the connection string passed to sql.Open, in sdk/go/driver's
	// format. Ignored when Conn is set.
	//
	// Use node:// for anything GORM does: it is the only in-process scheme
	// backed by a relational catalog, so CREATE TABLE — and therefore
	// AutoMigrate — works. It requires the side-effect import
	// _ "github.com/jeremyhahn/go-qrdb/sdk/go/driver/node". embedded:// is
	// deliberately KV-only and fails DDL with
	// *provider.RelationalCatalogUnavailableError; rest:// and grpc:// are a
	// query-only database/sql surface the dialector rejects outright.
	DSN string

	// Conn is a pre-opened connection pool. When set, it is used directly
	// and DSN is ignored, so DSN's embedded/rest/grpc scheme check does not
	// apply — the caller is responsible for choosing a pool whose transport
	// GORM can use.
	Conn gorm.ConnPool

	// DialectName is the value Name reports. Defaults to DefaultDialectName
	// ("mysql"); see the package doc comment for why.
	DialectName string

	// DefaultStringSize is the varchar width used for a String field with no
	// explicit gorm:"size:N" tag. New and Open fill an unset (zero) value in
	// with 256, so the PK/indexed/has-default 191-or-unbounded fallback
	// described on DataTypeOf only applies to a Dialector built by other
	// means with DefaultStringSize left explicitly at zero.
	DefaultStringSize uint

	// DefaultDatetimePrecision is the fractional-second precision applied to
	// DATETIME columns and to the NowFunc installed by Apply. Defaults to a
	// pointer to defaultDatetimePrecision (6). A field's own
	// gorm:"precision:N" tag, or an explicit zero from
	// DisableDatetimePrecision, overrides it.
	DefaultDatetimePrecision *int

	// DisableDatetimePrecision omits the fractional-second precision from
	// DATETIME columns entirely, for engines that predate it.
	DisableDatetimePrecision bool

	// DontSupportRenameIndex makes Migrator.RenameIndex return
	// *RenameIndexNotSupportedError instead of issuing ALTER TABLE ...
	// RENAME INDEX. Defaults to false (Go's zero value; neither New nor Open
	// overrides it): qrdb's engine genuinely accepts ALTER TABLE ... RENAME
	// INDEX (see docs/sql/README.md's "Relational Tables" section), unlike
	// real MySQL/MariaDB versions predating it, which is what this field
	// exists to work around in go-gorm/mysql. Set it true only to reproduce
	// that older-server behavior deliberately.
	DontSupportRenameIndex bool

	// DontSupportRenameColumn makes Migrator.RenameColumn issue MySQL's
	// pre-8.0 ALTER TABLE ... CHANGE ... ... (re-declaring the column's full
	// type) instead of the standard-SQL ALTER TABLE ... RENAME COLUMN ...
	// TO ... the base migrator issues. Both are accepted by qrdb's engine;
	// this defaults to false.
	DontSupportRenameColumn bool

	// DontSupportForShareClause gates the "FOR" ClauseBuilder that rewrites
	// FOR SHARE to LOCK IN SHARE MODE. Defaults to true.
	DontSupportForShareClause bool

	// DontSupportNullAsDefaultValue makes Migrator.ColumnTypes clear a
	// column's reported default value when information_schema reports it as
	// the literal string "NULL" rather than a real SQL NULL, matching
	// go-gorm/mysql's own MariaDB-oriented rewrite. Defaults to false.
	DontSupportNullAsDefaultValue bool

	// DontSupportDropConstraint is unused by Migrator: qrdb's go-mysql-server
	// engine resolves a generic "ALTER TABLE ... DROP CONSTRAINT name" to
	// whichever concrete constraint kind name names, so the base migrator's
	// standard-SQL DropConstraint always applies and this field's MariaDB-
	// oriented per-kind (DROP INDEX/DROP CHECK/DROP FOREIGN KEY) fallback is
	// never needed. Kept for API parity with go-gorm/mysql's Config.
	DontSupportDropConstraint bool

	// DisableForeignKeyConstraintWhenMigrating is NOT read by anything in
	// this package today — it is kept only for field-for-field parity with
	// go-gorm/mysql's identically-named Config field, which is itself
	// unread there too. The field that actually controls whether AutoMigrate
	// attempts a relationship-derived foreign key is gorm.Config's own
	// DisableForeignKeyConstraintWhenMigrating (gorm.io/gorm/migrator.
	// Migrator.AutoMigrate checks m.DB.DisableForeignKeyConstraintWhenMigrating,
	// i.e. the *gorm.DB's top-level config, not this Dialector's) — set that
	// one directly on the *gorm.Config passed to gorm.Open, exactly as any
	// other GORM dialect's caller would.
	//
	// qrdb's relational tables implement go-mysql-server's
	// sql.ForeignKeyTable (see docs/sql/README.md), so leaving gorm.Config's
	// flag at its own default — false, meaning AutoMigrate DOES attempt
	// relationship constraints — is the right choice: a foreign key is an
	// ordinary statement the engine resolves and enforces, and neither
	// CreateConstraint nor DropConstraint treats it specially.
	DisableForeignKeyConstraintWhenMigrating bool

	// SkipInitializeWithVersion skips the SELECT VERSION() connectivity
	// check Initialize otherwise performs. qrdb's SQL engine is uniformly
	// go-mysql-server, so unlike go-gorm/mysql the result is never used to
	// branch dialect behavior by server version — the query exists only to
	// fail Initialize fast against an unreachable or misconfigured server
	// instead of deferring that failure to the first real statement.
	SkipInitializeWithVersion bool
}

// Dialector implements gorm.Dialector for qrdb. Its SQL surface is qrdb's
// go-mysql-server engine, so its clause rewriting, quoting, data-type
// mapping, and error translation all follow MySQL semantics; see the package
// doc comment for why Name nonetheless defaults to "mysql".
type Dialector struct {
	*Config
}

// Compile-time interface assertions.
var (
	_ gorm.Dialector                     = Dialector{}
	_ gorm.ErrorTranslator               = Dialector{}
	_ gorm.SavePointerDialectorInterface = Dialector{}
)

// Open returns a Dialector that connects using dsn. It is equivalent to
// New(Config{DSN: dsn}).
func Open(dsn string) gorm.Dialector {
	return New(Config{DSN: dsn})
}

// New returns a Dialector built from cfg, with every field cfg leaves at its
// zero value set to the default documented on Config.
func New(cfg Config) gorm.Dialector {
	if cfg.DriverName == "" {
		cfg.DriverName = DefaultDriverName
	}
	if cfg.DialectName == "" {
		cfg.DialectName = DefaultDialectName
	}
	if cfg.DefaultStringSize == 0 {
		cfg.DefaultStringSize = defaultStringSize
	}
	if cfg.DefaultDatetimePrecision == nil {
		precision := defaultDatetimePrecision
		cfg.DefaultDatetimePrecision = &precision
	}

	return Dialector{Config: &cfg}
}

// Name returns the dialect GORM should treat this connection as speaking:
// Config.DialectName, or DefaultDialectName ("mysql") when unset. See the
// package doc comment for why the default is "mysql" rather than "qrdb".
func (dialector Dialector) Name() string {
	if dialector.Config != nil && dialector.Config.DialectName != "" {
		return dialector.Config.DialectName
	}
	return DefaultDialectName
}

// nowFunc returns a function that reports the current time rounded to n
// fractional-second digits, matching the precision DATETIME columns are
// created with so a round-tripped time.Time compares equal to what was
// stored.
func nowFunc(n int) func() time.Time {
	round := time.Second / time.Duration(math.Pow10(n))
	return func() time.Time {
		return time.Now().Round(round)
	}
}

// Apply installs a NowFunc on config that rounds to
// Config.DefaultDatetimePrecision, unless the caller already supplied one.
// Without this, comparisons between a freshly-set time.Time and the same
// value read back from a datetime(3) column fail on sub-millisecond
// remainders that MySQL's DATETIME truncated away on write.
func (dialector Dialector) Apply(config *gorm.Config) error {
	if config.NowFunc != nil {
		return nil
	}

	precision := defaultDatetimePrecision
	if dialector.Config != nil && dialector.Config.DefaultDatetimePrecision != nil {
		precision = *dialector.Config.DefaultDatetimePrecision
	}
	config.NowFunc = nowFunc(precision)
	return nil
}

// Initialize opens the connection pool and registers qrdb's callbacks and
// clause builders on db.
//
// When Config.Conn is unset, the DSN is parsed and rejected with
// *UnsupportedTransportError before sql.Open is attempted if its scheme is
// "rest" or "grpc" — see UnsupportedTransportError for why those transports
// cannot back a GORM connection.
func (dialector Dialector) Initialize(db *gorm.DB) error {
	if dialector.Conn != nil {
		db.ConnPool = dialector.Conn
	} else {
		if dsnCfg, err := qrdbdriver.ParseDSN(dialector.DSN); err != nil {
			return err
		} else if dsnCfg.Scheme == qrdbdriver.SchemeREST || dsnCfg.Scheme == qrdbdriver.SchemeGRPC {
			return &UnsupportedTransportError{Scheme: dsnCfg.Scheme}
		}

		driverName := dialector.DriverName
		if driverName == "" {
			driverName = DefaultDriverName
		}

		pool, err := sql.Open(driverName, dialector.DSN)
		if err != nil {
			return err
		}
		db.ConnPool = pool
	}

	if !dialector.SkipInitializeWithVersion {
		// A connectivity check only: qrdb's SQL engine is uniformly
		// go-mysql-server, so the version string is not used to branch
		// dialect behavior the way go-gorm/mysql branches on real MySQL
		// 5.x/MariaDB. Failing here surfaces a bad DSN or unreachable
		// server from Initialize rather than from the first real query.
		var version string
		if err := db.ConnPool.QueryRowContext(context.Background(), "SELECT VERSION()").Scan(&version); err != nil {
			return err
		}
	}

	callbacks.RegisterDefaultCallbacks(db, &callbacks.Config{
		CreateClauses: []string{"INSERT", "VALUES", "ON CONFLICT"},
		UpdateClauses: []string{"UPDATE", "SET", "WHERE", "ORDER BY", "LIMIT"},
		DeleteClauses: []string{"DELETE", "FROM", "WHERE", "ORDER BY", "LIMIT"},
	})

	for name, builder := range dialector.ClauseBuilders() {
		if _, exists := db.ClauseBuilders[name]; !exists {
			db.ClauseBuilders[name] = builder
		}
	}

	return nil
}

// BindVarTo writes "?", the positional placeholder qrdb's go-mysql-server
// engine and sdk/go/driver both expect.
func (dialector Dialector) BindVarTo(writer clause.Writer, stmt *gorm.Statement, v interface{}) {
	writer.WriteByte('?')
}

// QuoteTo writes str to writer as a backtick-quoted identifier, splitting on
// "." so that "a.b" becomes `a`.`b` rather than a single quoted string.
//
// This is copied verbatim from go-gorm/mysql's QuoteTo state machine, which
// correctly handles a bare identifier, a dotted "table.column" path,
// embedded backticks, and an identifier the caller already quoted; a naive
// wrap-in-backticks implementation fails on all but the first case.
func (dialector Dialector) QuoteTo(writer clause.Writer, str string) {
	var (
		underQuoted, selfQuoted bool
		continuousBacktick      int8
		shiftDelimiter          int8
	)

	for _, v := range []byte(str) {
		switch v {
		case '`':
			continuousBacktick++
			if continuousBacktick == 2 {
				writer.WriteString("``")
				continuousBacktick = 0
			}
		case '.':
			if continuousBacktick > 0 || !selfQuoted {
				shiftDelimiter = 0
				underQuoted = false
				continuousBacktick = 0
				writer.WriteByte('`')
			}
			writer.WriteByte(v)
			continue
		default:
			if shiftDelimiter-continuousBacktick <= 0 && !underQuoted {
				writer.WriteByte('`')
				underQuoted = true
				if selfQuoted = continuousBacktick > 0; selfQuoted {
					continuousBacktick -= 1
				}
			}

			for ; continuousBacktick > 0; continuousBacktick -= 1 {
				writer.WriteString("``")
			}

			writer.WriteByte(v)
		}
		shiftDelimiter++
	}

	if continuousBacktick > 0 && !selfQuoted {
		writer.WriteString("``")
	}
	writer.WriteByte('`')
}

// DefaultValueOf returns the expression GORM writes for a column omitted
// from an INSERT's VALUES list so that all rows in a multi-row insert keep
// the same column count: the literal SQL keyword DEFAULT.
func (dialector Dialector) DefaultValueOf(field *schema.Field) clause.Expression {
	return clause.Expr{SQL: "DEFAULT"}
}

// Explain renders sql with vars substituted in, for logging. It delegates to
// gorm's logger.ExplainSQL using a single-quote string escaper, matching
// MySQL string literal syntax.
func (dialector Dialector) Explain(sql string, vars ...interface{}) string {
	return logger.ExplainSQL(sql, nil, "'", vars...)
}

// SavePoint issues SAVEPOINT name on tx.
//
// name is validated against savepointNamePattern before being concatenated
// into the statement text; see InvalidSavePointNameError for why.
func (dialector Dialector) SavePoint(tx *gorm.DB, name string) error {
	if !savepointNamePattern.MatchString(name) {
		return &InvalidSavePointNameError{Name: name}
	}
	return tx.Exec("SAVEPOINT " + name).Error
}

// RollbackTo issues ROLLBACK TO SAVEPOINT name on tx.
//
// name is validated against savepointNamePattern before being concatenated
// into the statement text; see InvalidSavePointNameError for why.
func (dialector Dialector) RollbackTo(tx *gorm.DB, name string) error {
	if !savepointNamePattern.MatchString(name) {
		return &InvalidSavePointNameError{Name: name}
	}
	return tx.Exec("ROLLBACK TO SAVEPOINT " + name).Error
}
