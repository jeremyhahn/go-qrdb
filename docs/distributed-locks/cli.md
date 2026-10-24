# Distributed Locks — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-22
> Conflicts with this document are defects. Fix them here first.

Command group: `qrdb lock` — acquire, release, extend, inspect and list
distributed locks with fencing tokens. All verbs accept the root persistent
flags documented in [../key-value/cli.md](../key-value/cli.md#global-flags).

**A lock name is scoped to `(tenant, database)`.** The tenant comes from your
client certificate; the database comes from the root `--database` flag, exactly
as for `qrdb kv`. `qrdb lock acquire deploy --database billing` and
`qrdb lock acquire deploy --database reporting` take two DIFFERENT locks, and
`qrdb lock list` shows only the scope you asked for. If a lock looks like it is
not being contended when you expect it to be, check that both holders name the
same database.

Locks work the same in both run modes. A standalone node on the file or
objstore backend keeps no leases, so every verb here is refused there as
unsupported: a lock needs the lease that expires it.

## `qrdb lock acquire <key>`

Acquire a lock, **blocking** until it is available or the wait timeout expires.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--ttl` | int | required | lock TTL in seconds |
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
count. No flags. A lock nobody holds prints `not locked`.

```bash
qrdb lock status jobs/nightly-report --output json
```

## `qrdb lock list`

List active locks in the current `(tenant, database)` scope.

| Flag | Type | Default |
|---|---|---|
| `--prefix` | string | none — filter locks by key prefix |

```bash
qrdb lock list
qrdb lock list --prefix jobs/
qrdb lock list --database billing
```

No prefix means "every lock in this scope", not "every lock on this node":
another tenant's locks, and your own other databases' locks, are not
enumerable from here.

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

Every verb here is driven on every network transport by `lock_cli_test.go`
(`make integration-test-distributed-locks`). Per-transport accounting: the
`distributed-locks` row of
[`test/integration/COVERAGE.md`](../../test/integration/COVERAGE.md).
