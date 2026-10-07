# Credentials — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

Command group: `qrdb credential` — named-credential operations. All verbs
accept the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags).

These are the **engine's own operational secrets**, not an application secret
store. Application and tenant secrets belong to quicbaas's KMS, one layer up.

## `qrdb credential strategy`

Show the active credential strategy — `manual` or `barrier`. No flags.

```bash
qrdb credential strategy
```

## `qrdb credential store <name>`

Persist a credential value under the given name.

| Flag | Type |
|---|---|
| `--value` | string |

```bash
qrdb credential store smtp-password --value "$SMTP_PASS"
```

## `qrdb credential submit <name>`

Submit a credential value under the **manual** strategy. Retrieval blocks until
this happens, which is how a non-resident secret reaches a booting system: an
officer supplies it.

| Flag | Type |
|---|---|
| `--value` | string |

```bash
qrdb credential submit ceremony-seed --value "$SEED"
```

## `qrdb credential retrieve <name>`

Retrieve the current value. No flags.

```bash
qrdb credential retrieve smtp-password
```

## `qrdb credential retrieve-version <name>`

Retrieve a specific version. **`--version 0` means the current version.**

| Flag | Type | Default |
|---|---|---|
| `--version` | uint | `0` (current) |

```bash
qrdb credential retrieve-version smtp-password --version 3
```

A version past its grace period is gone; the call returns not-found.

## `qrdb credential rotate <name>`

Rotate to a new value.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--value` | string | — | the new value |
| `--grace-period` | int | `0` | seconds the **previous** version stays retrievable; `0` wipes it immediately |

```bash
# Cut over immediately
qrdb credential rotate smtp-password --value "$NEW"

# Keep the old value readable for an hour while consumers catch up
qrdb credential rotate smtp-password --value "$NEW" --grace-period 3600
```

The grace period is what lets a fleet of consumers roll over without a
synchronized restart.

## `qrdb credential versions <name>`

List the version history for a credential. No flags.

```bash
qrdb credential versions smtp-password --output json
```

## `qrdb credential list`

List stored credential names. No flags. **Names only — never values.**

```bash
qrdb credential list
```

## `qrdb credential gc <name>`

Reap expired historical versions. The current version is never touched. No
flags.

```bash
qrdb credential gc smtp-password
```

## `qrdb credential delete <name>`

Delete a credential and its non-current versions. No flags.

```bash
qrdb credential delete smtp-password
```

## Notes

- Under the `barrier` strategy, every verb here fails while the barrier is
  sealed.
- Under `manual`, values live only in memory: a restart re-blocks retrieval
  until somebody submits again.
- **PINs are not credentials.** They live in their own subsystem with lockout
  semantics — see [../auth-factors/cli.md](../auth-factors/cli.md).

## Parity

Every verb here has a `CommandDefinition` in the integration parity suite.
