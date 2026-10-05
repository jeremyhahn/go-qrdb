# Backup and Restore — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-03

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
	EncryptionNone   EncryptionType = "none"      // plaintext; only by explicit request
	EncryptionAES256 EncryptionType = "aes256"    // AES-256-GCM, authenticated -- the default when Encryption is left empty
)

type BackupStatus string
const (
	BackupStatusPending  BackupStatus = "pending"   // created, not started
	BackupStatusRunning  BackupStatus = "running"   // capturing shard snapshots
	BackupStatusComplete BackupStatus = "complete"
	BackupStatusFailed   BackupStatus = "failed"    // see ErrorMessage
)

type BackupRequest struct {
	Destination   string            `json:"destination"`                 // required
	Compression   CompressionType   `json:"compression,omitempty"`
	Encryption    EncryptionType    `json:"encryption,omitempty"`        // empty defaults to aes256 server-side, NOT none
	KeyProviderID string            `json:"key_provider_id,omitempty"`   // the keywrap.Provider to wrap this backup's DEK with; defaults to "software" when KeyRef is set
	KeyRef        string            `json:"key_ref,omitempty"`          // the key reference the named provider resolves; required unless Encryption is explicitly none -- see ../key-wrapping/
	Metadata      map[string]string `json:"metadata,omitempty"`
	ShardIDs      []uint64          `json:"shard_ids,omitempty"`         // empty = all shards
}

type BackupInfo struct {
	ID            string            `json:"id"`
	Timestamp     time.Time         `json:"timestamp"`
	Size          int64             `json:"size"`
	ShardCount    int               `json:"shard_count"`
	Status        BackupStatus      `json:"status"`
	Checksum      string            `json:"checksum"`
	Destination   string            `json:"destination"`
	Compression   CompressionType   `json:"compression"`
	Encryption    EncryptionType    `json:"encryption"`
	KeyProviderID string            `json:"key_provider_id,omitempty"` // empty for a "none" backup -- see ../key-wrapping/
	Metadata      map[string]string `json:"metadata,omitempty"`
	Duration      time.Duration     `json:"duration,omitempty"`
	ErrorMessage  string            `json:"error_message,omitempty"`
}

