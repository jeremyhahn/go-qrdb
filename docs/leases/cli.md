# Leases — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-23
> Conflicts with this document are defects. Fix them here first.

Command group: `qrdb lease` — create, revoke, renew, inspect and list
distributed TTL leases. All verbs accept the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags).

## `qrdb lease grant`

Create a new lease with a TTL.

| Flag | Type | Default | Required |
|---|---|---|---|
| `--ttl` | int64 | — | yes — time-to-live in seconds |

```bash
qrdb lease grant --ttl 60
qrdb lease grant --ttl 300 --output json
```

Output carries the lease `id`, which is what you attach keys with.

## `qrdb lease keep-alive <lease-id>`

Reset the TTL on an existing lease back to its granted value. No flags.

```bash
qrdb lease keep-alive 7
```

Run this on an interval comfortably shorter than the TTL. Reading or writing a
leased key does **not** renew the lease.

The renewal is answered by the cluster leader without a log write. Every
`lease.checkpoint_interval` (default `5m`) the leader records what each lease
longer than that has left. If leadership changes, the new leader counts every
lease from that recorded value — or its full TTL when it has none — plus
`lease.failover_grace` (default `5s`), so `lease ttl` may briefly show a
remaining TTL above the granted one. Both keys are set in the `lease:` section of the config file;
see [engineering.md](engineering.md#configuration).

## `qrdb lease ttl <lease-id>`

Show the current state of a lease: granted TTL, remaining TTL, and the keys
currently attached. A key re-written under another lease or with none since is
no longer listed. No flags.

```bash
qrdb lease ttl 7 --output json
```

## `qrdb lease revoke <lease-id>`

Revoke a lease immediately. **Every key attached to it is deleted.** Attached
means the key's latest write named this lease: a key re-written since under
another lease, or with none, is left alone. No flags.

```bash
qrdb lease revoke 7
```

## `qrdb lease list`

List all active leases. No flags.

```bash
qrdb lease list
```

## Attaching keys

Attachment happens on the write, not in this command group:

```bash
LEASE=$(qrdb lease grant --ttl 60 --output json | jq -r .id)
qrdb kv put session/abc '{"user":"alice"}' --lease-id "$LEASE"
```

Writing to a lease ID that does not exist is refused — the key is not written
without its lease.

## Parity

Every verb here is driven on every network transport by `lease_cli_test.go`
(`make integration-test-leases`). Per-transport accounting: the `leases` row of
[`test/integration/COVERAGE.md`](../../test/integration/COVERAGE.md).
