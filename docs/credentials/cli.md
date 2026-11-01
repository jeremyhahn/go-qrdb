# Credentials — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-27
> Conflicts with this document are defects. Fix them here first.

Command group: `qrdb credential` — named-credential operations. All verbs
accept the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags).

These are the **engine's own operational secrets**, not an application secret
store; application secrets belong to quicbaas's KMS, one layer up. Every verb
acts on the caller's own store: the platform's for a system principal, the
tenant's for a principal of a tenant with a dedicated barrier (`barrier`,
`volume` or `raft` isolation). A tenant on `prefix` isolation is refused, and
a tenant's store always reports the `barrier` strategy. See
[user.md](user.md#credentials-in-a-tenant).

## `qrdb credential strategy`

Show the active credential strategy — `manual` or `barrier`. No flags.

```bash
qrdb credential strategy
```

## `qrdb credential store <name>`

Persist a credential value under the given name.

| Flag | Type | Required |
|---|---|---|
| `--value` | string | yes |

```bash
qrdb credential store smtp-password --value "$SMTP_PASS"
```

## `qrdb credential submit <name>`

Submit a credential value under the **manual** strategy. Retrieval blocks until
this happens, which is how a non-resident secret reaches a booting system: an
officer supplies it.

| Flag | Type | Required |
|---|---|---|
| `--value` | string | yes |

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
| `--version` | uint64 | `0` (current) |

```bash
qrdb credential retrieve-version smtp-password --version 3
```

A version past its grace period is gone; the call returns not-found.

`retrieve` and `retrieve-version` print the value followed by a newline. The
CLI receives it as a protected-memory buffer and writes it straight to
standard output from inside the buffer's borrow, then destroys the buffer; the
terminal or pipe is where the value leaves protected memory (see
[Protected-memory boundary](architecture.md#protected-memory-boundary)).

## `qrdb credential rotate <name>`

Rotate to a new value.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--value` | string | — (required) | the new value |
| `--grace-period` | int64 | `0` | seconds the **previous** version stays retrievable; `0` wipes it immediately |

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

- Under the `barrier` strategy, every verb here except `strategy` fails while
  the barrier is sealed.
- Under `manual`, values live only in memory: a restart re-blocks retrieval
  until somebody submits again.
- **PINs are not credentials.** They live in their own subsystem with lockout
  semantics — see [../auth-factors/cli.md](../auth-factors/cli.md).

## Parity

`TestCredential_CLI_Lifecycle_PerProtocol` in
`test/integration/credential_cli_test.go` runs every verb here over REST, gRPC,
QUIC, MCP and Unix.
