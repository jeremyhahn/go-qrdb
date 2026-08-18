# Backup and Restore — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Exported surface

`sdk/go/transport`:

```go
type BackupClient interface {
	CreateBackup(ctx context.Context, req *BackupRequest) (*BackupInfo, error)
	ListBackups(ctx context.Context) ([]BackupInfo, error)
	GetBackup(ctx context.Context, id string) (*BackupInfo, error)
	DeleteBackup(ctx context.Context, id string) error
	RestoreBackup(ctx context.Context, req *RestoreRequest) (*RestoreResult, error)
	VerifyBackup(ctx context.Context, id string) (*VerifyResult, error)
}

type CompressionType string
const (
	CompressionNone   CompressionType = "none"    // default
	CompressionZstd   CompressionType = "zstd"    // high ratio, fast decompression
	CompressionSnappy CompressionType = "snappy"  // fast compression, moderate ratio
)

type EncryptionType string
const (
	EncryptionNone   EncryptionType = "none"      // default
	EncryptionAES256 EncryptionType = "aes256"    // AES-256-GCM, authenticated
)

type BackupStatus string
const (
	BackupStatusPending  BackupStatus = "pending"   // created, not started
	BackupStatusRunning  BackupStatus = "running"   // capturing shard snapshots
	BackupStatusComplete BackupStatus = "complete"
	BackupStatusFailed   BackupStatus = "failed"    // see ErrorMessage
)

type BackupRequest struct {
	Destination   string            `json:"destination"`              // required
	Compression   CompressionType   `json:"compression,omitempty"`
	Encryption    EncryptionType    `json:"encryption,omitempty"`
	EncryptionKey []byte            `json:"encryption_key,omitempty"` // required when Encryption != none
	Metadata      map[string]string `json:"metadata,omitempty"`
	ShardIDs      []uint64          `json:"shard_ids,omitempty"`      // empty = all shards
}

type BackupInfo struct {
	ID           string            `json:"id"`
	Timestamp    time.Time         `json:"timestamp"`
	Size         int64             `json:"size"`
	ShardCount   int               `json:"shard_count"`
	Status       BackupStatus      `json:"status"`
	Checksum     string            `json:"checksum"`
	Destination  string            `json:"destination"`
	Compression  CompressionType   `json:"compression"`
	Encryption   EncryptionType    `json:"encryption"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	Duration     time.Duration     `json:"duration,omitempty"`
	ErrorMessage string            `json:"error_message,omitempty"`
}

type RestoreRequest struct {
	BackupID      string   `json:"backup_id"`                // required
	TargetShards  []uint64 `json:"target_shards,omitempty"`  // empty = all
	ValidateFirst bool     `json:"validate_first,omitempty"`
	DecryptionKey []byte   `json:"decryption_key,omitempty"` // required for encrypted backups
}

type RestoreResult struct {
	BackupID       string        `json:"backup_id"`
	ShardsRestored int           `json:"shards_restored"`
	Duration       time.Duration `json:"duration"`
	Errors         []ShardError  `json:"errors,omitempty"`
}
type ShardError struct {
	ShardID uint64 `json:"shard_id"`
	Message string `json:"message"`
}

