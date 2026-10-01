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

package embedded

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sqlquery "github.com/jeremyhahn/go-qrdb/pkg/sql/query"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertions.
var (
	_ transport.SQLClient   = (*Transport)(nil)
	_ transport.SQLProvider = (*Transport)(nil)
)

// sqlSvc returns the transport's cached SQLServicer, building it on first
// use from t.kvSvc and t.dbSvc. Returns nil when either service is not
// configured.
//
// With a service registry (WithServiceRegistry) every query resolves the
// named database through it, as the network transports' SQL does: the
// database's own store, unprefixed, with its seal, ownership, quota and
// isolation applied. Without one the query reads t.kvSvc under the
// "<tenant>/<db>/" prefix.
func (t *Transport) sqlSvc() service.SQLServicer {
	t.sqlOnce.Do(func() {
		if t.kvSvc != nil && t.dbSvc != nil {
			t.sqlSvcCache = sqlquery.NewService(t.kvSvc, t.dbSvc, sqlquery.WithRegistry(t.registry))
		}
	})
	return t.sqlSvcCache
}

// Query executes an ad-hoc SQL query against a named database by
// delegating to the in-process SQL query service. Returns
// NilServiceError when no KV or database service has been configured.
func (t *Transport) Query(ctx context.Context, database, query string) (*transport.SQLQueryResult, error) {
	svc := t.sqlSvc()
	if svc == nil {
		return nil, &NilServiceError{Service: "SQLServicer"}
	}

	result, err := svc.Execute(ctx, database, query)
	if err != nil {
		return nil, err
	}

	return &transport.SQLQueryResult{
		Columns:  result.Columns,
		Rows:     result.Rows,
		RowCount: result.RowCount,
		Duration: result.Duration,
	}, nil
}

// SQLClient returns the embedded transport itself, which implements
// transport.SQLClient directly.
func (t *Transport) SQLClient() transport.SQLClient {
	return t
}
