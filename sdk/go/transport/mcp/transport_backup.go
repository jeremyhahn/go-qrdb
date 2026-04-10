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

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.BackupClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// BackupClient implementation (pending MCP tool definitions)
// ---------------------------------------------------------------------------

// CreateBackup initiates a new backup with the specified options. This method
// is not yet implemented for the MCP transport and requires corresponding MCP
// tool definitions.
func (t *Transport) CreateBackup(ctx context.Context, req *transport.BackupRequest) (*transport.BackupInfo, error) {
	return nil, &transport.NotImplementedError{Method: "CreateBackup"}
}

// ListBackups returns metadata for all available backups. This method is not
// yet implemented for the MCP transport and requires corresponding MCP tool
// definitions.
func (t *Transport) ListBackups(ctx context.Context) ([]transport.BackupInfo, error) {
	return nil, &transport.NotImplementedError{Method: "ListBackups"}
}

// GetBackup returns metadata for a specific backup by ID. This method is not
// yet implemented for the MCP transport and requires corresponding MCP tool
// definitions.
func (t *Transport) GetBackup(ctx context.Context, id string) (*transport.BackupInfo, error) {
	return nil, &transport.NotImplementedError{Method: "GetBackup"}
}

// DeleteBackup removes a backup by ID. This method is not yet implemented for
// the MCP transport and requires corresponding MCP tool definitions.
func (t *Transport) DeleteBackup(ctx context.Context, id string) error {
	return &transport.NotImplementedError{Method: "DeleteBackup"}
}

// RestoreBackup restores data from a backup. This method is not yet
// implemented for the MCP transport and requires corresponding MCP tool
// definitions.
func (t *Transport) RestoreBackup(ctx context.Context, req *transport.RestoreRequest) (*transport.RestoreResult, error) {
	return nil, &transport.NotImplementedError{Method: "RestoreBackup"}
}

// VerifyBackup verifies the integrity of a backup by computing and comparing
// checksums. This method is not yet implemented for the MCP transport and
// requires corresponding MCP tool definitions.
func (t *Transport) VerifyBackup(ctx context.Context, id string) (*transport.VerifyResult, error) {
	return nil, &transport.NotImplementedError{Method: "VerifyBackup"}
}
