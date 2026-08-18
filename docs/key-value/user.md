# Key-Value Store — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Prerequisites

- A running `qrdbd` you can reach, and client certificate material (`--ca-file`,
  `--cert-file`, `--key-file`) issued by that server's CA.
- The barrier unsealed, if the server is running encrypted. A sealed server
  refuses reads and writes; see [../barrier-seal/user.md](../barrier-seal/user.md).
- For history features (as-of reads, restore), the server must run
  `raft.sm_type: mvcc`. Ask an operator, or check with `qrdb kv revision` —
  a server with no revision history will tell you.

## First run

The default transport is the local Unix socket, so on the same host you can
usually just talk to it:

```bash
qrdb kv put hello world
qrdb kv get hello
```

Remotely, name the server and your certificates:

```bash
qrdb --server https://db.example.com \
     --ca-file ca.pem --cert-file client.pem --key-file client-key.pem \
     kv get hello
```

A URL scheme on `--server` picks the transport for you, so `https://` implies
REST and `grpcs://` implies gRPC without a separate `--transport`.

## Common tasks

**Write and read back.**

```bash
qrdb kv put app/config/timeout 30s
qrdb kv get app/config/timeout
```

**Pick how fresh the read must be.** The default is good for almost everything.
Reach for `--consistency strong` only when a stale read would be a correctness
bug (a balance check before a transfer), and `--consistency eventual` for
dashboards where staleness is fine and speed matters.

```bash
qrdb kv get accounts/123/balance --consistency strong
qrdb kv get metrics/hits --consistency eventual
```

**Pick how durable the write must be.** `--write-mode sync` waits for the data
to be on disk; `batched` is fastest and groups writes together.

```bash
qrdb kv put ledger/entry/9 '{"amount":100}' --write-mode sync
```

**List and scan a prefix.**

```bash
qrdb kv list app/
qrdb kv range app/a app/z --limit 100 --output json
```

**Attach a lease so a key expires by itself.** Grant a lease, then hang keys
off it; when the lease expires or is revoked, the keys go with it. See
[../leases/user.md](../leases/user.md).

```bash
LEASE=$(qrdb lease grant --ttl 60 --output json | jq -r .id)
qrdb kv put session/abc "$LEASE-data" --lease-id "$LEASE"
```

**Read a key as it was earlier.**

```bash
qrdb kv revision                                   # what revision are we at?
qrdb kv get app/config/timeout --at-revision 42    # what did it say back then?
```

**Undo a bad change to a set of keys.** Always preview first.

```bash
qrdb kv restore --prefix app/config/ --revision 42 --dry-run
qrdb kv restore --prefix app/config/ --revision 42
```

The restore writes the old values forward as new writes. Nothing is erased, and
anything written since the revision you named that you did *not* restore stays
exactly where it is.

**Work inside a specific database.**

```bash
qrdb --database billing kv list invoices/
```

## Troubleshooting

**"requires raft.sm_type: mvcc"** — the server keeps no revision history.
As-of reads, `kv restore` and watch replay all need it. This is a server
configuration change, not something you can pass as a flag.

**A revision you asked for is "below the compaction floor"** — that history has
been discarded by `kv compact` and cannot come back. Restore from a backup
instead ([../backup-restore/user.md](../backup-restore/user.md)).

**`kv restore` exited non-zero but said it succeeded** — that is the conflict
signal. One or more keys were written by somebody else between the plan and the
apply, so they were left alone rather than clobbered. The conflicting keys are
listed. Re-run once the other writer has stopped, or re-run with `--force` if
you are certain you want to overwrite.

**"scope is required" / "scope is ambiguous"** — `kv restore` needs exactly one
of `--prefix` or `--keys`. Passing neither would restore everything; passing
both is undecidable.

**A restore of a `__sys:` key was refused** — that is deliberate and has no
override. Rolling back `__sys:roles/` or `__sys:users/` would undo a permission
revocation.

**Keys you know exist do not appear in `kv list`** — you are probably looking at
a reserved prefix. Internal namespaces (`__sys:`, `__tbl:`, `__idx:`, `ts:`,
`idx:metric:`, `idx:tag:`, `retention:`) are filtered out of bulk enumeration.

**Everything returns "unavailable"** — check whether the barrier is sealed
(`qrdb barrier status`) and whether the node is healthy (`qrdb admin health`).
