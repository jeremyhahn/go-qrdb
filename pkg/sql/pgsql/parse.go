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

//go:build qrdb_pgsql

package pgsql

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	sqle "github.com/dolthub/go-mysql-server"
	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/dolthub/vitess/go/vt/sqlparser"
	"github.com/jackc/pgx/v5/pgtype"
	wire "github.com/jeroenrinzema/psql-wire"
	psqlerr "github.com/jeroenrinzema/psql-wire/errors"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/catalog"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// paramTimeLayout is the textual form a decoded time.Time bind parameter is
// re-encoded as. It mirrors timeBindLayout in sdk/go/driver/bindings.go: the
// MySQL DATETIME literal format at microsecond precision, the widest
// precision MySQL DATETIME supports, so no in-range timestamp loses
// fidelity on the way in. The two packages sit in different architectural
// layers (SDK vs. core) and deliberately do not import one another, so the
// constant is duplicated rather than shared.
const paramTimeLayout = "2006-01-02 15:04:05.999999"

// noopStatements are SQL statements that are handled as no-ops because they
// are PG session management commands that have no equivalent in GMS.
//
// BEGIN/COMMIT/ROLLBACK/END are deliberately NOT here: go-mysql-server's
// vitess-derived parser natively accepts BEGIN, COMMIT, and ROLLBACK; END
// (and END WORK / END TRANSACTION), the one PG spelling it does not accept,
// is rewritten to COMMIT before it ever reaches the parser -- see
// rewriteEndAsCommit below. QRDBSession now implements
// sql.TransactionSession (see pkg/sql/provider/session.go), so routing
// these statements through to the engine instead of swallowing them here is
// what makes interactive transactions actually take effect over the
// PostgreSQL wire protocol.
var noopStatements = map[string]bool{}

// endStatementRe matches PostgreSQL's END, END WORK, and END TRANSACTION --
// all aliases for COMMIT that MySQL's (and therefore go-mysql-server's)
// parser does not accept as a bare statement. rewriteEndAsCommit uses this
// to translate the statement text itself before it ever reaches the
// engine, rather than teaching the engine a new spelling of COMMIT.
var endStatementRe = regexp.MustCompile(`(?i)^END(\s+(WORK|TRANSACTION))?\s*;?\s*$`)

// rewriteEndAsCommit rewrites trimmed to "COMMIT" when it is exactly
// PostgreSQL's END/END WORK/END TRANSACTION alias for COMMIT, and returns
// trimmed unchanged otherwise.
func rewriteEndAsCommit(trimmed string) string {
	if endStatementRe.MatchString(trimmed) {
		return "COMMIT"
	}
	return trimmed
}

// noopPrefixes are SQL statement prefixes that are handled as no-ops because
// they are PG session configuration commands.
var noopPrefixes = []string{
	"SET ",
	"SHOW ",
	"RESET ",
	"DISCARD ",
	"DEALLOCATE ",
	"LISTEN ",
	"UNLISTEN ",
}

// ParseHandler handles SQL query parsing and execution for the PG wire
// protocol. It receives raw SQL from the PG client, translates PG syntax
// to GMS-compatible SQL, executes through the shared engine, and returns
// results as psql-wire PreparedStatements.
type ParseHandler struct {
	sqlEngine  *sqle.Engine
	translator *engine.Translator
	dbName     string
	// sealedProbe gates query execution while the storage barrier is
	// sealed. Nil disables the gate.
	sealedProbe sealedWindowProbe
}

// NewParseHandler creates a new ParseHandler.
func NewParseHandler(sqlEngine *sqle.Engine, translator *engine.Translator, dbName string) *ParseHandler {
	return &ParseHandler{
		sqlEngine:  sqlEngine,
		translator: translator,
		dbName:     dbName,
	}
}

