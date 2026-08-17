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
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sqlquery "github.com/jeremyhahn/go-qrdb/pkg/sql/query"
)

// SQLQueryResult holds the result of a SQL query execution. It is the
// canonical service-layer result type; the alias keeps the Wails IPC
// surface name stable (columns/rows/rowCount/duration).
type SQLQueryResult = service.SQLQueryResult

// SQLBinding exposes SQL query execution to the Wails frontend. It
// delegates to the canonical SQL query service (pkg/sql/query), the same
// execution core used by the REST API's POST /sql/query handler.
type SQLBinding struct {
	logger *logger.Logger
	conn   *ConnectionBinding
	sqlSvc atomic.Pointer[service.SQLServicer]
}

// NewSQLBinding creates a new SQLBinding.
func NewSQLBinding(log *logger.Logger, conn *ConnectionBinding) *SQLBinding {
	return &SQLBinding{logger: log, conn: conn}
}

// SetServices sets the service layer interfaces needed for SQL execution.
// Called by EmbeddedServerBinding during startup.
func (b *SQLBinding) SetServices(kvSvc service.KVServicer, dbSvc service.DatabaseServicer) {
	if kvSvc == nil {
		b.sqlSvc.Store(nil)
		return
	}
	svc := sqlquery.NewService(kvSvc, dbSvc)
	b.sqlSvc.Store(&svc)
}

// ClearServices clears the service references. Called during shutdown.
func (b *SQLBinding) ClearServices() {
	b.sqlSvc.Store(nil)
}

// Execute runs a SQL query and returns the results. The underlying service
// scopes the provider to the database's engine type when the database
// service is available, so that only engine-appropriate tables are exposed.
func (b *SQLBinding) Execute(query string, dbName string) (*SQLQueryResult, error) {
	b.logger.Debug("Execute called", "query", query, "database", dbName)

	svcPtr := b.sqlSvc.Load()
	if svcPtr == nil {
		return nil, &SQLNotAvailableError{}
	}

	result, err := (*svcPtr).Execute(context.Background(), dbName, query)
	if err != nil {
		b.logger.Error("Execute failed", "query", query, "error", err)
		return nil, &SQLExecutionError{Query: query, Err: err}
	}

	b.logger.Debug("Execute completed", "rowCount", result.RowCount, "duration", result.Duration)
	return result, nil
}
