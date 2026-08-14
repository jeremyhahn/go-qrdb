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
	"encoding/json"
	"io"
	"net/http"
	"net/url"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.DatabaseClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// DatabaseClient implementation
// ---------------------------------------------------------------------------

// CreateDatabase creates a new database with the specified engine and storage
// backend. Maps to POST /v1/databases with a JSON request body.
func (t *Transport) CreateDatabase(ctx context.Context, req transport.CreateDatabaseRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var info transport.DatabaseInfo
	if err := t.postJSONDecode(ctx, "CreateDatabase", "/v1/databases", req, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// GetDatabase returns information about a specific database by name.
// Maps to GET /v1/databases/{name}. Returns DatabaseNotFoundError when
// the server responds with 404.
func (t *Transport) GetDatabase(ctx context.Context, name string) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/v1/databases/" + url.PathEscape(name)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetDatabase", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetDatabase", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, &transport.DatabaseNotFoundError{Name: name}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("GetDatabase", resp)
	}

	var info transport.DatabaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, &transport.RequestError{Method: "GetDatabase", Err: &jsonDecodeError{Err: err}}
	}

	return &info, nil
}

// ListDatabases returns information about all databases.
// Maps to GET /v1/databases/list.
func (t *Transport) ListDatabases(ctx context.Context) ([]transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/v1/databases/list"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListDatabases", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListDatabases", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("ListDatabases", resp)
	}

	var listResp transport.ListDatabasesResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return nil, &transport.RequestError{Method: "ListDatabases", Err: &jsonDecodeError{Err: err}}
	}

	return listResp.Databases, nil
}

// UpdateDatabaseProtocol updates the wire protocol configuration for a
// database. Maps to PATCH /v1/databases/{name}/protocol.
func (t *Transport) UpdateDatabaseProtocol(ctx context.Context, req transport.UpdateDatabaseProtocolRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var info transport.DatabaseInfo
	if err := t.patchJSONDecode(ctx, "UpdateDatabaseProtocol", "/v1/databases/"+url.PathEscape(req.Name)+"/protocol", req, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// UpdateDatabaseEtcd updates the etcd wire protocol configuration for a
// database. Maps to PATCH /v1/databases/{name}/etcd.
func (t *Transport) UpdateDatabaseEtcd(ctx context.Context, req transport.UpdateDatabaseEtcdRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var info transport.DatabaseInfo
	if err := t.patchJSONDecode(ctx, "UpdateDatabaseEtcd", "/v1/databases/"+url.PathEscape(req.Name)+"/etcd", req, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// DropDatabase removes a database by name. Maps to DELETE /v1/databases/{name}.
func (t *Transport) DropDatabase(ctx context.Context, name string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/v1/databases/" + url.PathEscape(name)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, reqURL, nil)
	if err != nil {
		return &transport.RequestError{Method: "DropDatabase", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return &transport.RequestError{Method: "DropDatabase", Err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}

	return t.decodeErrorResponse("DropDatabase", resp)
}

// UpdateDatabaseRaft updates the Raft consensus configuration for a database.
// Maps to PATCH /v1/databases/{name}/raft.
func (t *Transport) UpdateDatabaseRaft(ctx context.Context, req transport.UpdateDatabaseRaftRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var info transport.DatabaseInfo
	if err := t.patchJSONDecode(ctx, "UpdateDatabaseRaft", "/v1/databases/"+url.PathEscape(req.Name)+"/raft", req.Raft, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// ScaleDatabase changes the replication factor of a dedicated-raft database.
// Maps to PATCH /v1/databases/{name}/scale.
func (t *Transport) ScaleDatabase(ctx context.Context, req transport.ScaleDatabaseRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var info transport.DatabaseInfo
	if err := t.patchJSONDecode(ctx, "ScaleDatabase", "/v1/databases/"+url.PathEscape(req.Name)+"/scale", req, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// UpdateDatabaseBarrier updates the encryption barrier configuration for a
// database. Maps to PATCH /v1/databases/{name}/barrier.
func (t *Transport) UpdateDatabaseBarrier(ctx context.Context, req transport.UpdateDatabaseBarrierRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var info transport.DatabaseInfo
	if err := t.patchJSONDecode(ctx, "UpdateDatabaseBarrier", "/v1/databases/"+url.PathEscape(req.Name)+"/barrier", req.Barrier, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// UpdateDatabaseConnection updates the remote connection configuration for a
// database. Maps to PATCH /v1/databases/{name}/connection.
func (t *Transport) UpdateDatabaseConnection(ctx context.Context, req transport.UpdateDatabaseConnectionRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var info transport.DatabaseInfo
	if err := t.patchJSONDecode(ctx, "UpdateDatabaseConnection", "/v1/databases/"+url.PathEscape(req.Name)+"/connection", req.Connection, &info); err != nil {
		return nil, err
	}
	return &info, nil
}
