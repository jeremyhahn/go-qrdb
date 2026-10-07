# Key Wrapping — User Guide

> **Status: NORMATIVE; lands incrementally.** Scope: go-qrdb · Last verified: 2026-10-06

This is what actually protects an encrypted backup's contents: a
fresh, random key generated per backup (the DEK), itself locked ("wrapped")
under a key you control (the KEK). You never see the DEK; you manage the
KEK. For the backup feature itself — creating, listing, verifying,
restoring — start with [backup-restore](../backup-restore/user.md); this
guide covers only the key-management part.

## Why there are two keys, not one

If your backup encrypted every shard directly under the one key you supply,
rotating that key would mean re-encrypting every backup you have ever
taken. Instead, each backup gets its own throwaway key (the DEK), and only
that small key is locked under yours (the KEK) — rotating your KEK means
re-locking small keys, not re-encrypting terabytes.

You only ever handle the KEK. The DEK is generated, used, and discarded by
qrdb on every backup; it is never written anywhere unwrapped.

## The key directory, set up once by the operator who runs the daemon

Every key file the built-in `"software"` provider reads must live inside
ONE directory the DAEMON is configured with — `--backup-key-dir`. This is
a server-side setting, not something a backup client supplies: resolving
the built-in provider with no directory configured is refused outright
rather than falling back to some implicit location.

```bash
mkdir -p /etc/qrdb/keys && chmod 700 /etc/qrdb/keys
qrdbd start --backup-key-dir /etc/qrdb/keys ...
```

## The key file is the documented way

```bash
# Generate a 32-byte key once, INSIDE the key directory above. Keep it
# safe — losing it means losing every backup it was used for; there is
# no recovery path.
openssl rand -out /etc/qrdb/keys/backup.key 32
chmod 600 /etc/qrdb/keys/backup.key

qrdb backup create --destination /backups/daily \
     --encryption-key-file backup.key
```

**`--encryption-key-file` takes a NAME, never a path.** It names a file
directly inside the server's `--backup-key-dir` — a value with a path
separator, `""`, `"."`, or `".."` is refused before the server even looks
at its filesystem. This is deliberate: the backup client (which may be
remote) never gets to choose an arbitrary location on the server to read,
only a name inside the one directory the operator configured.

Back up that key file the way you back up any other credential — NOT
inside a qrdb backup itself, which is exactly the thing it protects.

## Rotating the key: add a new file, never overwrite the old one

Overwriting `backup.key` in place would silently change what every
ALREADY-WRAPPED DEK depends on — a backup made yesterday under the old
bytes would stop resolving. Instead, add a new file under a new name,
alongside the old one, and start naming the new one in fresh backups:

```bash
openssl rand -out /etc/qrdb/keys/backup-2026-10.key 32
chmod 600 /etc/qrdb/keys/backup-2026-10.key

qrdb backup create --destination /backups/daily \
     --encryption-key-file backup-2026-10.key
```

The old file (`backup.key`) stays exactly where it is, with its old name:
every backup made under it keeps resolving automatically on restore (see
below), since each backup remembers which name it was wrapped under.
There is no "rotate" command and no re-wrap step — rotation here is an
operator file-management action, done once per key you introduce, never
undone for backups already taken.

## Restoring

```bash
qrdb backup restore --id bkp-20261004-0300 --validate
```

No key flag needed: qrdb remembers which key file (and provider) a backup
was wrapped under and resolves it automatically. You only need
`--decryption-key-file` to OVERRIDE that — the key file was renamed, or
you are restoring on a different machine than the one that created the
backup (with its own, differently-populated `--backup-key-dir`):

```bash
qrdb backup restore --id bkp-20261004-0300 \
     --decryption-key-file backup.key --validate
```

Either way, the SAME key the backup was created with is what matters, and
it is still a bare NAME inside the server's `--backup-key-dir`, never a
path. A different key — even one that is a valid 32 bytes — is refused
rather than silently producing garbage: qrdb tells you the key did not
match, not "corrupted" or "invalid data".

**Restore is all-or-nothing.** Every shard a restore targets is
authenticated and decrypted BEFORE any of them is applied. A wrong key, a
tampered shard, or a tampered backup header aborts the WHOLE restore with
one typed refusal and leaves nothing changed — not some shards restored
and others not. There is no partial state to clean up after a refused
restore; fix the underlying problem (the right key file, an uncorrupted
backup) and run the whole restore again.

### Disaster recovery: restoring before the daemon serves any traffic

A SEPARATE, offline form of restore runs at daemon startup, before any
client connects — for recovering a node that lost its own data directory
entirely (`qrdbd start --backup-dir <dir> --restore-backup-id <id>`; see
[backup-restore/user.md](../backup-restore/user.md) for the full
procedure). It resolves keys the same way the live `restore` command
does, through the same `--backup-key-dir`, and has its own override flag:

```bash
qrdbd start --backup-dir /backups/dr --backup-key-dir /etc/qrdb/keys \
     --restore-backup-id bkp-20261004-0300 \
     --restore-decryption-key-file backup.key \
     ...
```