type VerifyResult struct {
	Valid            bool     `json:"valid"`
	ChecksumMatch    bool     `json:"checksum_match"`
	ExpectedChecksum string   `json:"expected_checksum"`
	ActualChecksum   string   `json:"actual_checksum"`
	Errors           []string `json:"errors,omitempty"`
}
```

## Invariants

- **`ListBackups` is sorted by timestamp descending** — newest first.
- **Empty `ShardIDs` means all shards; empty `TargetShards` means all shards.**
  There is no separate "everything" flag.
- **Verification recomputes SHA-256 and compares it to the stored checksum.**
  `VerifyResult` reports both values, so a mismatch is diagnosable rather than a
  bare boolean.
- **Encryption is conditional, and the condition is enforced in both
  directions**: `Encryption != none` without `EncryptionKey` is refused, and
  restoring an encrypted backup without `DecryptionKey` is refused. No path
  silently writes or reads plaintext.
- **`ValidateFirst` runs verification before touching shard state.** A checksum
  mismatch aborts the restore rather than half-applying it.
- **Restore reports per-shard errors.** `RestoreResult.Errors` is a
  `[]ShardError`, so a partial restore names exactly which shards failed and
  `ShardsRestored` counts the ones that succeeded.
- **Restore overwrites shard state.** It is the most destructive operation the
  API offers.
- **Backups are full, never incremental.** Each capture is complete for the
  shards it names.
- **Shard backup and restore require raft mode.** In standalone they refuse with
  `requires mode: raft`.

## Permission model

| Permission | Gates |
|---|---|
| `backup:create` | creating a backup |
| `backup:list` | listing backups |
| `backup:read` | reading one backup's metadata |
| `backup:verify` | verifying integrity |
| `backup:delete` | deleting a backup |
| `backup:restore` | **restoring** — separate because it overwrites shard state |

## Error semantics

| Error type | Condition | Fails closed? |
|---|---|---|
| `BackupEmptyDestinationError` | `Destination` empty on create | yes |
| `BackupInvalidCompressionError` | unsupported compression type | yes |
| `BackupInvalidEncryptionError` | unsupported encryption type | yes |
| `BackupEncryptionKeyRequiredError` | encryption enabled, no key | **yes** |
| `BackupCreateError` | snapshot capture failed | yes |
| `BackupEmptyIDError` | ID empty on get/delete/verify/restore | yes |
| `BackupNotFoundError` | no backup with that ID | yes |
| `BackupDecryptionKeyRequiredError` | encrypted backup, no decryption key | **yes** |
| `BackupChecksumMismatchError` | `ValidateFirst` verification failed | **yes** — restore aborts |
| `BackupRestoreError` | context cancelled, or verification failed | **yes** |

A failed *create* records `Status: failed` with `ErrorMessage` rather than
disappearing, so a failed backup is visible in `list` instead of being an
absence somebody has to notice.

## Configuration

No dedicated config section. Destinations are per request:

- Local path: `/backups/daily`
- Object store URI: `s3://bucket/prefix`

Cloud destinations require the corresponding go-objstore build tag
(`WITH_AWS_S3`, `WITH_MINIO`, `WITH_GCP_STORAGE`, `WITH_AZURE_BLOB`,
`WITH_GLACIER`, `WITH_AZURE_ARCHIVE`).

`EncryptionKey` and `DecryptionKey` are key material and should be resolved
through the secret-reference SPI rather than passed as literals where the
deployment allows — see [../credentials/](../credentials/).

## Persistence

A backup is a set of shard snapshots plus a manifest holding the ID, timestamp,
size, shard count, checksum, destination, compression, encryption and metadata.
The checksum covers the captured payload.

**Compatibility**: the shard snapshot format is the compatibility surface.
Restoring onto a materially different topology is not a supported migration
path.

## Test strategy

- **Unit**: create with each compression and encryption combination, the
  key-required refusals in both directions, checksum computation and mismatch
  detection, selective shard capture and restore, per-shard error reporting,
  and `ListBackups` ordering.
- **The two key-required refusals are security tests.** They are what stop a
  backup being written in plaintext by omission.
- **Integration**: `backup_restore_test.go` lives in the **cluster stage**
  (`make integration-test-cluster`), which is **not currently wired into CI** —
  so backup and restore are not exercised end to end by CI today. This is a
  known gap.

## Observability

- `BackupInfo.Status` (`pending`/`running`/`complete`/`failed`) plus
  `ErrorMessage` and `Duration` are the operational signals.
- `VerifyResult` reports expected and actual checksums, not just validity.
- `RestoreResult.ShardsRestored` and `Errors` give a precise post-restore
  picture.
- Create, delete and restore are audited through [../audit/](../audit/).
