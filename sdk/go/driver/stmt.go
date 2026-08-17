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
	"database/sql/driver"
	"sync/atomic"
)

// Compile-time interface assertions.
var (
	_ driver.Stmt             = (*stmt)(nil)
	_ driver.StmtExecContext  = (*stmt)(nil)
	_ driver.StmtQueryContext = (*stmt)(nil)
)

// stmt implements driver.Stmt for embedded connections. It holds a reference
// to the connection and the query string; arguments are bound as typed
// expressions at execution time and the query text is never rewritten.
type stmt struct {
	conn  *embeddedConn
	query string

	// numInput is the placeholder count determined when the statement was
	// prepared, or -1 when the statement could not be parsed. It is computed
	// once rather than per call because NumInput is invoked on every
	// execution.
	numInput int

	closed atomic.Bool
}

// Close marks the statement as closed.
func (s *stmt) Close() error {
	s.closed.Store(true)
	return nil
}

// NumInput returns the number of placeholder parameters in the query, or -1
// when it is unknown, which tells database/sql to skip its argument-count
// check and let the engine report the problem.
func (s *stmt) NumInput() int {
	return s.numInput
}

// Exec executes a query that doesn't return rows using the legacy
// driver.Value arguments.
func (s *stmt) Exec(args []driver.Value) (driver.Result, error) {
	if s.closed.Load() {
		return nil, &StmtClosedError{}
	}
	named := valuesToNamed(args)
	return s.conn.ExecContext(context.Background(), s.query, named)
}

// Query executes a query that returns rows using the legacy
// driver.Value arguments.
func (s *stmt) Query(args []driver.Value) (driver.Rows, error) {
	if s.closed.Load() {
		return nil, &StmtClosedError{}
	}
	named := valuesToNamed(args)
	return s.conn.QueryContext(context.Background(), s.query, named)
}

// ExecContext executes a query that doesn't return rows.
func (s *stmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if s.closed.Load() {
		return nil, &StmtClosedError{}
	}
	return s.conn.ExecContext(ctx, s.query, args)
}

// QueryContext executes a query that returns rows.
func (s *stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if s.closed.Load() {
		return nil, &StmtClosedError{}
	}
	return s.conn.QueryContext(ctx, s.query, args)
}

// valuesToNamed converts legacy driver.Value args to NamedValue args.
func valuesToNamed(args []driver.Value) []driver.NamedValue {
	named := make([]driver.NamedValue, len(args))
	for i, v := range args {
		named[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return named
}

// networkStmt implements driver.Stmt for network connections.
type networkStmt struct {
	conn   *networkConn
	query  string
	closed atomic.Bool
}

// Close marks the statement as closed.
func (s *networkStmt) Close() error {
	s.closed.Store(true)
	return nil
}

// NumInput returns the number of placeholder parameters in the query.
func (s *networkStmt) NumInput() int {
	return countPlaceholders(s.query)
}

// Exec executes a query that doesn't return rows.
func (s *networkStmt) Exec(args []driver.Value) (driver.Result, error) {
	if s.closed.Load() {
		return nil, &StmtClosedError{}
	}
	named := valuesToNamed(args)
	return s.conn.ExecContext(context.Background(), s.query, named)
}

// Query executes a query that returns rows.
func (s *networkStmt) Query(args []driver.Value) (driver.Rows, error) {
	if s.closed.Load() {
		return nil, &StmtClosedError{}
	}
	named := valuesToNamed(args)
	return s.conn.QueryContext(context.Background(), s.query, named)
}

// ExecContext executes a query that doesn't return rows.
func (s *networkStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if s.closed.Load() {
		return nil, &StmtClosedError{}
	}
	return s.conn.ExecContext(ctx, s.query, args)
}

// QueryContext executes a query that returns rows.
func (s *networkStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if s.closed.Load() {
		return nil, &StmtClosedError{}
	}
	return s.conn.QueryContext(ctx, s.query, args)
}

// Compile-time interface assertions for networkStmt.
var (
	_ driver.Stmt             = (*networkStmt)(nil)
	_ driver.StmtExecContext  = (*networkStmt)(nil)
	_ driver.StmtQueryContext = (*networkStmt)(nil)
)
