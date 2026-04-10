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
	"fmt"
	"io"
	"strconv"
	"strings"

	sqle "github.com/dolthub/go-mysql-server"
	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/dolthub/vitess/go/vt/sqlparser"
	wire "github.com/jeroenrinzema/psql-wire"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// noopStatements are SQL statements that are handled as no-ops because they
// are PG session management commands that have no equivalent in GMS.
var noopStatements = map[string]bool{
	"BEGIN":    true,
	"COMMIT":   true,
	"ROLLBACK": true,
	"END":      true,
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
// Parameterized queries (containing ? placeholders after translation) are
// deferred: the translated SQL is captured at PARSE time but execution is
// delayed until the BIND/EXECUTE phase when parameter values arrive.
// Non-parameterized queries are executed eagerly during PARSE for lower
// latency.
func (h *ParseHandler) HandleParse(ctx context.Context, query string) (wire.PreparedStatements, error) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return h.emptyResult()
	}

	// Handle transaction control and session commands as no-ops.
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
	if strings.Contains(translated, "?") {
		return h.executeDeferred(ctx, trimmed, translated)
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
	result, err := collectRows(sqlCtx, rowIter)
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
func (h *ParseHandler) executeDeferred(ctx context.Context, trimmed, translated string) (wire.PreparedStatements, error) {
	// Parse the original PG query for $N parameters so psql-wire sends the
	// correct ParameterDescription back to the client. Without this, pgx
	// sees 0 expected params but N supplied args and rejects the statement.
	params := wire.ParseParameters(trimmed)

	// Determine result columns at PARSE time so pgx can negotiate proper
	// format codes during BIND. Without this, pgx receives NoData from
	// Describe and cannot decode values during EXECUTE.
	//
	// GMS PrepareQuery analyzes the SQL without executing, giving us the
	// output schema. Since the translated query contains ? placeholders
	// that GMS cannot analyze directly, we replace them with empty string
	// literals — the output schema does not depend on parameter values.
	//
	// For DML statements (INSERT/UPDATE/DELETE), columns remain nil which
	// is correct since they produce no result rows.
	var columns wire.Columns
	upper := strings.ToUpper(strings.TrimSpace(translated))
	if strings.HasPrefix(upper, "SELECT") {
		schemaQuery := strings.ReplaceAll(translated, "?", "''")
		sqlCtx := h.buildSQLContext(ctx)
		node, err := h.sqlEngine.PrepareQuery(sqlCtx, schemaQuery)
		if err == nil && node != nil {
			schema := node.Schema()
			if len(schema) > 0 {
				columns = schemaToColumns(schema)
			}
		}
	}

	stmt := wire.NewStatement(
		func(execCtx context.Context, writer wire.DataWriter, parameters []wire.Parameter) error {
			return h.executeDeferredCallback(execCtx, writer, parameters, trimmed, translated)
		},
		wire.WithColumns(columns),
		wire.WithParameters(params),
	)

	return wire.Prepared(stmt), nil
}

// executeDeferredCallback is the body of the deferred PreparedStatementFn
// callback. It converts wire parameters to GMS bindings, executes the query,
// and writes the result set. Extracted as a method to enable direct unit
// testing without needing to invoke the psql-wire callback machinery.
func (h *ParseHandler) executeDeferredCallback(
	ctx context.Context,
	writer wire.DataWriter,
	parameters []wire.Parameter,
	trimmed, translated string,
) error {
	// Convert wire parameters to GMS binding expressions.
	bindings, err := parametersToBindings(parameters)
	if err != nil {
		return err
	}

	// Build a fresh sql.Context for execution.
	sqlCtx := h.buildSQLContext(ctx)

	// Execute the query with parameter bindings.
	_, rowIter, _, err := h.sqlEngine.QueryWithBindings(sqlCtx, translated, nil, bindings, nil)
	if err != nil {
		return &QueryError{Query: trimmed, Err: err}
	}

	// Column metadata was already provided at PARSE time via
	// PrepareQuery so psql-wire sent RowDescription during the
	// Describe phase. Sending it again here via Define() would
	// violate the extended query protocol and cause pgx to
	// misinterpret row data.

	// Collect and write result.
	result, err := collectRows(sqlCtx, rowIter)
	if err != nil {
		return &QueryError{Query: trimmed, Err: err}
	}

	return writeRows(writer, result, trimmed)
}

// parametersToBindings converts PostgreSQL wire protocol parameters to
// go-mysql-server binding expressions. GMS names positional ? placeholders
// as "v1", "v2", etc. (1-indexed). NULL parameter values produce a
// sqlparser.NullVal; non-nil values produce a string literal that GMS
// will coerce to the appropriate type during query analysis.
//
// When the server returns unspecified parameter OIDs, pgx sends []byte
// values as PostgreSQL bytea hex format (\x followed by hex pairs).
// This function detects and decodes that format to recover the original
// binary data.
func parametersToBindings(parameters []wire.Parameter) (map[string]sqlparser.Expr, error) {
	bindings := make(map[string]sqlparser.Expr, len(parameters))
	for i, p := range parameters {
		// GMS uses 1-indexed names: v1, v2, v3, ...
		name := "v" + strconv.Itoa(i+1)

		if p.Value() == nil {
			bindings[name] = &sqlparser.NullVal{}
			continue
		}

		bindings[name] = sqlparser.NewStrVal(decodeBytea(p.Value()))
	}

	return bindings, nil
}

// decodeBytea detects PostgreSQL bytea hex format (\x followed by hex
// pairs) and decodes it to raw bytes. Non-bytea values are returned
// unchanged. This is needed because pgx sends []byte parameters as
// bytea hex when the server returns unspecified (0) parameter OIDs.
func decodeBytea(val []byte) []byte {
	if len(val) >= 2 && val[0] == '\\' && val[1] == 'x' {
		decoded, err := hex.DecodeString(string(val[2:]))
		if err == nil {
			return decoded
		}
	}
	return val
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
	identity := IdentityFromContext(ctx)
	base := sql.NewBaseSession()
	session := provider.NewSession(base, identity)
	sqlCtx := sql.NewContext(ctx, sql.WithSession(session))
	sqlCtx.SetCurrentDatabase(h.dbName)
	return sqlCtx
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

func collectRows(ctx *sql.Context, iter sql.RowIter) (*queryResult, error) {
	result := &queryResult{}
	for {
		row, err := iter.Next(ctx)
		if err == io.EOF {
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
