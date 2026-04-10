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
	"bytes"
	"context"
	"database/sql"
	sqld "database/sql/driver"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
)

// Compile-time interface assertions.
var (
	_ sqld.Conn           = (*networkConn)(nil)
	_ sqld.ConnBeginTx    = (*networkConn)(nil)
	_ sqld.ExecerContext  = (*networkConn)(nil)
	_ sqld.QueryerContext = (*networkConn)(nil)
)

// networkConn implements database/sql/driver.Conn by forwarding SQL
// queries to a remote qrdb server via HTTP REST API.
type networkConn struct {
	client   *http.Client
	baseURL  string
	database string
	closed   atomic.Bool
}

// sqlRequest is the JSON payload sent to POST /v1/sql/query.
type sqlRequest struct {
	Query    string `json:"query"`
	Database string `json:"database"`
}

// sqlResponse is the JSON payload returned by POST /v1/sql/query.
type sqlResponse struct {
	Columns  []string                 `json:"columns"`
	Rows     []map[string]interface{} `json:"rows"`
	RowCount int                      `json:"rowCount"`
	Duration string                   `json:"duration"`
	Error    string                   `json:"error,omitempty"`
}

// Prepare returns a prepared statement for the given query.
func (c *networkConn) Prepare(query string) (sqld.Stmt, error) {
	if c.closed.Load() {
		return nil, &ConnClosedError{}
	}
	return &networkStmt{conn: c, query: query}, nil
}

// Close marks the connection as closed.
func (c *networkConn) Close() error {
	c.closed.Store(true)
	return nil
}

// Begin starts a transaction with default options.
func (c *networkConn) Begin() (sqld.Tx, error) {
	return c.BeginTx(context.Background(), sqld.TxOptions{})
}

// BeginTx returns a no-op transaction. Network mode uses auto-commit
// semantics; only default and serializable isolation levels are accepted.
func (c *networkConn) BeginTx(_ context.Context, opts sqld.TxOptions) (sqld.Tx, error) {
	if c.closed.Load() {
		return nil, &ConnClosedError{}
	}
	if sql.IsolationLevel(opts.Isolation) != sql.LevelDefault &&
		sql.IsolationLevel(opts.Isolation) != sql.LevelSerializable {
		return nil, &UnsupportedIsolationError{Level: sql.IsolationLevel(opts.Isolation)}
	}
	return &networkTx{}, nil
}

// QueryContext executes a SQL query and returns the result rows.
func (c *networkConn) QueryContext(ctx context.Context, query string, args []sqld.NamedValue) (sqld.Rows, error) {
	if c.closed.Load() {
		return nil, &ConnClosedError{}
	}

	substituted, err := substituteArgs(query, args)
	if err != nil {
		return nil, err
	}

	resp, err := c.executeSQL(ctx, substituted)
	if err != nil {
		return nil, err
	}

	return newNetworkRows(resp), nil
}

// ExecContext executes a SQL statement and returns the result.
func (c *networkConn) ExecContext(ctx context.Context, query string, args []sqld.NamedValue) (sqld.Result, error) {
	if c.closed.Load() {
		return nil, &ConnClosedError{}
	}

	substituted, err := substituteArgs(query, args)
	if err != nil {
		return nil, err
	}

	resp, err := c.executeSQL(ctx, substituted)
	if err != nil {
		return nil, err
	}

	return &result{rowsAffected: int64(resp.RowCount)}, nil
}

// executeSQL sends a SQL query to the remote server via POST /v1/sql/query.
func (c *networkConn) executeSQL(ctx context.Context, query string) (*sqlResponse, error) {
	url := c.baseURL + "/v1/sql/query"

	payload, err := json.Marshal(&sqlRequest{
		Query:    query,
		Database: c.database,
	})
	if err != nil {
		return nil, &QueryExecError{Query: query, Err: err}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, &NetworkRequestError{URL: url, Err: err}
	}
	req.Header.Set("Content-Type", "application/json")

	httpResp, err := c.client.Do(req)
	if err != nil {
		return nil, &NetworkRequestError{URL: url, Err: err}
	}
	defer func() { _ = httpResp.Body.Close() }()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, &NetworkRequestError{URL: url, StatusCode: httpResp.StatusCode, Err: err}
	}

	if httpResp.StatusCode != http.StatusOK {
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return nil, &NetworkRequestError{
				URL:        url,
				StatusCode: httpResp.StatusCode,
				Err:        &NetworkResponseError{Message: errResp.Error},
			}
		}
		return nil, &NetworkRequestError{
			URL:        url,
			StatusCode: httpResp.StatusCode,
			Err:        &NetworkResponseError{Message: string(body)},
		}
	}

	var resp sqlResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, &NetworkRequestError{URL: url, StatusCode: httpResp.StatusCode, Err: err}
	}

	return &resp, nil
}
