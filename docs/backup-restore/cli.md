# Backup and Restore — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Command group: `qrdb backup` — create, list, inspect, verify, delete and restore
backups. All verbs accept the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags).

**Requires raft mode.** Shard backup and restore operate on shard snapshots; in
standalone mode they refuse with `requires mode: raft`.

## `qrdb backup create`

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--destination` | string | — | storage path or URI, e.g. `/backups/daily` or `s3://bucket/prefix` |
| `--compression` | string | `none` | `none \| zstd \| snappy` |
| `--encryption` | string | `none` | `none \| aes256` |
| `--encryption-key` | string | — | **required when `--encryption` is not `none`** |

```bash
qrdb backup create --destination /backups/daily

qrdb backup create --destination s3://acme-backups/qrdb \
     --compression zstd --encryption aes256 --encryption-key "$BACKUP_KEY"
```

Creating with encryption but no key is refused. Backing up specific shards is an
API-level option (`shard_ids`) and has no CLI flag; the CLI captures all shards.

The response carries the backup `id` and its `checksum` — record both.

## `qrdb backup list`

List available backups, **newest first**. No flags.

```bash
qrdb backup list --output json
```

## `qrdb backup get <id>`

Retrieve one backup's details: size, shard count, status, checksum,
destination, compression, encryption, duration, and any error message. No flags.

```bash
qrdb backup get bkp-20260819-0300 --output json
```

## `qrdb backup verify <id>`

Verify integrity by recomputing the SHA-256 checksum and comparing it to the
stored value. No flags.

```bash
qrdb backup verify bkp-20260819-0300 --output json
```

The result reports both the expected and the actual checksum, so a mismatch is
diagnosable rather than a bare failure.

## `qrdb backup restore`

Restore shard data from a backup. **This overwrites shard state.**

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--id` | string | — | backup ID to restore from |
| `--shards` | string | all shards | comma-separated shard IDs to restore |
| `--validate` | bool | false | run integrity verification **before** restoring |
| `--decryption-key` | string | — | **required for encrypted backups** |

```bash
# Verify then restore everything
qrdb backup restore --id bkp-20260819-0300 --validate

# Restore two shards from an encrypted backup
qrdb backup restore --id bkp-20260819-0300 --shards 1,2 \
     --decryption-key "$BACKUP_KEY" --validate
```

With `--validate`, a checksum mismatch aborts before any shard is touched.
Restoring an encrypted backup without `--decryption-key` is refused.

The result reports how many shards were restored and lists **per-shard errors**,
so a partial restore names exactly what failed.

## `qrdb backup delete <id>`

Delete a backup by ID. No flags.

```bash
qrdb backup delete bkp-20260812-0300
```

## Permissions

Each verb carries its own grant: `backup:create`, `backup:list`, `backup:read`,
`backup:verify`, `backup:delete`, `backup:restore`. **Restore is separate
because it overwrites shard state** — it is not implied by any of the others.

## Parity

Every verb here has a `CommandDefinition` in the integration parity suite. Note
that the end-to-end backup/restore integration test lives in the **cluster
stage**, which is not currently wired into CI.
