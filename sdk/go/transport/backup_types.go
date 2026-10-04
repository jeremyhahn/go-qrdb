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

package transport

import (
	"context"
	"time"
)

// ---------------------------------------------------------------------------
// Backup request/response types
// ---------------------------------------------------------------------------

// CompressionType identifies the compression algorithm for a backup.
type CompressionType string

const (
	// CompressionNone disables compression.
	CompressionNone CompressionType = "none"

	// CompressionZstd uses Zstandard compression.
	CompressionZstd CompressionType = "zstd"

	// CompressionSnappy uses Snappy compression.
	CompressionSnappy CompressionType = "snappy"
)

// EncryptionType identifies the encryption algorithm for a backup.
type EncryptionType string

const (
	// EncryptionNone disables encryption. It is never the default: a
	// caller gets it only by setting BackupRequest.Encryption to
	// EncryptionNone outright. The server logs and, where a compliance
	// audit logger is configured, audits that choice.
	EncryptionNone EncryptionType = "none"

	// EncryptionAES256 uses AES-256-GCM encryption. It is the server's
	// default when BackupRequest.Encryption is left empty, which then
	// requires BackupRequest.KeyRef (a key-file path for the built-in
	// "software" keywrap provider, or a hardware/cloud-KMS reference a
	// go-xkms-registered provider resolves).
	EncryptionAES256 EncryptionType = "aes256"
)

// BackupStatus describes the current state of a backup.
type BackupStatus string

const (
	// BackupStatusPending indicates the backup has been created but not
	// yet started.
	BackupStatusPending BackupStatus = "pending"

	// BackupStatusRunning indicates the backup is actively capturing
	// shard snapshots.
	BackupStatusRunning BackupStatus = "running"

	// BackupStatusComplete indicates the backup finished successfully.
	BackupStatusComplete BackupStatus = "complete"

	// BackupStatusFailed indicates the backup encountered an error.
	BackupStatusFailed BackupStatus = "failed"
)

// BackupRequest contains options for creating a backup.
type BackupRequest struct {
	// Destination is the storage path or URI where the backup will be
	// written (e.g., "/backups/daily" or "s3://bucket/prefix").
	Destination string `json:"destination"`

	// Compression selects the compression algorithm. Defaults to
	// CompressionNone when empty.
	Compression CompressionType `json:"compression,omitempty"`

	// Encryption selects the encryption algorithm. Left empty, the server
	// defaults it to EncryptionAES256 and then requires KeyRef: the
	// default is encrypted, not plaintext. A caller that wants plaintext
	// must set this to EncryptionNone outright rather than get it by
	// omission.
	Encryption EncryptionType `json:"encryption,omitempty"`

	// KeyProviderID names the registered keywrap.Provider to wrap this
	// backup's DEK with. Defaults to "software" when KeyRef is set and
	// this is left empty.
	KeyProviderID string `json:"key_provider_id,omitempty"`

	// KeyRef is the key reference the named provider resolves -- a
	// key-file path for the "software" provider. Required whenever
	// Encryption resolves to EncryptionAES256 (whether defaulted or named
	// explicitly): every KEK is referenced through a provider, never
	// passed as raw bytes through this struct.
	KeyRef string `json:"key_ref,omitempty"`

	// Metadata holds arbitrary user-supplied metadata tags.
	Metadata map[string]string `json:"metadata,omitempty"`

	// ShardIDs restricts the backup to the specified shards. An empty
	// slice backs up all shards.
	ShardIDs []uint64 `json:"shard_ids,omitempty"`
}

