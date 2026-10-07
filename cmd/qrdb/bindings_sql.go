//go:build qrdb_sql

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

package main

import (
	"context"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// SQLQueryResult holds the result of a SQL query execution. The alias keeps
// the Wails IPC surface name stable (columns/rows/rowCount/duration).
type SQLQueryResult = transport.SQLQueryResult

// SQLBinding exposes SQL query execution to the Wails frontend over the
// active connection's SDK SQL client, on whichever transport it uses.
type SQLBinding struct {
	logger *sdk.ConcreteLogger
	conn   *ConnectionBinding
}

// NewSQLBinding creates a new SQLBinding.
func NewSQLBinding(log *sdk.ConcreteLogger, conn *ConnectionBinding) *SQLBinding {
	return &SQLBinding{logger: log, conn: conn}
}

// Execute runs a SQL query against the named database and returns the
// results.
func (b *SQLBinding) Execute(query string, dbName string) (*SQLQueryResult, error) {
	b.logger.Debug("Execute called", "query", query, "database", dbName)

	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Execute failed", "error", err)
		return nil, err
	}

	result, err := cl.SQL().Query(context.Background(), dbName, query)
	if err != nil {
		b.logger.Error("Execute failed", "query", query, "error", err)
		return nil, &SQLExecutionError{Query: query, Err: err}
	}

	b.logger.Debug("Execute completed", "rowCount", result.RowCount, "duration", result.Duration)
	return result, nil
}
