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

package rest

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertions.
var (
	_ transport.SQLClient   = (*Transport)(nil)
	_ transport.SQLProvider = (*Transport)(nil)
)

// sqlQueryRequest is the JSON payload for POST /v1/sql/query.
type sqlQueryRequest struct {
	Query    string `json:"query"`
	Database string `json:"database"`
}

// Query executes an ad-hoc SQL query against a named database. Maps to
// POST /v1/sql/query -- the mount used by the Go database/sql driver and
// this SDK (the TypeScript SDK uses the canonical /sql/query mount; both
// dispatch to the same handler).
func (t *Transport) Query(ctx context.Context, database, query string) (*transport.SQLQueryResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var result transport.SQLQueryResult
	req := sqlQueryRequest{Query: query, Database: database}
	if err := t.postJSONDecode(ctx, "Query", "/v1/sql/query", req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// SQLClient returns the REST transport itself, which implements
// transport.SQLClient directly.
func (t *Transport) SQLClient() transport.SQLClient {
	return t
}
