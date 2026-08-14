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

package quic

import (
	"context"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.DatabaseClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// DatabaseClient implementation
// ---------------------------------------------------------------------------

// CreateDatabase creates a new database with the specified engine and
// storage backend by sending a JSON-encoded request over the QUIC wire
// protocol to the db.Create handler.
func (t *Transport) CreateDatabase(ctx context.Context, req transport.CreateDatabaseRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	respBytes, err := t.call(ctx, "db.Create", payload)
	if err != nil {
		return nil, err
	}

	var info transport.DatabaseInfo
	if err := json.Unmarshal(respBytes, &info); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &info, nil
}

// GetDatabase returns information about a specific database by sending
// a JSON-encoded name request to the db.Get handler.
func (t *Transport) GetDatabase(ctx context.Context, name string) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(struct {
		Name string `json:"name"`
	}{Name: name})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	respBytes, err := t.call(ctx, "db.Get", payload)
	if err != nil {
		return nil, err
	}

	var info transport.DatabaseInfo
	if err := json.Unmarshal(respBytes, &info); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &info, nil
}

// ListDatabases returns information about all databases by calling the
// db.List handler.
func (t *Transport) ListDatabases(ctx context.Context) ([]transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	respBytes, err := t.call(ctx, "db.List", nil)
	if err != nil {
		return nil, err
	}

	var infos []transport.DatabaseInfo
	if err := json.Unmarshal(respBytes, &infos); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return infos, nil
}

// DropDatabase removes a database by name by calling the db.Drop handler.
func (t *Transport) DropDatabase(ctx context.Context, name string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload, err := json.Marshal(struct {
		Name string `json:"name"`
	}{Name: name})
	if err != nil {
		return &marshalError{Err: err}
	}

	_, err = t.call(ctx, "db.Drop", payload)
	return err
}

// UpdateDatabaseProtocol updates the wire protocol configuration for a
// database by calling the db.UpdateProtocol handler.
func (t *Transport) UpdateDatabaseProtocol(ctx context.Context, req transport.UpdateDatabaseProtocolRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(struct {
		Name       string `json:"name"`
		MySQL      bool   `json:"mysql"`
		PostgreSQL bool   `json:"postgresql"`
	}{
		Name:       req.Name,
		MySQL:      req.MySQL,
		PostgreSQL: req.PostgreSQL,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	respBytes, err := t.call(ctx, "db.UpdateProtocol", payload)
	if err != nil {
		return nil, err
	}

	var info transport.DatabaseInfo
	if err := json.Unmarshal(respBytes, &info); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &info, nil
}

// UpdateDatabaseEtcd updates the etcd wire protocol configuration for a
// database by calling the db.UpdateEtcd handler.
func (t *Transport) UpdateDatabaseEtcd(ctx context.Context, req transport.UpdateDatabaseEtcdRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(struct {
		Name                string   `json:"name"`
		Enabled             bool     `json:"enabled"`
		ListenAddr          string   `json:"listenAddr"`
		AdvertiseClientURLs []string `json:"advertiseClientUrls,omitempty"`
	}{
		Name:                req.Name,
		Enabled:             req.Enabled,
		ListenAddr:          req.ListenAddr,
		AdvertiseClientURLs: req.AdvertiseClientURLs,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	respBytes, err := t.call(ctx, "db.UpdateEtcd", payload)
	if err != nil {
		return nil, err
	}

	var info transport.DatabaseInfo
	if err := json.Unmarshal(respBytes, &info); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &info, nil
}

// UpdateDatabaseRaft updates the Raft consensus configuration for a
// database by calling the db.UpdateRaft handler.
func (t *Transport) UpdateDatabaseRaft(ctx context.Context, req transport.UpdateDatabaseRaftRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(struct {
		Name string                        `json:"name"`
		Raft *transport.DatabaseRaftConfig `json:"raft"`
	}{
		Name: req.Name,
		Raft: req.Raft,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	respBytes, err := t.call(ctx, "db.UpdateRaft", payload)
	if err != nil {
		return nil, err
	}

	var info transport.DatabaseInfo
	if err := json.Unmarshal(respBytes, &info); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &info, nil
}

// ScaleDatabase changes the replication factor of a dedicated-raft database
// by calling the db.Scale handler.
func (t *Transport) ScaleDatabase(ctx context.Context, req transport.ScaleDatabaseRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(struct {
		Name           string `json:"name"`
		TargetReplicas int    `json:"targetReplicas"`
	}{
		Name:           req.Name,
		TargetReplicas: req.TargetReplicas,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	respBytes, err := t.call(ctx, "db.Scale", payload)
	if err != nil {
		return nil, err
	}

	var info transport.DatabaseInfo
	if err := json.Unmarshal(respBytes, &info); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &info, nil
}

// UpdateDatabaseBarrier updates the encryption barrier configuration for
// a database by calling the db.UpdateBarrier handler.
func (t *Transport) UpdateDatabaseBarrier(ctx context.Context, req transport.UpdateDatabaseBarrierRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(struct {
		Name    string                           `json:"name"`
		Barrier *transport.DatabaseBarrierConfig `json:"barrier"`
	}{
		Name:    req.Name,
		Barrier: req.Barrier,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	respBytes, err := t.call(ctx, "db.UpdateBarrier", payload)
	if err != nil {
		return nil, err
	}

	var info transport.DatabaseInfo
	if err := json.Unmarshal(respBytes, &info); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &info, nil
}

// UpdateDatabaseConnection updates the remote connection configuration
// for a database by calling the db.UpdateConnection handler.
func (t *Transport) UpdateDatabaseConnection(ctx context.Context, req transport.UpdateDatabaseConnectionRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(struct {
		Name       string                              `json:"name"`
		Connection *transport.DatabaseConnectionConfig `json:"connection"`
	}{
		Name:       req.Name,
		Connection: req.Connection,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	respBytes, err := t.call(ctx, "db.UpdateConnection", payload)
	if err != nil {
		return nil, err
	}

	var info transport.DatabaseInfo
	if err := json.Unmarshal(respBytes, &info); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &info, nil
}
