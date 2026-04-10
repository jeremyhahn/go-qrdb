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
	"io"
	"sync/atomic"

	sqle "github.com/dolthub/go-mysql-server"
	gmssql "github.com/dolthub/go-mysql-server/sql"
)

// Compile-time interface assertions.
var (
	_ sqld.Conn           = (*embeddedConn)(nil)
	_ sqld.ConnBeginTx    = (*embeddedConn)(nil)
	_ sqld.ExecerContext  = (*embeddedConn)(nil)
	_ sqld.QueryerContext = (*embeddedConn)(nil)
)

// embeddedConn implements database/sql/driver.Conn by executing queries
// directly against a GMS (go-mysql-server) engine instance.
type embeddedConn struct {
	engine   *sqle.Engine
	database string
	closed   atomic.Bool
	inTx     atomic.Bool
}

// Prepare returns a prepared statement for the given query.
func (c *embeddedConn) Prepare(query string) (sqld.Stmt, error) {
	if c.closed.Load() {
		return nil, &ConnClosedError{}
	}
	return &stmt{conn: c, query: query}, nil
}

// Close marks the connection as closed.
func (c *embeddedConn) Close() error {
	c.closed.Store(true)
	return nil
}

// Begin starts a new transaction with default options.
func (c *embeddedConn) Begin() (sqld.Tx, error) {
	return c.BeginTx(context.Background(), sqld.TxOptions{})
}

// BeginTx starts a new transaction with the given options. Only the
// default isolation level is supported for embedded mode.
func (c *embeddedConn) BeginTx(_ context.Context, opts sqld.TxOptions) (sqld.Tx, error) {
	if c.closed.Load() {
		return nil, &ConnClosedError{}
	}

	if sql.IsolationLevel(opts.Isolation) != sql.LevelDefault &&
		sql.IsolationLevel(opts.Isolation) != sql.LevelSerializable {
		return nil, &UnsupportedIsolationError{Level: sql.IsolationLevel(opts.Isolation)}
	}

	c.inTx.Store(true)

	sqlCtx := gmssql.NewEmptyContext()
	sqlCtx.SetCurrentDatabase(c.database)

	// Execute BEGIN to start a GMS transaction.
	_, _, _, err := c.engine.Query(sqlCtx, "BEGIN")
	if err != nil {
		c.inTx.Store(false)
		return nil, &QueryExecError{Query: "BEGIN", Err: err}
	}

	return &embeddedTx{conn: c}, nil
}

// QueryContext executes a query that returns rows.
func (c *embeddedConn) QueryContext(_ context.Context, query string, args []sqld.NamedValue) (sqld.Rows, error) {
	if c.closed.Load() {
		return nil, &ConnClosedError{}
	}

	substituted, err := substituteArgs(query, args)
	if err != nil {
		return nil, err
	}

	sqlCtx := gmssql.NewEmptyContext()
	sqlCtx.SetCurrentDatabase(c.database)

	schema, iter, _, err := c.engine.Query(sqlCtx, substituted)
	if err != nil {
		return nil, &QueryExecError{Query: substituted, Err: err}
	}

	return &embeddedRows{
		ctx:    sqlCtx,
		schema: schema,
		iter:   iter,
	}, nil
}

// ExecContext executes a query that does not return rows.
func (c *embeddedConn) ExecContext(_ context.Context, query string, args []sqld.NamedValue) (sqld.Result, error) {
	if c.closed.Load() {
		return nil, &ConnClosedError{}
	}

	substituted, err := substituteArgs(query, args)
	if err != nil {
		return nil, err
	}

	sqlCtx := gmssql.NewEmptyContext()
	sqlCtx.SetCurrentDatabase(c.database)

	schema, iter, _, err := c.engine.Query(sqlCtx, substituted)
	if err != nil {
		return nil, &QueryExecError{Query: substituted, Err: err}
	}

	// Drain the iterator to count affected rows and close it.
	var affected int64
	_ = schema // unused for exec
	for {
		_, rowErr := iter.Next(sqlCtx)
		if rowErr == io.EOF {
			break
		}
		if rowErr != nil {
			_ = iter.Close(sqlCtx)
			return nil, &QueryExecError{Query: substituted, Err: rowErr}
		}
		affected++
	}
	_ = iter.Close(sqlCtx)

	return &result{rowsAffected: affected}, nil
}
