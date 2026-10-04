# Key Wrapping — API

> **Status: NORMATIVE; lands incrementally.** Scope: go-qrdb · Last verified: 2026-10-04

Key wrapping adds no new RPC surface of its own. It changes the request and
response shapes of `POST /v1/backups` and `POST /v1/backups/restore`
(and their gRPC/QUIC/MCP/embedded equivalents), documented in full in
[backup-restore/api.md](../backup-restore/api.md). This document covers only
the fields this feature adds or changes to that existing schema.

## `POST /v1/backups` — `BackupRequest`, new fields

```json
{ "destination": "s3://acme-backups/qrdb",
  "compression": "zstd",
  "encryption": "aes256",
  "key_provider_id": "software",
  "key_ref": "/etc/qrdb/backup.key",
  "metadata": { "schedule": "nightly" },
  "shard_ids": [1, 2, 3] }
```

| Field | Type | Meaning |
|---|---|---|
| `key_provider_id` | string | the registered `keywrap.Provider` ID to wrap this backup's DEK with. Empty defaults to `"software"` when `key_ref` or `encryption_key` is set. |
| `key_ref` | string | the key reference the named provider resolves — a key-file path for `"software"`. Mutually exclusive with `encryption_key`. |
| `encryption_key` | bytes (base64 on the wire) | **kept, unchanged on the wire**: 32 raw KEK bytes used directly through an inline, unregistered software provider. Mutually exclusive with `key_ref`. |

Exactly one of `key_ref` / `encryption_key` may be set when `encryption` is
`"aes256"` (or defaulted to it); both set, or `key_ref` naming an
unregistered `key_provider_id`, is refused before any shard is captured.

## Response — `BackupInfo`, new field

```json
{ "id": "bkp-20261004-0300", "timestamp": "2026-10-04T03:00:00Z",
  "size": 4823945216, "shard_count": 3, "status": "complete",
  "checksum": "sha256:…", "destination": "s3://acme-backups/qrdb",
  "compression": "zstd", "encryption": "aes256",
  "key_provider_id": "software",
  "metadata": { "schedule": "nightly" },
  "duration": 184000000000, "error_message": "" }
```

`key_provider_id` is empty for a `"none"` backup. Nothing about the wrapped
DEK, the envelope's nonce, or the format version is surfaced here — see
[architecture.md](architecture.md#backup-integration) for why.

## `POST /v1/backups/restore` — `RestoreRequest`, new field

```json
{ "backup_id": "bkp-20261004-0300",
  "target_shards": [1, 2],
  "validate_first": true,
  "decryption_key_ref": "/etc/qrdb/backup.key",
  "decryption_key": "<base64, kept>" }
```

| Field | Type | Meaning |
|---|---|---|
| `decryption_key_ref` | string | the key reference to resolve through the backup's own stored `key_provider_id`. Mirrors `key_ref` on create. |
| `decryption_key` | bytes (base64 on the wire) | **kept, unchanged on the wire**: the inline path's counterpart. |

The provider actually used to `Unwrap` is whichever `key_provider_id` the
backup itself was created under — a restore request does not choose a
different provider than the one that wrapped the DEK; `decryption_key_ref`
only supplies that provider's own reference (the key file's path, for
`"software"`).

## Capability matrix

All six transports carry the same request/response shapes, unchanged from
the matrix in [backup-restore/api.md](../backup-restore/api.md#capability-matrix)
— key wrapping adds fields to existing messages, not new verbs.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Both `key_ref` and `encryption_key` set | 400 | `INVALID_ARGUMENT` |
| `key_provider_id` names an unregistered provider | 400 | `INVALID_ARGUMENT` — `*BackupDecryptionFailedError` |
| Wrong key (file or inline) on restore | 400 | `INVALID_ARGUMENT` — `*BackupDecryptionFailedError` |
| Tampered shard ciphertext on restore | 400 | `INVALID_ARGUMENT` — `*BackupDecryptionFailedError` |

These join, not replace, the existing mapping table in
[backup-restore/api.md](../backup-restore/api.md#status-and-error-mapping).
