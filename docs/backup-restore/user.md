# Backup and Restore — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26
> Conflicts with this document are defects. Fix them here first.

Test reference: `make integration-test-backup-restore`; coverage row: `test/integration/COVERAGE.md` `[backup-restore](../../docs/backup-restore/)`.

Capture shard state to somewhere else, check it is intact, and put it back.
This is disaster recovery. For undoing a handful of keys, use
[`kv restore`](../key-value/user.md) instead — that is a different operation.

## Prerequisites

- **Raft mode.** Backups are shard snapshots; standalone has no shards to
  snapshot and refuses with `requires mode: raft`.
- A reachable server, client certificates, and the relevant `backup:*`
  permissions. Note `backup:restore` is a separate grant from `backup:create` —
  restore overwrites shard state.
- For backups that outlive the daemon process, start `qrdbd` with
  `--backup-dir <dir>`. Without it, backups are held in memory only.

## Encryption is the default

**Backups are encrypted (`aes256`) unless you say otherwise.** Leave
`--encryption` off and supply `--encryption-key-file` (the NAME of a
32-byte key file inside the server's `--backup-key-dir`, never a path —
see [../key-wrapping/user.md](../key-wrapping/user.md) for the full
key-management story) to keep the default. Want plaintext? Say so
outright with `--encryption none` — that choice is logged and, where a
compliance audit trail is configured, recorded. There is no silent fallback
either way: omitting both the key file and `--encryption none` is refused.

## First run

On the server, before the daemon starts:

```bash
mkdir -p /etc/qrdb/keys && chmod 700 /etc/qrdb/keys
openssl rand -out /etc/qrdb/keys/backup.key 32 && chmod 600 /etc/qrdb/keys/backup.key
qrdbd start --backup-key-dir /etc/qrdb/keys --backup-dir /var/lib/qrdb/backups ...
```

From the CLI, naming the file by NAME, not path:

```bash
qrdb backup create --destination /backups/daily --encryption-key-file backup.key
qrdb backup list
```

Record the `id`. You will need it to verify or restore.

## Common tasks

**A nightly backup.** Encrypted (the default), labelled so you can tell it apart.

```bash
qrdb backup create --destination /backups/nightly --encryption-key-file backup.key
```

The backup lands in the server's `--backup-dir`; `--destination` is a label
recorded with it. Copy that directory off the machine with your usual tooling.

`--encryption aes256` is implied here; naming it is optional as long as a key
reference is supplied.

Keep `/etc/qrdb/keys/backup.key` somewhere you will still have it when the
machine you are backing up is gone. A backup you cannot decrypt is not a
backup.

**Verify on a schedule, not only when you need it.**

```bash
qrdb backup verify 3f8e2a1c-7b4d-4e9a-9c61-2d5f0b8a7e43 --output json
```

An unverified backup is a backup you have not got. Verification recomputes the
checksum and compares — the result shows you both values.

**Restore everything, checking first.**

```bash
qrdb backup restore --id 3f8e2a1c-7b4d-4e9a-9c61-2d5f0b8a7e43 --validate
```

`--validate` runs verification before touching any shard, so a corrupt backup
aborts instead of half-applying.

**Restore only some shards.**

```bash
qrdb backup restore --id 3f8e2a1c-7b4d-4e9a-9c61-2d5f0b8a7e43 --shards 1,2 --validate
```

**Restore an encrypted backup.** No key flag needed — qrdb remembers which
key file a backup was wrapped under and resolves it automatically.

```bash
qrdb backup restore --id 3f8e2a1c-7b4d-4e9a-9c61-2d5f0b8a7e43 --validate
```