// BackupInfo contains metadata about a completed or in-progress backup.
type BackupInfo struct {
	// ID is the unique backup identifier.
	ID string `json:"id"`

	// Timestamp is the time the backup was initiated.
	Timestamp time.Time `json:"timestamp"`

	// Size is the total backup size in bytes.
	Size int64 `json:"size"`

	// ShardCount is the number of shards included in the backup.
	ShardCount int `json:"shard_count"`

	// Status describes the current state of the backup.
	Status BackupStatus `json:"status"`

	// Checksum is the SHA-256 checksum of the backup data.
	Checksum string `json:"checksum"`

	// Destination is the storage path or URI where the backup was written.
	Destination string `json:"destination"`

	// Compression is the compression algorithm used.
	Compression CompressionType `json:"compression"`

	// Encryption is the encryption algorithm used.
	Encryption EncryptionType `json:"encryption"`

	// KeyProviderID is the keywrap.Provider that wrapped this backup's DEK.
	// Empty for an EncryptionNone backup.
	KeyProviderID string `json:"key_provider_id,omitempty"`

	// Metadata holds the user-supplied metadata tags.
	Metadata map[string]string `json:"metadata,omitempty"`

	// Duration is how long the backup took to complete.
	Duration time.Duration `json:"duration,omitempty"`

	// ErrorMessage contains the failure reason when Status is
	// BackupStatusFailed.
	ErrorMessage string `json:"error_message,omitempty"`
}

// RestoreRequest contains options for restoring from a backup.
type RestoreRequest struct {
	// BackupID identifies the backup to restore from.
	BackupID string `json:"backup_id"`

	// TargetShards restricts the restore to the specified shards.
	// An empty slice restores all shards in the backup.
	TargetShards []uint64 `json:"target_shards,omitempty"`

	// ValidateFirst runs a verification check before restoring.
	ValidateFirst bool `json:"validate_first,omitempty"`

	// DecryptionKeyRef overrides the key reference stored in the backup's
	// own envelope -- useful when restoring on a different node, or after
	// the key file has moved. Left empty, the stored reference is used.
	// The provider actually used to decrypt is always whichever one the
	// backup was created under; this field never selects a different
	// provider than that.
	DecryptionKeyRef string `json:"decryption_key_ref,omitempty"`
}

// RestoreResult describes the outcome of a restore operation.
type RestoreResult struct {
	// BackupID is the backup that was restored.
	BackupID string `json:"backup_id"`

	// ShardsRestored is the number of shards that were restored.
	ShardsRestored int `json:"shards_restored"`

	// Duration is how long the restore took.
	Duration time.Duration `json:"duration"`

	// Errors contains per-shard error messages for partial failures.
	Errors []ShardError `json:"errors,omitempty"`
}

// ShardError describes a per-shard error during backup or restore.
type ShardError struct {
	// ShardID is the shard that encountered the error.
	ShardID uint64 `json:"shard_id"`

	// Message describes the failure.
	Message string `json:"message"`
}

// VerifyResult describes the outcome of a backup verification.
type VerifyResult struct {
	// Valid is true when the backup passed all integrity checks.
	Valid bool `json:"valid"`

	// ChecksumMatch is true when the stored checksum matches the
	// computed checksum.
	ChecksumMatch bool `json:"checksum_match"`

	// ExpectedChecksum is the checksum stored in the backup metadata.
	ExpectedChecksum string `json:"expected_checksum"`

	// ActualChecksum is the checksum computed during verification.
	ActualChecksum string `json:"actual_checksum"`

	// Errors contains integrity check failures.
	Errors []string `json:"errors,omitempty"`
}

// ListBackupsResponse holds the list of backups returned by a list
// operation.
type ListBackupsResponse struct {
	Backups []BackupInfo `json:"backups"`
}

// ---------------------------------------------------------------------------
// Backup client interface
// ---------------------------------------------------------------------------

// BackupClient provides backup and restore operations.
type BackupClient interface {
	// CreateBackup initiates a new backup with the specified options.
	CreateBackup(ctx context.Context, req *BackupRequest) (*BackupInfo, error)

	// ListBackups returns metadata for all available backups.
	ListBackups(ctx context.Context) ([]BackupInfo, error)

	// GetBackup returns metadata for a specific backup by ID.
	GetBackup(ctx context.Context, id string) (*BackupInfo, error)

	// DeleteBackup removes a backup by ID.
	DeleteBackup(ctx context.Context, id string) error

	// RestoreBackup restores data from a backup.
	RestoreBackup(ctx context.Context, req *RestoreRequest) (*RestoreResult, error)

	// VerifyBackup verifies the integrity of a backup by computing
	// and comparing checksums.
	VerifyBackup(ctx context.Context, id string) (*VerifyResult, error)
}
