# Backup and Restore — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-22
> Conflicts with this document are defects. Fix them here first.

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column.

**Raft mode only.** A standalone server is composed without the backup
service: gRPC, Unix and QUIC answer `Unimplemented`; REST answers 501
(`service not implemented: backup`) on `GET /v1/backups/list` and the
`/v1/backups/{id}` routes, and does not register `POST /v1/backups` or
`POST /v1/backups/restore`.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Create | `POST /v1/backups` | `BackupCreate` | `backup.Create` | `qrdb_backup_create` | `BackupClient.CreateBackup` | `backup:create` |
| List | `GET /v1/backups/list` | `BackupList` | `backup.List` | `qrdb_backup_list` | `ListBackups` | `backup:list` |
| Get | `GET /v1/backups/{id}` | `BackupGet` | `backup.Get` | `qrdb_backup_get` | `GetBackup` | `backup:read` |
| Delete | `DELETE /v1/backups/{id}` | `BackupDelete` | `backup.Delete` | `qrdb_backup_delete` | `DeleteBackup` | `backup:delete` |
| Restore | `POST /v1/backups/restore` | `BackupRestore` | `backup.Restore` | `qrdb_backup_restore` | `RestoreBackup` | `backup:restore` |
| Verify | `POST /v1/backups/{id}/verify` | `BackupVerify` | `backup.Verify` | `qrdb_backup_verify` | `VerifyBackup` | `backup:verify` |

All six are present on all six transports.

Restore is `POST /v1/backups/restore` rather than a method on the backup's own
path, because the request selects target shards and carries a decryption key —
it is an operation on the cluster, taking a backup as input.

## Schemas

`POST /v1/backups` — `BackupRequest`:

```json
{ "destination": "/backups/daily",
  "compression": "zstd",
  "encryption": "aes256",
  "key_provider_id": "software",
  "key_ref": "backup.key",
  "metadata": { "schedule": "nightly" },
  "shard_ids": [1, 2, 3] }
```

`destination` is required: a label recorded with the backup and bound into its
authenticated data; the bytes go to the server's backup sink. `compression` is
validated and recorded, not applied to shard data. `shard_ids`
empty means **all shards**. `encryption` left unset (or empty string on the
wire) defaults to `aes256` -- it is **not** plaintext by omission. In either
case (unset or explicitly `aes256`) `key_ref` is **required**; a caller that
wants plaintext must send `encryption: "none"` outright. `key_ref` is a
key-wrap provider reference (the bare NAME of a key file inside the
server's configured key directory for the built-in `"software"` provider,
never a path), never raw key bytes — see
[../key-wrapping/api.md](../key-wrapping/api.md) for the full schema this
feature adds.

Response — `BackupInfo`:

```json
{ "id": "3f8e2a1c-7b4d-4e9a-9c61-2d5f0b8a7e43", "timestamp": "2026-08-19T03:00:00Z",
  "size": 4823945216, "shard_count": 3, "status": "complete",
  "checksum": "9f2c…e41a", "destination": "/backups/daily",
  "compression": "zstd", "encryption": "aes256",
  "key_provider_id": "software",
  "metadata": { "schedule": "nightly" },
  "duration_ms": 184000 }
```

`checksum` is the hex-encoded SHA-256. `error_message` is present only on a
failed backup.

`status`: `pending` · `running` · `complete` · `failed` (with `error_message`).
A failed create is **recorded**, not discarded, so it appears in `list`.
`key_provider_id` is empty for a `"none"` backup.

`GET /v1/backups/list` returns `{ "backups": [BackupInfo, …], "total": n }`,
entries **sorted by timestamp descending**.

`POST /v1/backups/restore` — `RestoreRequest`:

```json
{ "backup_id": "3f8e2a1c-7b4d-4e9a-9c61-2d5f0b8a7e43",
  "target_shards": [1, 2],
  "validate_first": true,
  "decryption_key_ref": "backup.key" }
```