type RestoreRequest struct {
	BackupID         string   `json:"backup_id"`                   // required
	TargetShards     []uint64 `json:"target_shards,omitempty"`     // empty = all
	ValidateFirst    bool     `json:"validate_first,omitempty"`
	DecryptionKeyRef string   `json:"decryption_key_ref,omitempty"` // overrides the key reference stored in the backup's own envelope; usually left empty -- see ../key-wrapping/
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
- **`aes256` is the default, not `none`.** `Encryption` left empty defaults to
  `EncryptionAES256`; it is never silently treated as plaintext. Plaintext is
  reachable only by setting `Encryption` to `EncryptionNone` explicitly, and
  that choice is logged at WARN and, where a compliance audit logger is
  configured, recorded in the audit trail.
- **Encryption on create is conditional, and the condition is enforced**: a
  request with `Encryption` empty or `aes256` and no `KeyRef` is refused
  (`BackupEncryptionRequiredError` when empty, `BackupEncryptionKeyRequiredError`
  when `aes256` was named explicitly). No path silently writes plaintext.
  `KeyRef` is a provider-resolved reference (the bare NAME of a key file
  inside the server's configured key directory for the built-in
  `"software"` provider, never a path), never raw key bytes — see
  [../key-wrapping/](../key-wrapping/architecture.md) for the full envelope
  design and the owner ruling that retired the inline-bytes path this
  document originally shipped with.
- **Restore needs no key material at all, in the common case.** The
  backup's own envelope records the key reference it was wrapped under, so
  `RestoreRequest.DecryptionKeyRef` left empty still resolves and decrypts
  correctly; it exists only to override that stored reference (the key
  file moved, or restore runs on a different node).
- **`ValidateFirst` runs verification before touching shard state.** A checksum
  mismatch aborts the restore rather than half-applying it.
- **Restore authenticates and decrypts every target shard before importing
  any of them.** A tampered or missing shard, a tampered manifest (see
  [../key-wrapping/architecture.md](../key-wrapping/architecture.md#envelope-format)),
  or a stored-metadata/artifact mismatch all abort the WHOLE restore with
  one typed `*BackupVerificationFailedError`/`*BackupMetadataMismatchError`
  and import nothing -- this is what closes the defect a security review of
  this feature found: decrypting and importing shard by shard meant a
  single tampered shard aborted only that shard, leaving every shard
  already imported in place with no top-level error.
- **Restore reports per-shard errors only for an OPERATIONAL import
  failure.** `RestoreResult.Errors` (`[]ShardError`) is populated only once
  every target shard has already passed verification and the underlying
  store refuses the import for some of them -- a narrower case than the
  bullet above, and `ShardsRestored` counts the ones that succeeded.
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

Every `backup:*` permission is **cross-scope** (`ScopeCross`, declared in
`pkg/permission/cluster_admin_scope.go`): only an identity of the `system`
tenant may exercise it. A backup is taken of shards by id, not of a tenant --
the backup service has no tenant model -- so one backup holds every tenant's
data on those shards, and a restore overwrites every tenant's state. The
routes name no tenant, so as `ScopeSelf` they admitted any holder in any
tenant; no built-in role below Owner carries `backup:*`, but a tenant's SO
holds `roles:create` and `roles:assign` and could grant them to itself.
`TestClusterAdmin_TenantGrantedPermissionsAreDenied` in `pkg/auth/rbac`
proves the refusal.

## Error semantics

| Error type | Condition | Fails closed? |
|---|---|---|
| `BackupEmptyDestinationError` | `Destination` empty on create | yes |
| `BackupInvalidCompressionError` | unsupported compression type | yes |
| `BackupInvalidEncryptionError` | unsupported encryption type | yes |
| `BackupEncryptionRequiredError` | `Encryption` left empty, no key | **yes** |
| `BackupEncryptionKeyRequiredError` | `Encryption` named explicitly, no key | **yes** |
| `BackupCreateError` | snapshot capture failed | yes |
| `BackupEmptyIDError` | ID empty on get/delete/verify/restore | yes |
| `BackupNotFoundError` | no backup with that ID | yes |
| `BackupDecryptionFailedError` | an unregistered `KeyProviderID` (create or restore), or a bad/wrong key reference — see [../key-wrapping/](../key-wrapping/engineering.md) | **yes** |
| `BackupVerificationFailedError` | a target shard failed verification before import -- wraps `BackupShardIntegrityError` (tampered shard ciphertext, a DIFFERENT error from `BackupDecryptionFailedError` above) or `BackupShardNotFoundError` | **yes** |
| `BackupPlaintextRestoreNotAllowedError` | restoring a plaintext-stored backup without the caller's explicit `AllowPlaintext`/`allow_plaintext` opt-in | **yes** |
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

`KeyRef`/`DecryptionKeyRef` name key material by reference (the bare NAME of
a key file inside the server's configured key directory for the built-in
`"software"` keywrap provider, never a path); they are never raw key
bytes — see [../key-wrapping/](../key-wrapping/architecture.md).

## Persistence

A backup is a set of shard snapshots plus a manifest holding the ID, timestamp,
size, shard count, checksum, destination, compression, encryption and metadata.
The checksum covers the captured payload.

**Compatibility**: the shard snapshot format is the compatibility surface.
Restoring onto a materially different topology is not a supported migration
path.

## Test strategy

- **Unit**: create with each compression and encryption combination, the
  default-with-no-key refusal, the explicit-encryption-with-no-key refusal,
  the wrong-key-reference refusal (`*BackupDecryptionFailedError`) and the
  tampered-shard-ciphertext refusal (the DIFFERENT
  `*BackupVerificationFailedError` wrapping `*BackupShardIntegrityError` —
  see [../key-wrapping/engineering.md](../key-wrapping/engineering.md)),
  the plaintext-restore-without-opt-in refusal
  (`*BackupPlaintextRestoreNotAllowedError`), checksum computation and
  mismatch detection, selective shard capture and restore, per-shard error
  reporting, and `ListBackups` ordering.
- **The encryption-required refusals are security tests.** They are what stop
  a backup being written in plaintext by omission -- the default is
  encrypted, and getting plaintext takes an explicit, visible choice.
- **Conformance**: per-transport coverage of the three encryption scenarios
  (default with no key refused, default with a key encrypts and restores,
  explicit `none` writes plaintext and warns/audits) lives in
  `sdk/go/conformance`.
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