// HandleParse implements the psql-wire ParseFn signature. It receives raw
// SQL from the PG client, translates PG syntax to GMS, executes through
// the shared engine, and returns results as PreparedStatements.
//
// Parameterized queries (the original PG text contains $N placeholders) are
// deferred: the translated SQL is captured at PARSE time but execution is
// delayed until the BIND/EXECUTE phase when parameter values arrive.
// Non-parameterized queries are executed eagerly during PARSE for lower
// latency. Parameterization is detected from the original query text via
// wire.ParseParameters rather than by scanning the translated SQL for a
// literal "?": the latter also matches a "?" that appears inside a string
// literal with zero real parameters (e.g. "WHERE msg = 'what?'"), which
// would incorrectly defer a query that has no bind variables at all.
func (h *ParseHandler) HandleParse(ctx context.Context, query string) (wire.PreparedStatements, error) {
	// Fail closed while the storage barrier is sealed: every query is
	// rejected with SQLSTATE 57P03 until the operator drives the unseal
	// API, mirroring the 503 / Unavailable envelope on the other
	// protocol transports.
	if h.sealedProbe != nil && h.sealedProbe.IsSealed() {
		return nil, sealedWireError(psqlerr.LevelError)
	}

	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return h.emptyResult()
	}

	// PostgreSQL's END/END WORK/END TRANSACTION are aliases for COMMIT that
	// go-mysql-server's parser does not itself accept; rewrite the
	// statement text before anything else touches it.
	trimmed = rewriteEndAsCommit(trimmed)

	// Handle session configuration commands (SET, SHOW, ...) as no-ops.
	// BEGIN/COMMIT/ROLLBACK are NOT no-ops -- see the noopStatements doc
	// comment -- so they fall through to translation/execution below like
	// any other statement.
	upper := strings.ToUpper(trimmed)
	if h.isNoopStatement(upper) {
		return h.emptyResult()
	}

	// Translate PG-specific syntax to GMS-compatible SQL.
	translated, err := h.translator.Translate(trimmed)
	if err != nil {
		return nil, &TranslationError{Query: trimmed, Err: err}
	}

	// Parameterized queries must defer execution until BIND/EXECUTE
	// when the wire protocol delivers parameter values. Non-parameterized
	// queries execute eagerly for lower latency.
	paramOIDs := wire.ParseParameters(trimmed)
	if len(paramOIDs) > 0 {
		return h.executeDeferred(ctx, trimmed, translated, paramOIDs)
	}

	return h.executeEager(ctx, trimmed, translated)
}

// executeEager runs the translated query immediately during the PARSE phase.
// This path is used for non-parameterized queries where all values are
// available in the SQL text itself.
func (h *ParseHandler) executeEager(ctx context.Context, trimmed, translated string) (wire.PreparedStatements, error) {
	// Build a GMS sql.Context with the QRDB session carrying identity.
	sqlCtx := h.buildSQLContext(ctx)

	// Execute the query through go-mysql-server.
	schema, rowIter, _, err := h.sqlEngine.Query(sqlCtx, translated)
	if err != nil {
		return nil, &QueryError{Query: trimmed, Err: err}
	}

	// Convert GMS schema to PG wire columns.
	columns := schemaToColumns(schema)

	// Collect all rows from the iterator so we can serve them
	// from the PreparedStatementFn callback.
	result, err := collectRows(sqlCtx, schema, rowIter)
	if err != nil {
		return nil, &QueryError{Query: trimmed, Err: err}
	}

	// Build the PreparedStatement with a callback that writes
	// the collected rows to the DataWriter.
	stmt := wire.NewStatement(
		func(_ context.Context, writer wire.DataWriter, _ []wire.Parameter) error {
			return writeRows(writer, result, trimmed)
		},
		wire.WithColumns(columns),
	)

	return wire.Prepared(stmt), nil
}

// executeDeferred creates a PreparedStatement whose callback defers query
// execution until the BIND/EXECUTE phase. This is required for parameterized
// queries because the PostgreSQL wire protocol sends parameter values in
// the BIND message, after PARSE has already completed.
//
// paramOIDs holds the parameter OID for each $N position, as returned by
// wire.ParseParameters — every entry starts unspecified (0). translated is
// parsed once here (rather than deferred to BIND/EXECUTE) so that its AST
// can be reused for two things: resolving real, non-zero OIDs for
// parameters that target a known column (see inferParamOIDs), and passing
// the already-parsed statement straight into QueryWithBindings at EXECUTE
// time instead of asking go-mysql-server to parse the same text again.
func (h *ParseHandler) executeDeferred(ctx context.Context, trimmed, translated string, paramOIDs []uint32) (wire.PreparedStatements, error) {
	sqlCtx := h.buildSQLContext(ctx)

	parsedStmt, parseErr := sqlparser.Parse(translated)
	if parseErr == nil {
		resolved := inferParamOIDs(sqlCtx, h.sqlEngine.Analyzer.Catalog, sqlCtx.GetCurrentDatabase(), parsedStmt)
		for ordinal, oid := range resolved {
			if ordinal >= 1 && ordinal <= len(paramOIDs) {
				paramOIDs[ordinal-1] = oid
			}
		}
	}

	stmt := wire.NewStatement(
		func(execCtx context.Context, writer wire.DataWriter, parameters []wire.Parameter) error {
			return h.executeDeferredCallback(execCtx, writer, parameters, trimmed, translated, parsedStmt, paramOIDs)
		},
		wire.WithColumns(h.resultColumns(sqlCtx, translated)),
		wire.WithParameters(paramOIDs),
	)

	return wire.Prepared(stmt), nil
}

