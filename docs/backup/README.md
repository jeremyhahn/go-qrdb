# Backup and Restore Service

Shard-level backup and restore with compression, encryption, and integrity verification.

## Overview

The QRDB Backup Service provides point-in-time shard snapshots with SHA-256 integrity verification. Backups support optional compression (Zstandard, Snappy) and encryption (AES-256-GCM). The service operates through a `ShardSnapshotProvider` interface, decoupling backup logic from the storage and Raft layers.

## Key Features

| Feature | Description |
|---------|-------------|
| **Shard Snapshots** | Point-in-time capture of individual or all shards |
| **Compression** | None, Zstandard (zstd), or Snappy |
| **Encryption** | None or AES-256-GCM |
| **Integrity Verification** | SHA-256 checksum computation and validation |
| **Selective Backup** | Back up specific shards or all shards |
| **Selective Restore** | Restore specific shards or all shards from a backup |
| **Pre-Restore Validation** | Optional integrity check before restoring |
| **Backup Lifecycle** | Create, list, get, verify, and delete backups |

## API Reference

### CreateBackup

Creates a new backup by capturing shard snapshots.

```go
func (s *BackupService) CreateBackup(ctx context.Context, opts BackupOptions) (*BackupInfo, error)
```

**BackupOptions:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `Destination` | `string` | Yes | Storage path or URI (e.g., `/backups/daily`, `s3://bucket/prefix`) |
| `Compression` | `CompressionType` | No | Compression algorithm. Defaults to `none` |
| `Encryption` | `EncryptionType` | No | Encryption algorithm. Defaults to `none` |
| `EncryptionKey` | `[]byte` | Conditional | Required when Encryption is not `none` |
| `Metadata` | `map[string]string` | No | Arbitrary user-supplied tags |
| `ShardIDs` | `[]uint64` | No | Specific shards to back up. Empty backs up all shards |

**Returns:** `*BackupInfo` with backup metadata including ID, checksum, size, and status.

**Errors:**
- `BackupEmptyDestinationError` -- destination is empty
- `BackupInvalidCompressionError` -- unsupported compression type
- `BackupInvalidEncryptionError` -- unsupported encryption type
- `BackupEncryptionKeyRequiredError` -- encryption enabled but no key provided
- `BackupCreateError` -- snapshot capture failed

### ListBackups

Returns metadata for all backups, sorted by timestamp descending (newest first).

```go
func (s *BackupService) ListBackups(ctx context.Context) ([]BackupInfo, error)
```

### GetBackup

Returns metadata for a specific backup by ID.

```go
func (s *BackupService) GetBackup(ctx context.Context, id string) (*BackupInfo, error)
```

**Errors:**
- `BackupEmptyIDError` -- ID is empty
- `BackupNotFoundError` -- no backup exists with the given ID

### DeleteBackup

Removes a backup by ID.

```go
func (s *BackupService) DeleteBackup(ctx context.Context, id string) error
```

**Errors:**
- `BackupEmptyIDError` -- ID is empty
- `BackupNotFoundError` -- no backup exists with the given ID

### RestoreBackup

Restores shard data from a backup. Supports selective shard restore and optional pre-restore verification.

```go
func (s *BackupService) RestoreBackup(ctx context.Context, opts RestoreOptions) (*RestoreResult, error)
```

**RestoreOptions:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `BackupID` | `string` | Yes | Backup to restore from |
| `TargetShards` | `[]uint64` | No | Specific shards to restore. Empty restores all |
| `ValidateFirst` | `bool` | No | Run integrity check before restoring |
| `DecryptionKey` | `[]byte` | Conditional | Required when the backup was encrypted |

**Returns:** `*RestoreResult` with shard count restored, duration, and per-shard errors.

**Errors:**
- `BackupEmptyIDError` -- BackupID is empty
- `BackupNotFoundError` -- no backup exists with the given ID
- `BackupDecryptionKeyRequiredError` -- encrypted backup but no decryption key
- `BackupChecksumMismatchError` -- validation failed (when `ValidateFirst` is true)
- `BackupRestoreError` -- context cancelled or verification failed

