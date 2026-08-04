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

// Package query provides the canonical service.SQLServicer implementation:
// ad-hoc SQL query execution over the service-layer KV surface via the
// go-mysql-server engine. It is the single execution core shared by the
// REST API handler (pkg/api/rest) and the Wails desktop SQL binding
// (cmd/qrdb). It lives under pkg/sql rather than pkg/service because
// pkg/sql/provider imports pkg/service.
package query

import (
	"context"
	"fmt"
	"io"
	"time"

	sqle "github.com/dolthub/go-mysql-server"
	"github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// sqlService implements service.SQLServicer over the provider + engine
// composition. It is unexported; construct it with NewService.
type sqlService struct {
	kvSvc service.KVServicer
	dbSvc service.DatabaseServicer
}

// Compile-time interface assertion.
var _ service.SQLServicer = (*sqlService)(nil)

// NewService creates the canonical SQL query service backed by the given
// KV service. When dbSvc is non-nil, each query resolves the target
// database through it so that only engine-appropriate tables are exposed
// and unknown databases fail with the resolver's typed not-found error.
// When dbSvc is nil (legacy single-database mode), queries run unscoped
// with all tables visible.
func NewService(kvSvc service.KVServicer, dbSvc service.DatabaseServicer) service.SQLServicer {
	return &sqlService{kvSvc: kvSvc, dbSvc: dbSvc}
}

// Execute runs query against the named database and returns the result set.
func (s *sqlService) Execute(ctx context.Context, dbName, query string) (*service.SQLQueryResult, error) {
	if query == "" {
		return nil, &service.SQLEmptyQueryError{}
	}
	if dbName == "" {
		return nil, &service.SQLEmptyDatabaseError{}
	}

	// Resolve the database engine for table scoping. The resolver's typed
	// error (e.g. *database.NotFoundError) is returned unwrapped so that
	// transports can map it to their not-found status.
	var engineType database.EngineType
	if s.dbSvc != nil {
		info, err := s.dbSvc.Get(dbName)
		if err != nil {
			return nil, err
		}
		engineType = info.Engine
	}

	// Create an engine-scoped provider and SQL engine for this execution.
	prov := provider.NewProviderWithEngine(dbName, s.kvSvc, engineType)
	eng := engine.New(engine.Config{Provider: prov})
	defer func() { _ = eng.Close() }()

	start := time.Now()
	result, err := run(ctx, eng, dbName, query)
	if err != nil {
		return nil, &service.SQLExecutionError{Query: query, Err: err}
	}
	result.Duration = time.Since(start).String()
	return result, nil
}

// run creates a SQL context bound to the caller's context, executes the
// query, and materializes the result rows.
func run(ctx context.Context, eng *sqle.Engine, dbName, query string) (*service.SQLQueryResult, error) {
	sqlCtx := sql.NewContext(ctx)
	sqlCtx.SetCurrentDatabase(dbName)

	schema, iter, _, err := eng.Query(sqlCtx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = iter.Close(sqlCtx) }()

	// Build column names from the schema.
	columns := make([]string, len(schema))
	for i, col := range schema {
		columns[i] = col.Name
	}

	rows := []map[string]any{}
	for {
		row, err := iter.Next(sqlCtx)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		rowMap := make(map[string]any, len(columns))
		for i, col := range columns {
			rowMap[col] = formatValue(row[i])
		}
		rows = append(rows, rowMap)
	}

	return &service.SQLQueryResult{
		Columns:  columns,
		Rows:     rows,
		RowCount: len(rows),
	}, nil
}

// formatValue converts a SQL row cell value to a string suitable for JSON
// serialization. Byte slices are decoded as UTF-8 strings (so wire clients
// receive plaintext rather than base64), nil cells become empty strings,
// and all other types are formatted with fmt.Sprintf.
func formatValue(v any) string {
	switch val := v.(type) {
	case []byte:
		return string(val)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", val)
	}
}