Supply `--decryption-key-file` only to OVERRIDE the stored reference (the
key file moved, or you're restoring on a different machine):

```bash
qrdb backup restore --id 3f8e2a1c-7b4d-4e9a-9c61-2d5f0b8a7e43 \
     --decryption-key-file backup.key --validate
```

**Write a backup in plaintext, deliberately.** Only when you have a specific
reason to — a backup covering the system shard carries the cluster CA and
every tenant's login CA key material in the clear.

```bash
qrdb backup create --destination /backups/scratch --encryption none
```

The CLI prints a warning when you do this, and the server logs it (and
audits it, where a compliance audit trail is configured).

**See what you have and clear out the old.**

```bash
qrdb backup list --output json          # newest first
qrdb backup get 3f8e2a1c-7b4d-4e9a-9c61-2d5f0b8a7e43
qrdb backup delete a91d04e6-52c3-4f0b-8e7a-6b3c9f1d2e85
```

There is no built-in retention policy — deleting old backups is your scheduler's
job, same as creating them.

## Troubleshooting

**"backups are encrypted by default".** You left `--encryption` unset (or
named `aes256` explicitly) and supplied no `--encryption-key-file`. The
default is encrypted, not plaintext, so this is refused rather than minting
a key you don't have or falling back to plaintext you didn't ask for.
Supply a key file, or say `--encryption none` if you actually want
plaintext.

**"encryption key is required when encryption is enabled".** You named
`--encryption aes256` explicitly and supplied no `--encryption-key-file`. The
fix is the same: supply `--encryption-key-file`, or choose `--encryption none`.

**A typed refusal on restore naming the key.** The backup's own stored key
reference did not resolve (the key file is gone, or you overrode it with
the wrong name via `--decryption-key-file`). There is no recovery path
without the matching key file — this is why the key file belongs in your
key management, not only on the machine being backed up.

**Restore aborted with a checksum mismatch.** `--validate` did its job: the
backup is corrupt or truncated, and nothing was written. Try an older backup.

**Restore refused outright, nothing written.** A tampered or missing shard,
a tampered manifest, or a stored-metadata/artifact mismatch all abort the
WHOLE restore with one typed error before anything is imported — restore
verifies every target shard before importing any of them, specifically so
a problem with one shard can never leave another already imported. There
is no partial state to clean up; fix the underlying problem (a wrong key,
a corrupted artifact) and retry the whole restore.

**Restore finished but some shards failed.** This is a different, narrower
case: every shard already passed verification, and the underlying storage
layer itself refused the import for some of them (an operational failure,
not a cryptographic one). The result lists per-shard errors and counts the
ones that succeeded; fix the underlying storage problem and re-run the
restore for just those shards with `--shards`.

**A backup shows `status: failed` in `list`.** A failed create is recorded
rather than discarded, and `error_message` says why. That is deliberate — a
backup that failed silently would just be an absence nobody noticed until it
mattered.

**Backups keep getting bigger and slower.** Every backup is a **full** capture
of the shards it names; there are no incremental or differential backups, and
shard data is stored uncompressed (`--compression` is recorded, not applied).
Prune old ones.

**`qrdb backup` says "requires mode: raft".** The server is standalone. Backups
operate on shard snapshots and there are none.

**Restore fails with a format-marker error (`FormatMarkerMissingError`).**
`qrdbd start --restore-backup-id <id> --backup-dir <dir>` and any
`sm_storage: file`/`BackendFile` restore target open the backup directory
through the same `file.New` the data plane uses, which accepts only the hashed
flat layout
([storage-engines/architecture.md](../storage-engines/architecture.md#the-file-engine-a-hashed-flat-content-addressed-layout))
and does not migrate a directory without its format marker. Restore that
backup with the binary that wrote it. A backup made with
`sm_storage: pebble`/`BackendPebble`/`BackendMemory` is unaffected.

**You want to migrate data to a database with a different engine or backend.**
Backup and restore is the usual route, but note the shard snapshot format is the
compatibility surface — restoring onto a materially different topology is not a
supported migration path. For a logical export, use SQL or `kv range`.

**Nobody is checking the backups.** Worth saying plainly: this feature has no
scheduler and no retention policy. If nothing outside the database is running
`create`, `verify` and `delete`, then nothing is happening.