### VerifyBackup

Verifies backup integrity by recomputing the SHA-256 checksum and comparing it to the stored value.

```go
func (s *BackupService) VerifyBackup(ctx context.Context, id string) (*VerifyResult, error)
```

**Returns:** `*VerifyResult` with validity status, checksum comparison, and error details.

**Errors:**
- `BackupEmptyIDError` -- ID is empty
- `BackupNotFoundError` -- no backup exists with the given ID

## Configuration Options

### Compression

| Value | Constant | Description |
|-------|----------|-------------|
| `none` | `CompressionNone` | No compression (default) |
| `zstd` | `CompressionZstd` | Zstandard -- high ratio, fast decompression |
| `snappy` | `CompressionSnappy` | Snappy -- fast compression, moderate ratio |

### Encryption

| Value | Constant | Description |
|-------|----------|-------------|
| `none` | `EncryptionNone` | No encryption (default) |
| `aes256` | `EncryptionAES256` | AES-256-GCM authenticated encryption |

### Backup Status

| Value | Description |
|-------|-------------|
| `pending` | Created but not yet started |
| `running` | Actively capturing shard snapshots |
| `complete` | Finished successfully |
| `failed` | Encountered an error (see `ErrorMessage`) |

## SDK Usage

### Create a Backup

```go
ctx := context.Background()

info, err := client.Backup().CreateBackup(ctx, &transport.BackupRequest{
    Destination: "/backups/daily",
    Compression: transport.CompressionZstd,
    Encryption:  transport.EncryptionAES256,
    EncryptionKey: []byte("my-32-byte-encryption-key-here!!"),
    Metadata: map[string]string{
        "env":     "production",
        "created": "2026-03-07",
    },
})
if err != nil {
    log.Fatal(err)
}
fmt.Printf("Backup %s: %d shards, %d bytes\n", info.ID, info.ShardCount, info.Size)
```

### List and Inspect Backups

```go
backups, err := client.Backup().ListBackups(ctx)
if err != nil {
    log.Fatal(err)
}
for _, b := range backups {
    fmt.Printf("%s  %s  %s  %d shards\n", b.ID, b.Timestamp, b.Status, b.ShardCount)
}
```

### Verify and Restore

```go
// Verify integrity first
result, err := client.Backup().VerifyBackup(ctx, backupID)
if err != nil {
    log.Fatal(err)
}
if !result.Valid {
    log.Fatalf("Backup corrupt: %v", result.Errors)
}

// Restore with pre-validation
restore, err := client.Backup().RestoreBackup(ctx, &transport.RestoreRequest{
    BackupID:      backupID,
    ValidateFirst: true,
    DecryptionKey: []byte("my-32-byte-encryption-key-here!!"),
})
if err != nil {
    log.Fatal(err)
}
fmt.Printf("Restored %d shards in %s\n", restore.ShardsRestored, restore.Duration)
```

### Selective Shard Backup

```go
info, err := client.Backup().CreateBackup(ctx, &transport.BackupRequest{
    Destination: "/backups/partial",
    ShardIDs:    []uint64{1, 2, 5},
    Compression: transport.CompressionSnappy,
})
```

### Delete a Backup

```go
err := client.Backup().DeleteBackup(ctx, backupID)
```

## Architecture

```
Client -> BackupClient (SDK) -> Transport -> BackupService -> ShardSnapshotProvider
                                                                  |
                                                    +-------------+-------------+
                                                    |             |             |
                                                 Shard 1       Shard 2      Shard N
                                                (snapshot)    (snapshot)   (snapshot)
                                                    |             |             |
                                                    +------+------+------+------+
                                                           |
                                                     SHA-256 Checksum
                                                           |
                                                     Backup Registry
```

The `ShardSnapshotProvider` interface decouples the backup service from storage internals:

```go
type ShardSnapshotProvider interface {
    AvailableShards(ctx context.Context) ([]uint64, error)
    SnapshotShard(ctx context.Context, shardID uint64) ([]byte, error)
    RestoreShard(ctx context.Context, shardID uint64, data []byte) error
}
```
