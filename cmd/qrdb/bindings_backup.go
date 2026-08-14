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

package main

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// BackupBinding exposes backup and restore operations to the Wails frontend.
type BackupBinding struct {
	logger *logger.Logger
	conn   *ConnectionBinding
}

// NewBackupBinding creates a new BackupBinding backed by the given connection.
func NewBackupBinding(log *logger.Logger, conn *ConnectionBinding) *BackupBinding {
	return &BackupBinding{logger: log, conn: conn}
}

// Create initiates a new backup with the specified options.
func (b *BackupBinding) Create(req *transport.BackupRequest) (*transport.BackupInfo, error) {
	b.logger.Debug("Create called", "destination", req.Destination)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Create failed", "error", err)
		return nil, err
	}
	bc, err := backupClient(cl)
	if err != nil {
		b.logger.Error("Create failed", "error", err)
		return nil, err
	}
	info, err := bc.CreateBackup(context.Background(), req)
	if err != nil {
		b.logger.Error("Create failed", "destination", req.Destination, "error", err)
		return nil, err
	}
	b.logger.Debug("Create completed", "backupID", info.ID, "status", info.Status)
	return info, nil
}

// List returns metadata for all available backups.
func (b *BackupBinding) List() ([]transport.BackupInfo, error) {
	b.logger.Debug("List called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("List failed", "error", err)
		return nil, err
	}
	bc, err := backupClient(cl)
	if err != nil {
		b.logger.Error("List failed", "error", err)
		return nil, err
	}
	backups, err := bc.ListBackups(context.Background())
	if err != nil {
		b.logger.Error("List failed", "error", err)
		return nil, err
	}
	b.logger.Debug("List completed", "count", len(backups))
	return backups, nil
}

// Get returns metadata for a specific backup by ID.
func (b *BackupBinding) Get(id string) (*transport.BackupInfo, error) {
	b.logger.Debug("Get called", "id", id)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Get failed", "error", err)
		return nil, err
	}
	bc, err := backupClient(cl)
	if err != nil {
		b.logger.Error("Get failed", "error", err)
		return nil, err
	}
	info, err := bc.GetBackup(context.Background(), id)
	if err != nil {
		b.logger.Error("Get failed", "id", id, "error", err)
		return nil, err
	}
	b.logger.Debug("Get completed", "id", id, "status", info.Status)
	return info, nil
}

// Delete removes a backup by ID.
func (b *BackupBinding) Delete(id string) error {
	b.logger.Debug("Delete called", "id", id)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Delete failed", "error", err)
		return err
	}
	bc, err := backupClient(cl)
	if err != nil {
		b.logger.Error("Delete failed", "error", err)
		return err
	}
	if err := bc.DeleteBackup(context.Background(), id); err != nil {
		b.logger.Error("Delete failed", "id", id, "error", err)
		return err
	}
	b.logger.Debug("Delete completed", "id", id)
	return nil
}

// Restore restores data from a backup.
func (b *BackupBinding) Restore(req *transport.RestoreRequest) (*transport.RestoreResult, error) {
	b.logger.Debug("Restore called", "backupID", req.BackupID)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Restore failed", "error", err)
		return nil, err
	}
	bc, err := backupClient(cl)
	if err != nil {
		b.logger.Error("Restore failed", "error", err)
		return nil, err
	}
	result, err := bc.RestoreBackup(context.Background(), req)
	if err != nil {
		b.logger.Error("Restore failed", "backupID", req.BackupID, "error", err)
		return nil, err
	}
	b.logger.Debug("Restore completed", "backupID", result.BackupID, "shardsRestored", result.ShardsRestored)
	return result, nil
}

// Verify checks the integrity of a backup by computing and comparing checksums.
func (b *BackupBinding) Verify(id string) (*transport.VerifyResult, error) {
	b.logger.Debug("Verify called", "id", id)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Verify failed", "error", err)
		return nil, err
	}
	bc, err := backupClient(cl)
	if err != nil {
		b.logger.Error("Verify failed", "error", err)
		return nil, err
	}
	result, err := bc.VerifyBackup(context.Background(), id)
	if err != nil {
		b.logger.Error("Verify failed", "id", id, "error", err)
		return nil, err
	}
	b.logger.Debug("Verify completed", "id", id, "valid", result.Valid)
	return result, nil
}
