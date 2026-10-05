# Backup and Restore — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-03

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
  "key_provider_id": "software",
  "key_ref": "/etc/qrdb/backup.key",
  "metadata": { "schedule": "nightly" },
  "shard_ids": [1, 2, 3] }
```

`destination` is required; a local path or an object-store URI. `shard_ids`
empty means **all shards**. `encryption` left unset (or empty string on the
wire) defaults to `aes256` -- it is **not** plaintext by omission. In either
case (unset or explicitly `aes256`) `key_ref` is **required**; a caller that
wants plaintext must send `encryption: "none"` outright. `key_ref` is a
key-wrap provider reference (a key-file path for the built-in `"software"`
provider), never raw key bytes — see
[../key-wrapping/api.md](../key-wrapping/api.md) for the full schema this
feature adds.

Response — `BackupInfo`:

```json
{ "id": "bkp-20260819-0300", "timestamp": "2026-08-19T03:00:00Z",
  "size": 4823945216, "shard_count": 3, "status": "complete",
  "checksum": "sha256:…", "destination": "s3://acme-backups/qrdb",
  "compression": "zstd", "encryption": "aes256",
  "key_provider_id": "software",
  "metadata": { "schedule": "nightly" },
  "duration": 184000000000, "error_message": "" }
```

`status`: `pending` · `running` · `complete` · `failed` (with `error_message`).
A failed create is **recorded**, not discarded, so it appears in `list`.
`key_provider_id` is empty for a `"none"` backup.

`GET /v1/backups/list` returns `BackupInfo` entries **sorted by timestamp
descending**.

`POST /v1/backups/restore` — `RestoreRequest`:

```json
{ "backup_id": "bkp-20260819-0300",
  "target_shards": [1, 2],
  "validate_first": true,
  "decryption_key_ref": "/etc/qrdb/backup.key" }
```

`decryption_key_ref` is optional and usually omitted: the backup's own
envelope records the key reference it was wrapped under, so restore
resolves and decrypts it with no key material supplied at all in the
common case. Supply it only to override that stored reference (the key
file moved, or restore runs on a different node) — see
[../key-wrapping/api.md](../key-wrapping/api.md).

Response — `RestoreResult`:

```json
{ "backup_id": "bkp-20260819-0300", "shards_restored": 2,
  "duration": 96000000000,
  "errors": [ { "shard_id": 3, "message": "…" } ] }
```

**Restore authenticates and decrypts every target shard BEFORE importing any
of them**; a tampered/missing shard, a tampered manifest, or a stored-
metadata/artifact mismatch aborts the whole restore with one typed,
top-level error and imports nothing. `errors`/`shards_restored` above
report a narrower case: an OPERATIONAL import failure after every shard
has already passed verification, named per shard rather than collapsed
into the top-level refusal.

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
| `aes256` | AES-256-GCM, authenticated — the default when `encryption` is left unset |
| `none` | plaintext — only by explicit request; logged at WARN and, where a compliance audit logger is configured, recorded |

**BREAKING:** `aes256` became the default; it was previously `none`. A caller
that relied on the old plaintext default must now send `key_ref`, or send
`encryption: "none"` explicitly to keep writing plaintext. A request that
names neither is refused with `BackupEncryptionRequiredError`
(`INVALID_ARGUMENT`), distinct from the pre-existing
`BackupEncryptionKeyRequiredError` that fires when `aes256` is named
explicitly without a key reference.

The checksum provides **integrity**; authenticated encryption provides
**authenticity**. They are different properties, and now both default to on.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| Empty destination / empty ID | 400 | `INVALID_ARGUMENT` |
| Unsupported compression or encryption type | 400 | `INVALID_ARGUMENT` |
| Default encryption (unset) and no key reference | 400 | `INVALID_ARGUMENT` — `BackupEncryptionRequiredError`, **fail closed** |
| Encryption explicitly requested without a key reference | 400 | `INVALID_ARGUMENT` — `BackupEncryptionKeyRequiredError`, **fail closed** |
| Unregistered `key_provider_id`, wrong key reference, or tampered ciphertext on restore | 400 | `INVALID_ARGUMENT` — `BackupDecryptionFailedError`, **fail closed** (see [../key-wrapping/api.md](../key-wrapping/api.md)) |
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