`allow_plaintext` (omitted above) is the caller's explicit opt-in to
restore a backup whose OWN stored state is plaintext
(`encryption: "none"`). Left unset (`false`), restoring such a backup is
refused with `BackupPlaintextRestoreNotAllowedError` before any shard is
touched -- never inferred from the backup's own metadata. See
[../key-wrapping/](../key-wrapping/architecture.md) for why: an attacker
who both flips a backup's stored `encryption` to `"none"` and deletes its
envelope object produces a backup that is internally consistent
plaintext, indistinguishable by stored state alone from a genuine one.

`decryption_key_ref` is optional and usually omitted: the backup's own
envelope records the key reference it was wrapped under, so restore
resolves and decrypts it with no key material supplied at all in the
common case. Supply it only to override that stored reference (the key
file moved, or restore runs on a different node) — see
[../key-wrapping/api.md](../key-wrapping/api.md).

Response — `RestoreResult`:

```json
{ "backup_id": "3f8e2a1c-7b4d-4e9a-9c61-2d5f0b8a7e43", "shards_restored": 2,
  "duration_ms": 96000,
  "errors": [ { "shard_id": 3, "message": "…" } ] }
```

**Restore authenticates and decrypts every target shard BEFORE importing any
of them**; a tampered/missing shard, a tampered manifest, or a stored-
metadata/artifact mismatch aborts the whole restore with one typed,
top-level error and imports nothing. `errors`/`shards_restored` above
report a narrower case: an OPERATIONAL import failure after every shard
has already passed verification, named per shard.

`POST /v1/backups/{id}/verify` — `VerifyResult`:

```json
{ "valid": true, "checksum_match": true,
  "expected_checksum": "9f2c…e41a", "actual_checksum": "9f2c…e41a" }
```

## Compression and encryption

`compression` accepts `none` (the default), `zstd` and `snappy`. The value is
validated and recorded in `BackupInfo`; shard data is stored uncompressed.

| Encryption | Meaning |
|---|---|
| `aes256` | AES-256-GCM, authenticated — the default when `encryption` is left unset |
| `none` | plaintext — only by explicit request; logged at WARN and, where a compliance audit logger is configured, recorded |

The checksum provides **integrity**; authenticated encryption provides
**authenticity**. Both are on by default.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| Empty destination / empty ID | 400 | `INVALID_ARGUMENT` |
| Unsupported compression or encryption type | 400 | `INVALID_ARGUMENT` |
| Default encryption (unset) and no key reference | 400 | `INVALID_ARGUMENT` — `BackupEncryptionRequiredError`, **fail closed** |
| Encryption explicitly requested without a key reference | 400 | `INVALID_ARGUMENT` — `BackupEncryptionKeyRequiredError`, **fail closed** |
| Unregistered `key_provider_id`, or wrong key reference, on restore | 400 | `INVALID_ARGUMENT` — `BackupDecryptionFailedError`, **fail closed** (see [../key-wrapping/api.md](../key-wrapping/api.md)) |
| Tampered shard ciphertext on restore | 400 | `INVALID_ARGUMENT` — `BackupVerificationFailedError` wrapping `BackupShardIntegrityError`, **fail closed** -- a DIFFERENT typed error from the row above; checked at a different layer (see [../key-wrapping/api.md](../key-wrapping/api.md)) |
| Plaintext backup restored without `allow_plaintext` | 400 | `INVALID_ARGUMENT` — `BackupPlaintextRestoreNotAllowedError`, **fail closed** |
| Backup not found | 404 | `NOT_FOUND` |
| Checksum mismatch under `validate_first` | 409 | `FAILED_PRECONDITION` — restore aborts before touching shards |
| Snapshot capture failed | 500 | `INTERNAL` — recorded as `status: failed` |
| Missing permission | 403 | `PERMISSION_DENIED` |
| Standalone mode | 501 on the routes it registers (see above) | `UNIMPLEMENTED` — `backup service is not configured` |

## Pagination

`ListBackups` returns every backup, newest first, and is not paginated. The set
is bounded by operator retention, which is external to this API.

## Versioning

Backup REST paths are under `/v1/`. Nothing here is deprecated.