// resultColumns returns the result columns a deferred statement will produce,
// resolved at PARSE time, or nil for a statement that produces no rows.
//
// The PostgreSQL extended query protocol settles a statement's result shape
// during Describe, which happens before BIND delivers any parameter value and
// long before a row exists. Whatever is declared here is what psql-wire sends
// as the RowDescription, and it is binding: the client negotiates its format
// codes from it, and psql-wire itself refuses to write a row whose width
// disagrees with it ("unexpected columns, N columns are defined inside the
// given table but M were given"). Declaring nothing for a statement that does
// produce a row is therefore not a conservative default but a hard failure at
// EXECUTE time.
//
// Two statement shapes produce rows. A SELECT is the obvious one. The other
// is INSERT … RETURNING, which is how gorm.io/driver/postgres reads back
// every generated primary key it issues — the single most common statement a
// PostgreSQL ORM sends. Both are resolved the same way, by asking
// go-mysql-server to analyze (not execute) the statement and taking the
// resulting plan node's schema; go-mysql-server's InsertInto node reports its
// RETURNING expressions as its schema, so no special case is needed beyond
// asking.
//
// The translated text is handed to PrepareQuery with its ? placeholders
// intact. Analyzing a statement that still has unbound parameters is exactly
// what PrepareQuery is for, and a result schema depends only on a
// statement's shape, never on parameter values. Substituting a stand-in
// literal for each placeholder first — ” was the obvious choice — instead
// makes the probe fail on every placeholder in a context that is not a
// string: LIMIT ? became LIMIT ”, which is not a valid row count, so the
// analysis errored and the statement was described as returning nothing.
// gorm issues LIMIT $N on every First/Take/Last, so that stand-in silently
// disabled the description for a large share of ordinary SELECT traffic.
//
// A probe that fails to analyze yields nil rather than an error: this is
// metadata resolution, and the authoritative diagnosis of a bad statement
// belongs to the execution path, which reports it against the caller's real
// parameters.
func (h *ParseHandler) resultColumns(sqlCtx *sql.Context, translated string) wire.Columns {
	upper := strings.ToUpper(strings.TrimSpace(translated))
	if !strings.HasPrefix(upper, "SELECT") && !engine.HasReturning(translated) {
		return nil
	}

	node, err := h.sqlEngine.PrepareQuery(sqlCtx, translated)
	if err != nil || node == nil {
		return nil
	}
	schema := node.Schema(sqlCtx)
	if len(schema) == 0 {
		return nil
	}
	return schemaToColumns(schema)
}

