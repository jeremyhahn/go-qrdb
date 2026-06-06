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

package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.BackupClient = (*Transport)(nil)

// mcpBackupInfo mirrors the JSON emitted by the MCP backup tools, which
// marshal the service-layer BackupInfo directly (PascalCase field names, no
// json tags). Duration is encoded as integer nanoseconds.
type mcpBackupInfo struct {
	ID           string
	Timestamp    time.Time
	Size         int64
	ShardCount   int
	Status       string
	Checksum     string
	Destination  string
	Compression  string
	Encryption   string
	Metadata     map[string]string
	Duration     time.Duration
	ErrorMessage string
}

// toBackupInfo converts the MCP wire shape to the transport contract.
func (m *mcpBackupInfo) toBackupInfo() *transport.BackupInfo {
	return &transport.BackupInfo{
		ID:           m.ID,
		Timestamp:    m.Timestamp,
		Size:         m.Size,
		ShardCount:   m.ShardCount,
		Status:       transport.BackupStatus(m.Status),
		Checksum:     m.Checksum,
		Destination:  m.Destination,
		Compression:  transport.CompressionType(m.Compression),
		Encryption:   transport.EncryptionType(m.Encryption),
		Metadata:     m.Metadata,
		Duration:     m.Duration,
		ErrorMessage: m.ErrorMessage,
	}
}

// CreateBackup captures a new backup via the qrdb_backup_create tool.
func (t *Transport) CreateBackup(ctx context.Context, req *transport.BackupRequest) (*transport.BackupInfo, error) {
	args := map[string]any{"destination": req.Destination}
	if req.Compression != "" {
		args["compression"] = string(req.Compression)
	}
	if req.Encryption != "" {
		args["encryption"] = string(req.Encryption)
	}
	if len(req.EncryptionKey) > 0 {
		args["encryption_key"] = base64.StdEncoding.EncodeToString(req.EncryptionKey)
	}
	if len(req.ShardIDs) > 0 {
		args["shard_ids"] = req.ShardIDs
	}

	text, err := t.callTool(ctx, "qrdb_backup_create", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "CreateBackup", Err: err}
	}
	var info mcpBackupInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CreateBackup", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return info.toBackupInfo(), nil
}

// ListBackups returns metadata for all backups via the qrdb_backup_list tool.
func (t *Transport) ListBackups(ctx context.Context) ([]transport.BackupInfo, error) {
	text, err := t.callTool(ctx, "qrdb_backup_list", map[string]any{})
	if err != nil {
		return nil, &transport.RequestError{Method: "ListBackups", Err: err}
	}
	var infos []mcpBackupInfo
	if jsonErr := json.Unmarshal([]byte(text), &infos); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ListBackups", Err: &jsonDecodeError{Err: jsonErr}}
	}
	out := make([]transport.BackupInfo, len(infos))
	for i := range infos {
		out[i] = *infos[i].toBackupInfo()
	}
	return out, nil
}

// GetBackup returns metadata for a backup by ID via the qrdb_backup_get tool.
func (t *Transport) GetBackup(ctx context.Context, id string) (*transport.BackupInfo, error) {
	text, err := t.callTool(ctx, "qrdb_backup_get", map[string]any{"id": id})
	if err != nil {
		return nil, &transport.RequestError{Method: "GetBackup", Err: err}
	}
	var info mcpBackupInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		return nil, &transport.RequestError{Method: "GetBackup", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return info.toBackupInfo(), nil
}

// DeleteBackup removes a backup by ID via the qrdb_backup_delete tool.
func (t *Transport) DeleteBackup(ctx context.Context, id string) error {
	if _, err := t.callTool(ctx, "qrdb_backup_delete", map[string]any{"id": id}); err != nil {
		return &transport.RequestError{Method: "DeleteBackup", Err: err}
	}
	return nil
}

// RestoreBackup restores shards via the qrdb_backup_restore tool.
func (t *Transport) RestoreBackup(ctx context.Context, req *transport.RestoreRequest) (*transport.RestoreResult, error) {
	args := map[string]any{"backup_id": req.BackupID}
	if len(req.TargetShards) > 0 {
		args["target_shards"] = req.TargetShards
	}
	if req.ValidateFirst {
		args["validate_first"] = true
	}
	if len(req.DecryptionKey) > 0 {
		args["decryption_key"] = base64.StdEncoding.EncodeToString(req.DecryptionKey)
	}

	text, err := t.callTool(ctx, "qrdb_backup_restore", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "RestoreBackup", Err: err}
	}
	var wire struct {
		BackupID       string
		ShardsRestored int
		Duration       time.Duration
		Errors         []struct {
			ShardID uint64
			Message string
		}
	}
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		return nil, &transport.RequestError{Method: "RestoreBackup", Err: &jsonDecodeError{Err: jsonErr}}
	}
	errs := make([]transport.ShardError, len(wire.Errors))
	for i := range wire.Errors {
		errs[i] = transport.ShardError{ShardID: wire.Errors[i].ShardID, Message: wire.Errors[i].Message}
	}
	return &transport.RestoreResult{
		BackupID:       wire.BackupID,
		ShardsRestored: wire.ShardsRestored,
		Duration:       wire.Duration,
		Errors:         errs,
	}, nil
}

// VerifyBackup verifies a backup's integrity via the qrdb_backup_verify tool.
func (t *Transport) VerifyBackup(ctx context.Context, id string) (*transport.VerifyResult, error) {
	text, err := t.callTool(ctx, "qrdb_backup_verify", map[string]any{"id": id})
	if err != nil {
		return nil, &transport.RequestError{Method: "VerifyBackup", Err: err}
	}
	var wire struct {
		Valid            bool
		ChecksumMatch    bool
		ExpectedChecksum string
		ActualChecksum   string
		Errors           []string
	}
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		return nil, &transport.RequestError{Method: "VerifyBackup", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return &transport.VerifyResult{
		Valid:            wire.Valid,
		ChecksumMatch:    wire.ChecksumMatch,
		ExpectedChecksum: wire.ExpectedChecksum,
		ActualChecksum:   wire.ActualChecksum,
		Errors:           wire.Errors,
	}, nil
}
