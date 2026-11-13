# Credentials — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-11-13
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

## `qrdb credential rotation-policy set <name>`

Set the policy under which every node's scheduler rotates the credential
automatically. The cluster rotates it once per interval, not once per node.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--enabled` | bool | `false` | turn scheduled rotation on; omitted, the policy is disabled and the schedule cleared |
| `--interval` | duration | — | time between rotations, a Go duration such as `720h`; required when enabled |
| `--retain-versions` | int | `0` | versions kept after a rotation, the current one included; at least `1` when enabled |

```bash
# Rotate every 30 days, keeping the current value and two predecessors
qrdb credential rotation-policy set smtp-password --enabled --interval 720h --retain-versions 3

# Stop scheduled rotation
qrdb credential rotation-policy set smtp-password
```

The server validates the policy and refuses an invalid one with
invalid-argument: a non-positive or unparsable interval, `--retain-versions`
below 1, or any policy under the `manual` strategy. A scheduled rotation needs
a rotation value source wired into the server; `qrdbd` ships none, so on a
stock daemon a due rotation fails and is logged (see
[engineering.md](engineering.md#scheduled-rotation)).

## `qrdb credential rotation-policy get <name>`

Show the credential's rotation policy and, when one is scheduled, the next
rotation time (RFC 3339, UTC). No flags. A credential with no policy shows
`enabled=false`.

```bash
qrdb credential rotation-policy get smtp-password
# rotation policy for smtp-password: enabled=true interval=720h0m0s retain-versions=3 next-rotation=2026-11-29T12:00:00Z
```

## `qrdb credential migration-conflicts list`

List the credentials whose copy on the node you are talking to differs from
the replicated record and was kept when the node migrated its records (see
[architecture.md](architecture.md#prefix-migration)), with the parts that
differ. No flags. Never prints a value. An empty list prints nothing. Needs
`credentials:list`.

```bash
qrdb credential migration-conflicts list
# smtp-password: value=true versions=false policy=false
```

## `qrdb credential migration-conflicts resolve <name>`

Settle one conflict on the node you are talking to. Needs
`credentials:rotate`.

| Flag | Description |
|---|---|
| `--resolution` | Required. `import` writes the node's kept current value as a new version of the replicated record, retaining the version it replaces; `discard` keeps the replicated record. |

Either way the node's kept records for the name are deleted.

```bash
qrdb credential migration-conflicts resolve smtp-password --resolution import
# migration conflict for smtp-password resolved: import
```

A name with no conflict on the node fails with not-found; a resolution other
than `import` or `discard` fails with invalid-argument.

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
QUIC, MCP and Unix. `TestCredential_CLI_RotationPolicy_PerProtocol` runs
`rotation-policy set` and `get` over the same five: a policy set is the policy
read back, an invalid one and any policy on a `manual` store are refused with
the typed error, and a disabled one reads back with nothing scheduled.
`TestCredential_CLI_Cluster_OneStore_PerProtocol` sets a policy through one
node of a three-node cluster and reads it through another. `migration-conflicts`
is covered by `cmd/qrdb` unit tests and by each SDK transport's tests: a stock
cluster has no pre-upgrade copies to conflict.
