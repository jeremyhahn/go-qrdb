# Backup and Restore — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column.

**Raft mode only.** In standalone every capability here refuses with
`requires mode: raft` — REST 501, gRPC/QUIC `Unimplemented`.

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
{ "destination": "s3://acme-backups/qrdb",
  "compression": "zstd",
  "encryption": "aes256",
  "encryption_key": "<base64>",
  "metadata": { "schedule": "nightly" },
  "shard_ids": [1, 2, 3] }
```

`destination` is required; a local path or an object-store URI. `shard_ids`
empty means **all shards**. `encryption_key` is **required** when `encryption`
is not `none`.

Response — `BackupInfo`:

```json
{ "id": "bkp-20260819-0300", "timestamp": "2026-08-19T03:00:00Z",
  "size": 4823945216, "shard_count": 3, "status": "complete",
  "checksum": "sha256:…", "destination": "s3://acme-backups/qrdb",
  "compression": "zstd", "encryption": "aes256",
  "metadata": { "schedule": "nightly" },
  "duration": 184000000000, "error_message": "" }
```

`status`: `pending` · `running` · `complete` · `failed` (with `error_message`).
A failed create is **recorded**, not discarded, so it appears in `list`.

`GET /v1/backups/list` returns `BackupInfo` entries **sorted by timestamp
descending**.

`POST /v1/backups/restore` — `RestoreRequest`:

```json
{ "backup_id": "bkp-20260819-0300",
  "target_shards": [1, 2],
  "validate_first": true,
  "decryption_key": "<base64>" }
```

Response — `RestoreResult`:

```json
{ "backup_id": "bkp-20260819-0300", "shards_restored": 2,
  "duration": 96000000000,
  "errors": [ { "shard_id": 3, "message": "…" } ] }
```

**Restore reports per-shard errors** rather than collapsing a partial restore
into one failure.

`POST /v1/backups/{id}/verify` — `VerifyResult`:

```json
{ "valid": true, "checksum_match": true,
  "expected_checksum": "sha256:…", "actual_checksum": "sha256:…",
  "errors": [] }
```

## Compression and encryption

| Compression | Meaning |
|---|---|
| `none` | default |
| `zstd` | high ratio, fast decompression |
| `snappy` | fast compression, moderate ratio |

| Encryption | Meaning |
|---|---|
| `none` | default |
| `aes256` | AES-256-GCM, authenticated |

The checksum provides **integrity**; authenticated encryption provides
**authenticity**. They are different properties and only the first is on by
default.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| Empty destination / empty ID | 400 | `INVALID_ARGUMENT` |
| Unsupported compression or encryption type | 400 | `INVALID_ARGUMENT` |
| Encryption requested without a key | 400 | `INVALID_ARGUMENT` — **fail closed** |
| Encrypted backup restored without a decryption key | 400 | `INVALID_ARGUMENT` — **fail closed** |
| Backup not found | 404 | `NOT_FOUND` |
| Checksum mismatch under `validate_first` | 409 | `FAILED_PRECONDITION` — restore aborts before touching shards |
| Snapshot capture failed | 500 | `INTERNAL` — recorded as `status: failed` |
| Missing permission | 403 | `PERMISSION_DENIED` |
| Standalone mode | 501 | `UNIMPLEMENTED` — `requires mode: raft` |

## Pagination

`ListBackups` returns every backup, newest first, and is not paginated. The set
is bounded by operator retention, which is external to this API.

## Versioning

Backup REST paths are under `/v1/`. Nothing here is deprecated.
