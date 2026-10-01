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

package driver

import (
	"context"
	"database/sql"
	sqld "database/sql/driver"
	"sync/atomic"
	"time"

	sqle "github.com/dolthub/go-mysql-server"
	gmssql "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// Compile-time interface assertions.
var (
	_ sqld.Conn               = (*embeddedConn)(nil)
	_ sqld.ConnBeginTx        = (*embeddedConn)(nil)
	_ sqld.ConnPrepareContext = (*embeddedConn)(nil)
	_ sqld.ExecerContext      = (*embeddedConn)(nil)
	_ sqld.QueryerContext     = (*embeddedConn)(nil)
	_ sqld.NamedValueChecker  = (*embeddedConn)(nil)
	_ sqld.Pinger             = (*embeddedConn)(nil)
	_ sqld.SessionResetter    = (*embeddedConn)(nil)
	_ sqld.Validator          = (*embeddedConn)(nil)
)

// embeddedConn implements database/sql/driver.Conn by executing queries
// directly against a GMS (go-mysql-server) engine instance.
//
// It owns exactly one go-mysql-server session for its lifetime. See
// newEmbeddedSession for why that matters.
type embeddedConn struct {
	engine   *sqle.Engine
	database string
	session  *provider.QRDBSession
	loc      *time.Location
	closed   atomic.Bool
	inTx     atomic.Bool
	bad      atomic.Bool
}

// Prepare returns a prepared statement for the given query.
func (c *embeddedConn) Prepare(query string) (sqld.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

// PrepareContext prepares the query through the engine so that placeholder
// counting and syntax errors are reported at prepare time rather than at
// execution, matching what a client expects from a real prepared statement.
func (c *embeddedConn) PrepareContext(ctx context.Context, query string) (sqld.Stmt, error) {
	if c.closed.Load() {
		return nil, &ConnClosedError{}
	}

	sqlCtx := c.newSQLContext(ctx, query)
	end, err := c.beginCommand()
	if err != nil {
		return nil, err
	}
	_, err = c.engine.PrepareQuery(sqlCtx, query)
	end()
	if err != nil {
		return nil, &PrepareError{Query: query, Err: asSQLStateError(err, query)}
	}

	return &stmt{conn: c, query: query, numInput: countPlaceholders(query)}, nil
}

// Close marks the connection as closed and releases the engine-side session
// state, including any prepared statements cached under this session's id.
func (c *embeddedConn) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	if c.session != nil {
		// Ending the session releases any row lock an open transaction on
		// this connection still holds, rather than leaving it to expire.
		c.session.SessionEnd()
		c.engine.CloseSession(c.session.ID())
	}
	return nil
}

// IsValid reports whether the connection may be reused from the pool.
func (c *embeddedConn) IsValid() bool {
	return !c.closed.Load() && !c.bad.Load()
}

// ResetSession prepares a pooled connection for reuse by a new caller. A
// transaction left open by the previous user is rolled back rather than
// inherited, and a connection that cannot be returned to a clean state is
// reported bad so the pool discards it.
func (c *embeddedConn) ResetSession(ctx context.Context) error {
	if c.closed.Load() {
		return sqld.ErrBadConn
	}

	if c.inTx.Load() {
		if _, err := c.execInternal(ctx, "ROLLBACK"); err != nil {
			c.bad.Store(true)
			return sqld.ErrBadConn
		}
		c.inTx.Store(false)
	}

	c.session.SetCurrentDatabase(c.database)
	return nil
}

// Ping verifies the engine is reachable and able to execute a statement.
func (c *embeddedConn) Ping(ctx context.Context) error {
	if c.closed.Load() {
		return sqld.ErrBadConn
	}
	if _, err := c.execInternal(ctx, "SELECT 1"); err != nil {
		c.bad.Store(true)
		return sqld.ErrBadConn
	}
	return nil
}

// CheckNamedValue accepts the Go types bindArg can bind, widening the default
// database/sql conversion so that unsigned 64-bit values and the narrower
// integer and float widths reach the driver intact instead of being funnelled
// through int64 (which wraps large uint64 values negative).
func (c *embeddedConn) CheckNamedValue(nv *sqld.NamedValue) error {
	if nv.Name != "" {
		return &NamedParameterUnsupportedError{Name: nv.Name}
	}

	val, err := normalizeArgValue(nv.Ordinal, nv.Value)
	if err != nil {
		return err
	}
	nv.Value = val
	return nil
}

// Begin starts a new transaction with default options.
func (c *embeddedConn) Begin() (sqld.Tx, error) {
	return c.BeginTx(context.Background(), sqld.TxOptions{})
}

// BeginTx starts a new transaction with the given options.
//
// Isolation levels are mapped onto SET TRANSACTION ISOLATION LEVEL rather than
// rejected outright, so a caller asking for a level the engine treats as an
// alias of its default is served rather than refused.
func (c *embeddedConn) BeginTx(ctx context.Context, opts sqld.TxOptions) (sqld.Tx, error) {
	if c.closed.Load() {
		return nil, &ConnClosedError{}
	}

	level := sql.IsolationLevel(opts.Isolation)
	if stmt, ok := isolationLevelStatements[level]; ok && stmt != "" {
		if _, err := c.execInternal(ctx, stmt); err != nil {
			return nil, err
		}
	} else if !ok {
		return nil, &UnsupportedIsolationError{Level: level}
	}

	begin := "START TRANSACTION"
	if opts.ReadOnly {
		begin = "START TRANSACTION READ ONLY"
	}
	if _, err := c.execInternal(ctx, begin); err != nil {
		return nil, err
	}

	c.inTx.Store(true)
	return &embeddedTx{conn: c}, nil
}

// isolationLevelStatements maps a database/sql isolation level to the
// statement that selects it, or the empty string when the engine's default
// already provides it. A level absent from the map is unsupported.
var isolationLevelStatements = map[sql.IsolationLevel]string{
	sql.LevelDefault:         "",
	sql.LevelSerializable:    "SET TRANSACTION ISOLATION LEVEL SERIALIZABLE",
	sql.LevelRepeatableRead:  "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ",
	sql.LevelReadCommitted:   "SET TRANSACTION ISOLATION LEVEL READ COMMITTED",
	sql.LevelReadUncommitted: "SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED",
}

// QueryContext executes a query that returns rows.
func (c *embeddedConn) QueryContext(ctx context.Context, query string, args []sqld.NamedValue) (sqld.Rows, error) {
	if c.closed.Load() {
		return nil, &ConnClosedError{}
	}

	end, err := c.beginCommand()
	if err != nil {
		return nil, err
	}
	sqlCtx, schema, iter, err := c.run(ctx, query, args)
	if err != nil {
		end()
		return nil, err
	}

	// The command lasts until the caller closes the rows: that is when the
	// statement has finished running.
	return &embeddedRows{
		ctx:    sqlCtx,
		schema: schema,
		iter:   iter,
		end:    end,
	}, nil
}

// ExecContext executes a query that does not return rows.
func (c *embeddedConn) ExecContext(ctx context.Context, query string, args []sqld.NamedValue) (sqld.Result, error) {
	if c.closed.Load() {
		return nil, &ConnClosedError{}
	}

	end, err := c.beginCommand()
	if err != nil {
		return nil, err
	}
	defer end()
	sqlCtx, schema, iter, err := c.run(ctx, query, args)
	if err != nil {
		return nil, err
	}

	okRes, ok, err := drainOkResult(sqlCtx, schema, iter)
	if err != nil {
		return nil, &QueryExecError{Query: query, Err: asSQLStateError(err, query)}
	}
	if !ok {
		// A statement with no OkResult (DDL, or a SELECT run through Exec)
		// affects no rows and generates no insert id.
		return &result{}, nil
	}

	return &result{
		lastInsertID: int64(okRes.InsertID),
		rowsAffected: int64(okRes.RowsAffected),
	}, nil
}

// run binds the arguments and executes the query, returning the engine
// context alongside the result so callers can iterate or drain it.
//
// The query text is passed to the engine verbatim; arguments travel as bind
// expressions and are never spliced into it.
func (c *embeddedConn) run(ctx context.Context, query string, args []sqld.NamedValue) (*gmssql.Context, gmssql.Schema, gmssql.RowIter, error) {
	bindings, err := namedValuesToBindings(args, c.loc)
	if err != nil {
		return nil, nil, nil, err
	}

	sqlCtx := c.newSQLContext(ctx, query)

	schema, iter, _, err := c.engine.QueryWithBindings(sqlCtx, query, nil, bindings, nil)
	if err != nil {
		return nil, nil, nil, &QueryExecError{Query: query, Err: asSQLStateError(err, query)}
	}
	return sqlCtx, schema, iter, nil
}

// execInternal runs a driver-issued statement (transaction control, ping) that
// takes no arguments and returns no rows of interest.
func (c *embeddedConn) execInternal(ctx context.Context, query string) (sqld.Result, error) {
	end, err := c.beginCommand()
	if err != nil {
		return nil, err
	}
	defer end()
	sqlCtx, schema, iter, err := c.run(ctx, query, nil)
	if err != nil {
		return nil, err
	}
	if _, _, drainErr := drainOkResult(sqlCtx, schema, iter); drainErr != nil {
		return nil, &QueryExecError{Query: query, Err: asSQLStateError(drainErr, query)}
	}
	return &result{}, nil
}

// beginCommand brackets one statement as a command on the connection's
// session, as go-mysql-server's MySQL handler brackets each of its own, and
// returns the call that ends it. The session resolves each table and
// servicer once for the statement in between (see the SQL provider's
// statement scope).
func (c *embeddedConn) beginCommand() (end func(), err error) {
	if c.session == nil {
		return func() {}, nil
	}
	if err := gmssql.SessionCommandBegin(c.session); err != nil {
		return nil, err
	}
	return func() { gmssql.SessionCommandEnd(c.session) }, nil
}