// executeDeferredCallback is the body of the deferred PreparedStatementFn
// callback. It converts wire parameters to GMS bindings, executes the query,
// and writes the result set. Extracted as a method to enable direct unit
// testing without needing to invoke the psql-wire callback machinery.
//
// parsedStmt is the AST produced by parsing translated at PARSE time (nil if
// that parse failed); passing it through to QueryWithBindings lets the
// engine execute directly against it instead of re-parsing translated from
// scratch. paramOIDs is the same slice executeDeferred declared in
// ParameterDescription, positionally aligned with parameters.
func (h *ParseHandler) executeDeferredCallback(
	ctx context.Context,
	writer wire.DataWriter,
	parameters []wire.Parameter,
	trimmed, translated string,
	parsedStmt sqlparser.Statement,
	paramOIDs []uint32,
) error {
	// Convert wire parameters to GMS binding expressions.
	bindings, err := parametersToBindings(parameters, paramOIDs)
	if err != nil {
		return err
	}

	// Build a fresh sql.Context for execution.
	sqlCtx := h.buildSQLContext(ctx)

	// Execute the query with parameter bindings. The schema is needed to
	// resolve ENUM/SET cells against their declared column types before
	// they are written to the wire; it is not sent as a RowDescription
	// here (see the note below).
	schema, rowIter, _, err := h.sqlEngine.QueryWithBindings(sqlCtx, translated, parsedStmt, bindings, nil)
	if err != nil {
		return &QueryError{Query: trimmed, Err: err}
	}

	// Column metadata was already provided at PARSE time via
	// PrepareQuery so psql-wire sent RowDescription during the
	// Describe phase. Sending it again here via Define() would
	// violate the extended query protocol and cause pgx to
	// misinterpret row data.

	// Collect and write result.
	result, err := collectRows(sqlCtx, schema, rowIter)
	if err != nil {
		return &QueryError{Query: trimmed, Err: err}
	}

	return writeRows(writer, result, trimmed)
}

// parametersToBindings converts PostgreSQL wire protocol parameters to
// go-mysql-server binding expressions. GMS names positional ? placeholders
// "v1", "v2", ... (1-indexed). NULL parameter values produce a
// sqlparser.NullVal regardless of OID.
//
// oids holds the PostgreSQL OID declared for each parameter position at
// PARSE time (see inferParamOIDs); a zero entry, or a shorter oids slice
// than parameters, means the position's target type could not be resolved
// statically. A resolved parameter is decoded through pgtype's own codec
// for that OID — which handles both the text and binary wire formats pgx
// may have chosen — and the resulting native Go value is dispatched to a
// bind expression by paramExpr, mirroring bindArg in
// sdk/go/driver/bindings.go. An unresolved parameter falls back to a plain
// string literal: safe for every scalar type, since GMS coerces a string
// literal to the target column's type during analysis, but not for binary
// columns, which is exactly the case inferParamOIDs exists to resolve.
func parametersToBindings(parameters []wire.Parameter, oids []uint32) (map[string]sqlparser.Expr, error) {
	bindings := make(map[string]sqlparser.Expr, len(parameters))
	for i, p := range parameters {
		// GMS uses 1-indexed names: v1, v2, v3, ...
		name := "v" + strconv.Itoa(i+1)

		if p.Value() == nil {
			bindings[name] = &sqlparser.NullVal{}
			continue
		}

		var oid uint32
		if i < len(oids) {
			oid = oids[i]
		}
		if oid == 0 {
			bindings[name] = sqlparser.NewStrVal(p.Value())
			continue
		}

		native, err := p.Scan(oid)
		if err != nil {
			return nil, &ParameterBindingError{Index: i, Err: err}
		}

		expr, err := paramExpr(native)
		if err != nil {
			return nil, &ParameterBindingError{Index: i, Err: err}
		}
		bindings[name] = expr
	}

	return bindings, nil
}

// paramExpr converts a pgtype-decoded native Go value into the matching
// sqlparser bind expression, mirroring bindArg in
// sdk/go/driver/bindings.go. []byte is bound as a hex literal rather than a
// string literal so the engine types it as binary rather than
// reinterpreting it under the connection charset — the corruption
// decodeBytea used to paper over after the fact by guessing at the wire
// text's shape instead of knowing the parameter's real type.
func paramExpr(v any) (sqlparser.Expr, error) {
	switch val := v.(type) {
	case bool:
		if val {
			return sqlparser.NewIntVal([]byte("1")), nil
		}
		return sqlparser.NewIntVal([]byte("0")), nil

	case int16:
		return sqlparser.NewIntVal(strconv.AppendInt(nil, int64(val), 10)), nil

	case int32:
		return sqlparser.NewIntVal(strconv.AppendInt(nil, int64(val), 10)), nil

	case int64:
		return sqlparser.NewIntVal(strconv.AppendInt(nil, val, 10)), nil

	case float32:
		return sqlparser.NewFloatVal(strconv.AppendFloat(nil, float64(val), 'g', -1, 32)), nil

	case float64:
		return sqlparser.NewFloatVal(strconv.AppendFloat(nil, val, 'g', -1, 64)), nil

	case string:
		return sqlparser.NewStrVal([]byte(val)), nil

	case []byte:
		return sqlparser.NewHexVal([]byte(hex.EncodeToString(val))), nil

	case time.Time:
		return sqlparser.NewStrVal([]byte(val.UTC().Format(paramTimeLayout))), nil

	default:
		return nil, &UnsupportedParameterTypeError{Value: v}
	}
}

