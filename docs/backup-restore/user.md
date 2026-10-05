# Backup and Restore — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Capture shard state to somewhere else, check it is intact, and put it back.
This is disaster recovery. For undoing a handful of keys, use
[`kv restore`](../key-value/user.md) instead — that is a different operation.

## Prerequisites

- **Raft mode.** Backups are shard snapshots; standalone has no shards to
  snapshot and refuses with `requires mode: raft`.
- A reachable server, client certificates, and the relevant `backup:*`
  permissions. Note `backup:restore` is a separate grant from `backup:create` —
  restore overwrites shard state.
- For an object-store destination, a daemon built with the matching go-objstore
  tag (`WITH_AWS_S3=1` and friends).

## First run

```bash
qrdb backup create --destination /backups/daily
qrdb backup list
```

Record the `id`. You will need it to verify or restore.

## Common tasks

**A real nightly backup.** Compress, encrypt, and send it off the box.

```bash
qrdb backup create --destination s3://acme-backups/qrdb \
     --compression zstd --encryption aes256 --encryption-key "$BACKUP_KEY"
```

Keep `$BACKUP_KEY` somewhere you will still have it when the machine you are
backing up is gone. A backup you cannot decrypt is not a backup.

**Verify on a schedule, not only when you need it.**

```bash
qrdb backup verify bkp-20260819-0300 --output json
```

An unverified backup is a backup you have not got. Verification recomputes the
checksum and compares — the result shows you both values.

**Restore everything, checking first.**

```bash
qrdb backup restore --id bkp-20260819-0300 --validate
```

`--validate` runs verification before touching any shard, so a corrupt backup
aborts instead of half-applying.

**Restore only some shards.**

```bash
qrdb backup restore --id bkp-20260819-0300 --shards 1,2 --validate
```

**Restore an encrypted backup.**

```bash
qrdb backup restore --id bkp-20260819-0300 \
     --decryption-key "$BACKUP_KEY" --validate
```

**See what you have and clear out the old.**

```bash
qrdb backup list --output json          # newest first
qrdb backup get bkp-20260819-0300
qrdb backup delete bkp-20260712-0300
```

There is no built-in retention policy — deleting old backups is your scheduler's
job, same as creating them.

## Troubleshooting

**"encryption key required".** You asked for `--encryption aes256` without
`--encryption-key`. This is refused rather than falling back to plaintext,
because a backup silently written unencrypted is the worst possible outcome.

**"decryption key required".** The backup was encrypted. Without the key it
cannot be restored — there is no recovery path. This is why the key belongs in
your key management, not on the machine being backed up.

**Restore aborted with a checksum mismatch.** `--validate` did its job: the
backup is corrupt or truncated, and nothing was written. Try an older backup.

**Restore finished but some shards failed.** The result lists per-shard errors
and counts the ones that succeeded. Fix the underlying problem for those shards
and re-run the restore for just them with `--shards`.

**A backup shows `status: failed` in `list`.** A failed create is recorded
rather than discarded, and `error_message` says why. That is deliberate — a
backup that failed silently would just be an absence nobody noticed until it
mattered.

**Backups keep getting bigger and slower.** Every backup is a **full** capture
of the shards it names; there are no incremental or differential backups. Use
compression (`zstd` for ratio, `snappy` for speed) and prune old ones.

**`qrdb backup` says "requires mode: raft".** The server is standalone. Backups
operate on shard snapshots and there are none.

**Restoring a backup made before the file engine's hashed-layout change
fails with a format-marker error.** `qrdbd restore --backup-dir` and any
`sm_storage: file`/`BackendFile` restore target open the backup directory
through the same `file.New` the data plane uses
([storage-engines/architecture.md](../storage-engines/architecture.md#the-file-engine-a-hashed-flat-content-addressed-layout-breaking)),
so a backup written before that change is the OLD layout and is refused
in-place (`FormatMarkerMissingError`) by a binary that carries the new one,
by design — there is no migration, per the project's no-backward-
compatibility policy. Restore that backup with a binary from before the
change, or treat it as unrecoverable by this one. A backup made with
`sm_storage: pebble`/`BackendPebble`/`BackendMemory` is unaffected: only the
`file` engine's on-disk bytes changed.

**You want to migrate data to a database with a different engine or backend.**
Backup and restore is the usual route, but note the shard snapshot format is the
compatibility surface — restoring onto a materially different topology is not a
supported migration path. For a logical export, use SQL or `kv range`.

**Nobody is checking the backups.** Worth saying plainly: this feature has no
scheduler and no retention policy. If nothing outside the database is running
`create`, `verify` and `delete`, then nothing is happening.