`--restore-decryption-key-file` is `--decryption-key-file`'s counterpart
for this offline path: also a bare NAME inside `--backup-key-dir`, also
only needed to override the backup's own stored key reference, and left
unset in the common case.

## Choosing plaintext on purpose

```bash
# Explicit plaintext, when you have a specific reason to accept it.
qrdb backup create --destination /backups/scratch --encryption none
```

Leaving `--encryption` unset with no key at all is refused — qrdb will not
silently write a backup in plaintext. See
[backup-restore/user.md](../backup-restore/user.md) for the full
encrypt-by-default behavior.

**This choice is never silent.** Both creating a plaintext backup and
restoring one are logged at WARN, and — wherever the deployment has a
compliance audit trail configured — recorded as an audit event you (or an
auditor) can later query. If the deployment's audit policy is configured
to fail CLOSED (the default) and that record cannot be written, the
plaintext backup or restore is refused rather than let through
unrecorded: an auditor reviewing the trail later is guaranteed that every
plaintext override it is missing a record for simply never happened.

## There is no "pass the key directly" option

An earlier draft of this feature let you pass the key's bytes straight on
the command line (`--encryption-key`). That option is gone: a key's bytes
never ride in a flag value, an environment variable, or over the wire to
the server — only a NAME does, and the server resolves that name against
its own, server-side key directory. If you are scripting around a
secrets manager, have it write the key to a file inside
`--backup-key-dir` (with tight permissions) and pass that file's name,
rather than injecting the bytes as an argument.

## Hardware-backed keys (TPM, HSM, cloud KMS)

Not something you configure from the CLI today. A deployment that ships
with hardware or cloud-KMS key custody (go-xkms-based products) registers
that backend once, at startup, and from then on `--key-provider <name>`
selects it the same way `--encryption-key-file` selects the built-in
software provider. If you are running plain go-qrdb with no such product
layered on top, the software key-file setup described above is what you
have, and it is fully supported — not a fallback.

## The same KEK also protects the tenant DEK keyring

Everything above is written in terms of backup, the first consumer of this
key-wrap seam, but the tenant DEK keyring
([tenant-dek/user.md](../tenant-dek/user.md)) uses the identical
mechanism for its own per-tenant keys: the same `"software"` provider (a
SEPARATE directory, `--dek-key-dir`, since the keyring's key reference
defaults to the tenant ID rather than an operator-chosen backup name), and
the same `--key-provider`-equivalent composition seam for hardware/cloud
KMS. Register a hardware provider once and both Backup and the DEK
keyring resolve it — there is no second registration step.

```bash
mkdir -p /etc/qrdb/dek-keys && chmod 700 /etc/qrdb/dek-keys
# One 32-byte key file per tenant, named after that tenant's ID.
openssl rand -out /etc/qrdb/dek-keys/acme 32 && chmod 600 /etc/qrdb/dek-keys/acme
qrdbd start --dek-key-dir /etc/qrdb/dek-keys ...
```

## Troubleshooting

**"backups are encrypted by default".** You asked for a backup without a
key, a key file, or `--encryption none`. Supply one of the three.

**"no key directory configured".** The daemon was started with no
`--backup-key-dir`, so the built-in `"software"` provider has nowhere to
look for key files at all. Restart it with the flag set.

**"decryption failed" / a typed refusal on restore.** Two different
things report two different ways, deliberately. A tampered SHARD's own
ciphertext — its AES-256-GCM authentication tag no longer matches —
returns `*BackupVerificationFailedError` wrapping
`*BackupShardIntegrityError`. A wrong key file, or a tampered WRAPPED
DEK (the key-encrypting layer one step up from a shard's own
ciphertext), both report as `*BackupDecryptionFailedError` — these two
are collapsed into the same error on purpose, so an attacker cannot use
the difference to narrow down a correct key by trial and error. Nothing
is restored either way, regardless of which error you see.

**"is stored as plaintext; restoring it requires an explicit opt-in".**
Restoring a backup whose OWN stored state is plaintext always requires
you to say so explicitly — `--allow-plaintext` on `qrdb backup restore`,
`allow_plaintext`/`AllowPlaintext` on every other transport and SDK, or
`--allow-plaintext-restore` on `qrdbd start --restore-backup-id`'s
offline path. It is never inferred from the backup's own metadata: an
attacker who flips a backup's stored `encryption` to `"none"` and
deletes its envelope object produces a backup that LOOKS like a genuine
plaintext one by stored state alone, and this opt-in is the only thing
that still requires a human (or an automation that has decided to trust
this specific backup) to say so on every restore, not merely once at
create time.

**"key must be exactly 32 bytes" / the key reference was refused.** Your
key file is the wrong size, or the value you passed was not a bare file
name (it contained a path separator, or named a directory or a missing
file). The error deliberately does not say which — the server never
confirms or denies anything about its own filesystem to a caller.
Generate a fresh key (`openssl rand -out <file> 32` under
`--backup-key-dir`, producing exactly 32 bytes) and pass only its name.

**I lost my key file.** There is no recovery. A backup encrypted under a
key you no longer have is unrecoverable by design — the same design that
keeps anyone else from recovering it without that key either. Treat the
key file with at least as much care as the backups it protects.
