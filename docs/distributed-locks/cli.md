# Distributed Locks — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Command group: `qrdb lock` — acquire, release, extend, inspect and list
distributed locks with fencing tokens. All verbs accept the root persistent
flags documented in [../key-value/cli.md](../key-value/cli.md#global-flags).

## `qrdb lock acquire <key>`

Acquire a lock, **blocking** until it is available or the wait timeout expires.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--ttl` | int | — | lock TTL in seconds |
| `--holder` | string | auto-generated UUID | holder identifier |
| `--reentrant` | bool | false | allow re-acquisition by the same holder |
| `--wait` | int | 0 | acquisition wait timeout in seconds; `0` uses the request timeout |

```bash
qrdb lock acquire jobs/nightly-report --ttl 300 --wait 30
qrdb lock acquire jobs/nightly-report --ttl 300 --holder worker-7 --output json
```

**Record the `fencing_token` from the output.** You need it to release or extend,
and the resource you are protecting should check it.

## `qrdb lock trylock <key>`

Attempt to acquire without blocking. Same flags as `acquire`, minus `--wait`.

| Flag | Type | Default |
|---|---|---|
| `--ttl` | int | — |
| `--holder` | string | auto-generated UUID |
| `--reentrant` | bool | false |

```bash
qrdb lock trylock jobs/nightly-report --ttl 300 || echo "someone else has it"
```

## `qrdb lock extend <key>`

Refresh the TTL on a lock you hold.

| Flag | Type | Required |
|---|---|---|
| `--token` | int | yes — the fencing token returned at acquisition |

```bash
qrdb lock extend jobs/nightly-report --token 4711
```

A stale token is refused. Extend on an interval well inside the TTL.

## `qrdb lock release <key>`

Release a lock you hold.

| Flag | Type | Required |
|---|---|---|
| `--token` | int | yes — the fencing token returned at acquisition |

```bash
qrdb lock release jobs/nightly-report --token 4711
```

For a reentrant lock acquired *n* times, release *n* times.

## `qrdb lock status <key>`

Show the current state of a lock: holder, fencing token, lease, TTL, acquire
count. No flags.

```bash
qrdb lock status jobs/nightly-report --output json
```

## `qrdb lock list`

List active locks.

| Flag | Type | Default |
|---|---|---|
| `--prefix` | string | none — filter locks by key prefix |

```bash
qrdb lock list
qrdb lock list --prefix jobs/
```

## A complete critical section

```bash
KEY=jobs/nightly-report
OUT=$(qrdb lock trylock "$KEY" --ttl 300 --holder "$(hostname)-$$" --output json) || exit 0
TOKEN=$(echo "$OUT" | jq -r .fencing_token)
trap 'qrdb lock release "$KEY" --token "$TOKEN"' EXIT

run-the-report --fence "$TOKEN"     # the resource checks the token
```

Passing the token through to the protected resource is the part that makes this
safe rather than merely conventional.

## Parity

Every verb here has a `CommandDefinition` in the integration parity suite.
