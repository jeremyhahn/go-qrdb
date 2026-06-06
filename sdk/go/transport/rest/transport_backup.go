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
	"net/http"
	"net/url"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.BackupClient = (*Transport)(nil)

// restBackupInfo is the on-wire JSON shape returned by the REST backup
// handlers. Timestamps are RFC3339 strings and durations are integer
// milliseconds; we lift these into the transport.BackupInfo contract.
type restBackupInfo struct {
	ID           string            `json:"id"`
	Timestamp    string            `json:"timestamp"`
	Size         int64             `json:"size"`
	ShardCount   int               `json:"shard_count"`
	Status       string            `json:"status"`
	Checksum     string            `json:"checksum"`
	Destination  string            `json:"destination"`
	Compression  string            `json:"compression"`
	Encryption   string            `json:"encryption"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	DurationMs   int64             `json:"duration_ms"`
	ErrorMessage string            `json:"error_message,omitempty"`
}

// toBackupInfo converts the on-wire shape to the transport contract.
func (r *restBackupInfo) toBackupInfo() *transport.BackupInfo {
	info := &transport.BackupInfo{
		ID:           r.ID,
		Size:         r.Size,
		ShardCount:   r.ShardCount,
		Status:       transport.BackupStatus(r.Status),
		Checksum:     r.Checksum,
		Destination:  r.Destination,
		Compression:  transport.CompressionType(r.Compression),
		Encryption:   transport.EncryptionType(r.Encryption),
		Metadata:     r.Metadata,
		Duration:     time.Duration(r.DurationMs) * time.Millisecond,
		ErrorMessage: r.ErrorMessage,
	}
	if r.Timestamp != "" {
		if ts, err := time.Parse(time.RFC3339, r.Timestamp); err == nil {
			info.Timestamp = ts
		}
	}
	return info
}

// restListBackupsResponse is the on-wire JSON shape for GET /v1/backups/list.
type restListBackupsResponse struct {
	Backups []restBackupInfo `json:"backups"`
	Total   int              `json:"total"`
}

// restRestoreResult is the on-wire JSON shape for POST /v1/backups/restore.
type restRestoreResult struct {
	BackupID       string `json:"backup_id"`
	ShardsRestored int    `json:"shards_restored"`
	DurationMs     int64  `json:"duration_ms"`
	Errors         []struct {
		ShardID uint64 `json:"shard_id"`
		Message string `json:"message"`
	} `json:"errors,omitempty"`
}

// CreateBackup captures a new backup. Maps to POST /v1/backups.
func (t *Transport) CreateBackup(ctx context.Context, req *transport.BackupRequest) (*transport.BackupInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var wire restBackupInfo
	if err := t.postJSONDecode(ctx, "CreateBackup", "/v1/backups", req, &wire); err != nil {
		return nil, err
	}
	return wire.toBackupInfo(), nil
}

// ListBackups returns metadata for all backups. Maps to GET /v1/backups/list.
func (t *Transport) ListBackups(ctx context.Context) ([]transport.BackupInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var wire restListBackupsResponse
	if err := t.getJSONDecode(ctx, "ListBackups", "/v1/backups/list", &wire); err != nil {
		return nil, err
	}
	out := make([]transport.BackupInfo, len(wire.Backups))
	for i := range wire.Backups {
		out[i] = *wire.Backups[i].toBackupInfo()
	}
	return out, nil
}

// GetBackup returns metadata for a backup by ID. Maps to GET /v1/backups/{id}.
func (t *Transport) GetBackup(ctx context.Context, id string) (*transport.BackupInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var wire restBackupInfo
	if err := t.getJSONDecode(ctx, "GetBackup", "/v1/backups/"+url.PathEscape(id), &wire); err != nil {
		return nil, err
	}
	return wire.toBackupInfo(), nil
}

// DeleteBackup removes a backup by ID. Maps to DELETE /v1/backups/{id}.
func (t *Transport) DeleteBackup(ctx context.Context, id string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return t.deleteNoContent(ctx, "DeleteBackup", "/v1/backups/"+url.PathEscape(id))
}

// RestoreBackup restores shards from a backup. Maps to POST /v1/backups/restore.
func (t *Transport) RestoreBackup(ctx context.Context, req *transport.RestoreRequest) (*transport.RestoreResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var wire restRestoreResult
	if err := t.postJSONDecode(ctx, "RestoreBackup", "/v1/backups/restore", req, &wire); err != nil {
		return nil, err
	}
	errs := make([]transport.ShardError, len(wire.Errors))
	for i := range wire.Errors {
		errs[i] = transport.ShardError{ShardID: wire.Errors[i].ShardID, Message: wire.Errors[i].Message}
	}
	return &transport.RestoreResult{
		BackupID:       wire.BackupID,
		ShardsRestored: wire.ShardsRestored,
		Duration:       time.Duration(wire.DurationMs) * time.Millisecond,
		Errors:         errs,
	}, nil
}

// VerifyBackup verifies a backup's integrity. Maps to POST /v1/backups/{id}/verify.
func (t *Transport) VerifyBackup(ctx context.Context, id string) (*transport.VerifyResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var result transport.VerifyResult
	if err := t.postJSONDecode(ctx, "VerifyBackup", "/v1/backups/"+url.PathEscape(id)+"/verify", struct{}{}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// getJSONDecode issues a GET to the given path and decodes the JSON response
// body into dest.
func (t *Transport) getJSONDecode(ctx context.Context, method, path string, dest any) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+path, nil)
	if err != nil {
		return &transport.RequestError{Method: method, Err: err}
	}
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return &transport.RequestError{Method: method, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return t.decodeErrorResponse(method, resp)
	}
	if dest != nil {
		if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
			return &transport.RequestError{Method: method, Err: &jsonDecodeError{Err: err}}
		}
	}
	return nil
}

// deleteNoContent issues a DELETE to the given path and expects a 2xx with no
// response body.
func (t *Transport) deleteNoContent(ctx context.Context, method, path string) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.baseURL+path, nil)
	if err != nil {
		return &transport.RequestError{Method: method, Err: err}
	}
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return &transport.RequestError{Method: method, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return t.decodeErrorResponse(method, resp)
	}
	return nil
}
