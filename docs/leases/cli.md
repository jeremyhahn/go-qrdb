# Leases — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Command group: `qrdb lease` — create, revoke, renew, inspect and list
distributed TTL leases. All verbs accept the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags).

## `qrdb lease grant`

Create a new lease with a TTL.

| Flag | Type | Default | Required |
|---|---|---|---|
| `--ttl` | int | — | yes — time-to-live in seconds |

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

## `qrdb lease ttl <lease-id>`

Show the current state of a lease: granted TTL, remaining TTL, and the attached
keys where tracked. No flags.

```bash
qrdb lease ttl 7 --output json
```

## `qrdb lease revoke <lease-id>`

Revoke a lease immediately. **Every key attached to it is deleted.** No flags.

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

Every verb here has a `CommandDefinition` in the integration parity suite.