// paramOIDForColumn returns the PostgreSQL OID to declare for a bind
// parameter targeting a column of type t, so pgx encodes — and this server
// decodes — the value through a real typed codec instead of a string
// literal. It returns 0 (unspecified) for any GMS type it does not
// recognize, which parametersToBindings treats as "fall back to the
// pre-existing string-literal binding".
//
// This intentionally diverges from LookupOID, which maps BLOB/VARBINARY
// output columns to TextOID so the read path can serialize arbitrary bytes
// as PG text without negotiating a binary codec pgx may not support. For a
// bind parameter the direction is reversed and the risk is opposite: a
// binary column bound as a string literal has its raw bytes reinterpreted
// under the connection charset, corrupting anything that is not valid
// UTF-8. Declaring ByteaOID here instead lets the value round-trip as true
// binary end to end — the actual fix for the corruption decodeBytea used
// to paper over.
//
// Integer and float columns are all mapped to the widest OID in their
// family (Int8OID, Float8OID) regardless of the column's declared width,
// so a single consistently-used OID drives both the encode (pgx) and
// decode (Scan) side without a width-mismatch risk from picking one OID to
// declare and assuming a narrower one to decode.
func paramOIDForColumn(t sql.Type) uint32 {
	switch {
	case types.IsJSON(t), types.IsGeometry(t):
		// Left unresolved (falls back to a string-literal bind). GMS's own
		// IsBinaryType classifies JSON and spatial columns as "binary"
		// because of how they're framed on the MySQL wire protocol, but
		// they are not PostgreSQL bytea; declaring ByteaOID for them would
		// be wrong. Both are text-representable and GMS coerces a string
		// literal to them without loss, so the safe fallback is correct
		// here, unlike for a genuine binary column.
		return 0
	case types.IsBinaryType(t):
		return pgtype.ByteaOID
	case types.IsBoolean(t):
		return pgtype.BoolOID
	case types.IsInteger(t):
		return pgtype.Int8OID
	case types.IsFloat(t), types.IsDecimal(t):
		return pgtype.Float8OID
	case types.IsDatetimeType(t), types.IsTimestampType(t):
		return pgtype.TimestamptzOID
	case types.IsTextOnly(t):
		return pgtype.TextOID
	default:
		return 0
	}
}

// inferParamOIDs performs a best-effort, single-table resolution of each
// bind parameter's target column type, returning the PostgreSQL OID to
// declare for it, keyed by 1-indexed parameter ordinal.
//
// Only two statement shapes are resolved: an INSERT with an explicit
// column list, and an UPDATE SET clause — the shapes GORM's Create and
// Save/Updates paths generate, and the only ones where an unresolved
// binary column corrupts data on write (see paramOIDForColumn's doc
// comment). Every other parameter position — including WHERE-clause
// parameters, multi-table statements, and anything this walk cannot
// uniquely resolve — is left unmapped and falls back to the pre-existing
// string-literal binding in parametersToBindings, exactly as before this
// change for every type except an unresolved binary column.
func inferParamOIDs(sqlCtx *sql.Context, catalog sql.Catalog, dbName string, stmt sqlparser.Statement) map[int]uint32 {
	oids := make(map[int]uint32)

	switch node := stmt.(type) {
	case *sqlparser.Insert:
		table, ok := lookupTable(sqlCtx, catalog, dbName, node.Table.Name.String())
		if !ok {
			return oids
		}
		values, ok := insertValues(node.Rows)
		if !ok {
			return oids
		}
		schema := table.Schema(sqlCtx)
		for _, tuple := range values {
			for i, expr := range tuple {
				if i >= len(node.Columns) {
					continue
				}
				ordinal, ok := valArgOrdinal(expr)
				if !ok {
					continue
				}
				colType, ok := columnType(schema, node.Columns[i])
				if !ok {
					continue
				}
				oids[ordinal] = paramOIDForColumn(colType)
			}
		}

	case *sqlparser.Update:
		if len(node.TableExprs) != 1 {
			return oids
		}
		tableName, ok := singleTableName(node.TableExprs[0])
		if !ok {
			return oids
		}
		table, ok := lookupTable(sqlCtx, catalog, dbName, tableName)
		if !ok {
			return oids
		}
		schema := table.Schema(sqlCtx)
		for _, assign := range node.Exprs {
			ordinal, ok := valArgOrdinal(assign.Expr)
			if !ok {
				continue
			}
			colType, ok := columnType(schema, assign.Name.Name)
			if !ok {
				continue
			}
			oids[ordinal] = paramOIDForColumn(colType)
		}
	}

	return oids
}

