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

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertions.
var (
	_ sqld.Conn           = (*networkConn)(nil)
	_ sqld.ConnBeginTx    = (*networkConn)(nil)
	_ sqld.ExecerContext  = (*networkConn)(nil)
	_ sqld.QueryerContext = (*networkConn)(nil)
	_ sqld.Validator      = (*networkConn)(nil)
)

// networkConn implements database/sql/driver.Conn by forwarding statements to
// a remote qrdb server through an SDK transport.
//
// The remote SQL surface accepts a statement and returns a result set. It
// carries no bind parameters, no affected-row count, no insert id, and no
// session handle, so this connection supports fewer capabilities than the
// embedded one and says so explicitly rather than approximating them.
type networkConn struct {
	sqlClient transport.SQLClient
	database  string
	scheme    string
	closed    atomic.Bool
}

// Prepare returns a prepared statement for the given query.
func (c *networkConn) Prepare(query string) (sqld.Stmt, error) {
	if c.closed.Load() {
		return nil, &ConnClosedError{}
	}
	return &networkStmt{conn: c, query: query}, nil
}

// Close marks the connection as closed. The transport is owned by the
// connector and outlives individual connections.
func (c *networkConn) Close() error {
	c.closed.Store(true)
	return nil
}

// IsValid reports whether the connection may be reused from the pool.
func (c *networkConn) IsValid() bool {
	return !c.closed.Load()
}

// Begin starts a new transaction with default options.
func (c *networkConn) Begin() (sqld.Tx, error) {
	return c.BeginTx(context.Background(), sqld.TxOptions{})
}

// BeginTx reports that transactions are unavailable over this transport.
//
// The remote SQL API executes one statement per request with no session
// affinity, so there is nothing for BEGIN to attach to. Returning a handle
// that silently discards COMMIT and ROLLBACK — which this connection
// previously did — makes a rollback look like it succeeded while changing
// nothing, so the request is refused instead.
func (c *networkConn) BeginTx(_ context.Context, opts sqld.TxOptions) (sqld.Tx, error) {
	if c.closed.Load() {
		return nil, &ConnClosedError{}
	}
	if level := sql.IsolationLevel(opts.Isolation); level != sql.LevelDefault {
		return nil, &UnsupportedIsolationError{Level: level}
	}
	return nil, &TransactionUnsupportedError{Scheme: c.scheme}
}

// QueryContext executes a query and returns the result rows.
func (c *networkConn) QueryContext(ctx context.Context, query string, args []sqld.NamedValue) (sqld.Rows, error) {
	if c.closed.Load() {
		return nil, &ConnClosedError{}
	}
	if len(args) > 0 {
		return nil, &ParameterizedQueryUnsupportedError{Scheme: c.scheme, Count: len(args)}
	}

	result, err := c.sqlClient.Query(ctx, c.database, query)
	if err != nil {
		return nil, &QueryExecError{Query: query, Err: err}
	}

	return newNetworkRows(result), nil
}

// ExecContext executes a statement that does not return rows.
func (c *networkConn) ExecContext(ctx context.Context, query string, args []sqld.NamedValue) (sqld.Result, error) {
	if c.closed.Load() {
		return nil, &ConnClosedError{}
	}
	if len(args) > 0 {
		return nil, &ParameterizedQueryUnsupportedError{Scheme: c.scheme, Count: len(args)}
	}

	if _, err := c.sqlClient.Query(ctx, c.database, query); err != nil {
		return nil, &QueryExecError{Query: query, Err: err}
	}

	// The wire response carries a row count, not an affected-row count, and no
	// insert id. Reporting the row count as RowsAffected would be wrong for
	// every DML statement, so the result declares the metadata unavailable.
	return &result{unsupported: true}, nil
}