// lookupTable resolves tableName in dbName through catalog, reporting false
// if the table does not exist or the lookup errors — either way, the
// caller's safe response is to leave the affected parameters unmapped.
func lookupTable(sqlCtx *sql.Context, catalog sql.Catalog, dbName, tableName string) (sql.Table, bool) {
	table, _, err := catalog.Table(sqlCtx, dbName, tableName)
	if err != nil || table == nil {
		return nil, false
	}
	return table, true
}

// insertValues extracts the row tuples from an INSERT statement's Rows
// field, or false if rows is anything other than a literal VALUES list
// (e.g. INSERT ... SELECT). Vitess represents a plain VALUES clause as
// sqlparser.Values, and a VALUES clause carrying an "AS alias(cols)" row
// alias (MySQL 8.0.19+ syntax) as *sqlparser.AliasedValues, which embeds
// Values; both are handled so a query using the alias form still resolves.
func insertValues(rows sqlparser.InsertRows) (sqlparser.Values, bool) {
	switch v := rows.(type) {
	case sqlparser.Values:
		return v, true
	case *sqlparser.AliasedValues:
		return v.Values, true
	default:
		return nil, false
	}
}

// singleTableName returns the unqualified table name referenced by a plain
// single-table expression, or false if expr is a join, subquery, or
// anything else that does not name exactly one table.
func singleTableName(expr sqlparser.TableExpr) (string, bool) {
	aliased, ok := expr.(*sqlparser.AliasedTableExpr)
	if !ok {
		return "", false
	}
	name, ok := aliased.Expr.(sqlparser.TableName)
	if !ok {
		return "", false
	}
	return name.Name.String(), true
}

// columnType returns the GMS type of the column named col in schema,
// comparing case-insensitively as SQL identifiers are.
func columnType(schema sql.Schema, col sqlparser.ColIdent) (sql.Type, bool) {
	for _, c := range schema {
		if col.EqualString(c.Name) {
			return c.Type, true
		}
	}
	return nil, false
}

// valArgOrdinal reports the 1-indexed bind variable ordinal of expr, if
// expr is a positional placeholder produced by translating a PostgreSQL $N
// parameter. Vitess rewrites each "?" token in the query text to a
// *sqlparser.SQLVal of type ValArg whose Val is ":vN" (1-indexed); this
// recovers N.
func valArgOrdinal(expr sqlparser.Expr) (int, bool) {
	val, ok := expr.(*sqlparser.SQLVal)
	if !ok || val.Type != sqlparser.ValArg {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(string(val.Val), ":v"))
	if err != nil {
		return 0, false
	}
	return n, true
}

// isNoopStatement returns true if the given upper-cased SQL statement
// should be handled as a no-op.
func (h *ParseHandler) isNoopStatement(upper string) bool {
	// Check exact matches first (BEGIN, COMMIT, etc.).
	// Strip trailing semicolons for matching.
	cleaned := strings.TrimRight(upper, "; ")
	if noopStatements[cleaned] {
		return true
	}

	// Check prefix matches (SET, SHOW, RESET, etc.).
	for _, prefix := range noopPrefixes {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}

	return false
}

// emptyResult returns a PreparedStatements with no columns and an empty
// data writer callback.
func (h *ParseHandler) emptyResult() (wire.PreparedStatements, error) {
	stmt := wire.NewStatement(emptyResultFn)
	return wire.Prepared(stmt), nil
}

// emptyResultFn is the PreparedStatementFn used by emptyResult. It sends
// a simple "OK" completion tag to the DataWriter. Extracted as a
// package-level function to enable direct unit testing.
func emptyResultFn(_ context.Context, writer wire.DataWriter, _ []wire.Parameter) error {
	return writer.Complete("OK")
}

// buildSQLContext creates a go-mysql-server sql.Context from the connection
// context. If an auth identity is present, it is carried through via a
// QRDBSession for tenant-scoped operations.
func (h *ParseHandler) buildSQLContext(ctx context.Context) *sql.Context {
	session := connectionSessionFromContext(ctx)
	if session == nil {
		// Defensive fallback for a caller that bypasses the connection-level
		// SessionMiddleware registered in server.go (e.g. HandleParse
		// invoked directly, as this package's own unit tests do) -- see
		// newConnectionSessionHandler's doc comment. A session built this
		// way is a fresh, throwaway one exactly like every ParseFn call got
		// before interactive transactions were supported, so BEGIN/COMMIT/
		// ROLLBACK state cannot persist across statements through it; every
		// real connection accepted by Server always goes through
		// newConnectionSessionHandler instead.
		identity := IdentityFromContext(ctx)
		session = provider.NewSession(sql.NewBaseSession(), identity)
	}
	sqlCtx := sql.NewContext(ctx, sql.WithSession(session))
	sqlCtx.SetCurrentDatabase(sessionDatabase(wire.ClientParameters(ctx), h.dbName))
	return sqlCtx
}

// sessionDatabase returns the database a connection selected in its PostgreSQL
// startup message, falling back to the server-wide default when the client
// named none.
//
// Honouring the startup parameter is what makes `dbname=` mean anything: the
// server-wide default is only a default. Serving it regardless would silently
// route a client that asked for database A against database B for every
// statement -- succeeding against the wrong keyspace, or, when the default is
// not one the connection's tenant owns, failing every statement with
// "service: database not found: <default>". The MySQL wire server has always
// carried its own per-connection database; this is the PostgreSQL side of the
// same contract.
//
// It takes the parameter map rather than the context because psql-wire's
// context key for it is unexported, so a context carrying client parameters
// cannot be constructed outside that package -- and this decision is worth a
// unit test.
func sessionDatabase(params wire.Parameters, fallback string) string {
	if requested := params[wire.ParamDatabase]; requested != "" {
		return requested
	}
	return fallback
}

// ---------------------------------------------------------------------------
// Per-connection session (interactive transactions)
//
// go-mysql-server stores an interactive transaction's state (see
// provider.QRDBSession's sql.TransactionSession implementation) on the
// session, not the query context, so BEGIN/COMMIT/ROLLBACK/SAVEPOINT can
// only take effect across the statements of a PostgreSQL wire connection if
// every statement on that connection shares the SAME *provider.QRDBSession
// -- exactly as sdk/go/driver's embeddedConn holds one session for its
// whole lifetime (see that package's newEmbeddedSession doc comment).
// psql-wire's ParseFn (HandleParse) is called once per statement with no
// connection handle of its own, so the session is instead created once, by
// a wire.SessionHandler registered via wire.SessionMiddleware (see
// Server.Start), and threaded through every subsequent statement's context
// -- the same mechanism auth.go's identityContextKey already uses to carry
// the authenticated identity across a connection's statements.
// ---------------------------------------------------------------------------

// qrdbSessionContextKey is the context key newConnectionSessionHandler
// stores the connection's *provider.QRDBSession under.
type qrdbSessionContextKey struct{}

// newConnectionSessionHandler returns a wire.SessionHandler that creates
// exactly one *provider.QRDBSession for the lifetime of a PostgreSQL wire
// connection. It must be registered (see server.go) AFTER any auth
// wire.SessionMiddleware, so IdentityFromContext already reflects whatever
// identity authentication established by the time the session is built --
// the identity determines the session's tenant scoping for every statement
// issued on the connection.
func newConnectionSessionHandler() wire.SessionHandler {
	return func(ctx context.Context) (context.Context, error) {
		identity := IdentityFromContext(ctx)
		session := provider.NewSession(sql.NewBaseSession(), identity)
		return context.WithValue(ctx, qrdbSessionContextKey{}, session), nil
	}
}

// connectionSessionFromContext retrieves the connection's QRDBSession
// stored by newConnectionSessionHandler, or nil if none is present (see
// buildSQLContext's fallback).
func connectionSessionFromContext(ctx context.Context) *provider.QRDBSession {
	session, _ := ctx.Value(qrdbSessionContextKey{}).(*provider.QRDBSession)
	return session
}

// schemaToColumns converts a GMS sql.Schema to psql-wire Columns with
// the appropriate PostgreSQL OIDs.
func schemaToColumns(schema sql.Schema) wire.Columns {
	columns := make(wire.Columns, len(schema))
	for i, col := range schema {
		typeName := strings.ToUpper(col.Type.Type().String())
		columns[i] = wire.Column{
			Name:  col.Name,
			Oid:   LookupOID(typeName),
			Width: int16(col.Type.MaxTextResponseByteLength(nil)),
		}
	}
	return columns
}

// queryResult holds the result of a GMS query execution. For DML
// statements (INSERT/UPDATE/DELETE), isDML is true and rowsAffected
// contains the affected count. For SELECT, rows contains the data.
type queryResult struct {
	isDML        bool
	rowsAffected uint64
	rows         [][]any
}

// collectRows materializes every row of iter. schema describes iter's
// columns and is what lets each cell be resolved against its declared type
// before it reaches the wire -- without it an ENUM cell would be written as
// its ordinal and a SET cell as its bitmask (see
// catalog.ResolveColumnValue). A nil or short schema leaves the
// corresponding cells unresolved, which is the correct behaviour for the
// single-cell OkResult rows DML produces.
func collectRows(ctx *sql.Context, schema sql.Schema, iter sql.RowIter) (*queryResult, error) {
	result := &queryResult{}
	for {
		row, err := iter.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = iter.Close(ctx)
			return nil, err
		}

		// DML statements return a single OkResult row.
		if len(row) == 1 {
			if ok, isOk := row[0].(types.OkResult); isOk {
				result.isDML = true
				result.rowsAffected = ok.RowsAffected
				continue
			}
		}

		// Convert sql.Row ([]interface{}) to []any for the wire writer.
		anyRow := make([]any, len(row))
		for j, v := range row {
			if j < len(schema) && schema[j] != nil {
				resolved, resolveErr := catalog.ResolveColumnValue(ctx, schema[j].Type, v)
				if resolveErr != nil {
					_ = iter.Close(ctx)
					return nil, resolveErr
				}
				v = resolved
			}
			anyRow[j] = formatValue(v)
		}
		result.rows = append(result.rows, anyRow)
	}

	_ = iter.Close(ctx)
	return result, nil
}

// writeRows writes the collected rows to the DataWriter and sends the
// command complete tag.
func writeRows(writer wire.DataWriter, result *queryResult, query string) error {
	if result.isDML {
		tag := buildCompleteTag(query, int(result.rowsAffected))
		return writer.Complete(tag)
	}

	for _, row := range result.rows {
		if err := writer.Row(row); err != nil {
			return err
		}
	}

	tag := buildCompleteTag(query, len(result.rows))
	return writer.Complete(tag)
}

// buildCompleteTag generates the PostgreSQL CommandComplete tag string
// for a given query and row count. For SELECT queries, it returns
// "SELECT N". For INSERT/UPDATE/DELETE, it returns the appropriate tag
// with affected row count.
func buildCompleteTag(query string, rowCount int) string {
	upper := strings.ToUpper(strings.TrimSpace(query))
	switch {
	case strings.HasPrefix(upper, "SELECT"):
		return fmt.Sprintf("SELECT %d", rowCount)
	case strings.HasPrefix(upper, "INSERT"):
		return fmt.Sprintf("INSERT 0 %d", rowCount)
	case strings.HasPrefix(upper, "UPDATE"):
		return fmt.Sprintf("UPDATE %d", rowCount)
	case strings.HasPrefix(upper, "DELETE"):
		return fmt.Sprintf("DELETE %d", rowCount)
	default:
		return "OK"
	}
}

// formatValue converts a GMS value to a format suitable for the PG wire
// protocol. Byte slices are converted to string for text-mode encoding
// since BLOB/VARBINARY columns use TextOID to avoid binary format
// issues with pgx's extended query protocol.
func formatValue(v any) any {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case []byte:
		return string(val)
	default:
		return val
	}
}
