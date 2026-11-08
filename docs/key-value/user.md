# Key-Value Store — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

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

## Reading a key's version

A key's **version** counts its writes since it was created: 1 after the first
write, 2 after the next, and so on. It is the number a transaction's `version` compare reads, so you can read it, then
write only if nobody wrote in between.

Three calls report it, and they are the supported ways to get it — the version
they report is the same number for the same key:

| Call | CLI | SDK |
|---|---|---|
| a put that returns what it wrote | `qrdb kv put <key> <value> --revision` | `MVCC().PutWithLease(ctx, key, value, 0)` |
| a read with its metadata | `qrdb kv get <key> --revision` | `MVCC().GetRevision(ctx, key)` |
| a batch of get operations | `qrdb batch execute --file ops.json` | `Batch().ExecuteBatch(ctx, ops)` with `get` ops |
| a get inside a transaction branch | `qrdb tx execute --file tx.json` | `Transaction().ExecuteTransaction(ctx, req)` with `get` ops |

A get in a transaction branch is answered inside the transaction, after its
compares and any write staged before it in the same branch, so it reports what
the transaction itself leaves. It needs the `mvcc` state machine and the
cluster feature `transaction-reads`; see
[../transactions/api.md](../transactions/api.md).

A plain `kv put` and `kv get` return no version. With `raft.sm_type: mvcc`
every call also reports the key's create and modification revisions. A daemon
that keeps no revision history counts no writes: its `version` compare reads
only whether the key exists (0 absent, 1 present), so `kv put --revision` there
reports version 1 and no revisions.

## Claiming a key so exactly one caller wins

When two processes might write the same key at the same time, reading it and
then writing it is not enough — both read the same value, both decide, and the
second silently discards the first. Under a lock that is two holders.

The **guarded write verbs** decide the condition inside the database, in the
same step as the write, so exactly one caller can win:

| Verb | Use it to | Answers |
|---|---|---|
| put-if-absent | claim a key nobody holds yet | stored: true / false |
| compare-and-swap | change a value only if nobody else changed it first | swapped: true / false |
| compare-and-delete | release a claim you still hold | deleted: true / false |

**`false` is not an error.** It means somebody else got there first, which is
the normal outcome of a race. Do not retry it — re-read, decide again, or give
up; retrying an answer that will never change is a loop.

These have **no `qrdb kv` command**; they are an API surface, reached through a
client. Over the Go SDK they live on a capability interface a client is asked
for by type:

```go
if gv, ok := client.KV().(transport.GuardedKVClient); ok {
    stored, err := gv.PutIfAbsent(ctx, "claim/orders", []byte("worker-1"))
}
```

**From an MCP agent** they are three tools, alongside `qrdb_put` and
`qrdb_get`:

| Tool | Arguments | Result |
|---|---|---|
| `qrdb_put_if_absent` | `key`, `value` | `{"stored": true}` or `{"stored": false}` |
| `qrdb_compare_and_swap` | `key`, `expected`, `next` | `{"swapped": …}` |
| `qrdb_compare_and_delete` | `key`, `expected` | `{"deleted": …}` |

Every value argument — `value`, `expected`, `next` — is **base64-encoded**, as
it is for `qrdb_put`. The guard compares raw bytes, so a value that changed in
transit would make a release fail against the very value it was meant to
match. A missing value argument means the empty value, not "no value".

You need `kv:put` to call the first two and `kv:delete` to call the third —
the same permissions the ordinary write and delete need, not extra ones.

## Troubleshooting

**A `Put` failed with "storage is closed"** — the node released its store while
your call was in flight, which is what a shutdown does to work still running.
It is not a fault and not something to retry on the same node. In code, test
for it rather than reading the message:

```go
if err := kv.Put(ctx, key, value); err != nil && sdk.IsStorageClosed(err) {
	return nil // conclude cleanly; a restart resumes from the last record
}
```

Over gRPC you see `Unavailable` and over REST a `503`, both meaning the same:
this node is going away, retry elsewhere or later.

**A write or read answered `503` / `Unavailable` with "timeout"** — Raft found
no leader to answer it in time: an election was under way, or a new leader had
not yet committed the first entry of its term. Retry after a short wait. A
write that timed out may still have committed, so retry it only when writing
the same value again is harmless, or use one of the guarded write verbs above.

**"requires an MVCC-backed shard (configure raft.sm_type: mvcc)"** — the server keeps no revision history.
As-of reads, `kv restore` and watch replay all need it. This is a server
configuration change, not something you can pass as a flag.

**A revision you asked for "has been compacted"** — that history has
been discarded by `kv compact` on this database and cannot come back. Restore
from a backup instead ([../backup-restore/user.md](../backup-restore/user.md)).
Compaction is per database: compacting one database never discards another
database's history, and no tenant -- the platform operator included -- can
compact a database it does not own.

**History you did not compact is gone.** Check the database's compaction
policy: `qrdb database info <name> --output json` shows it as `compaction`, and
the node compacts the database on that schedule. It is off unless your tenant
set one.

**The system tenant's raw keys keep all their history.** Keys outside every
database -- what the unscoped embedded client writes -- are compacted only by
the platform's node-keyspace policy, which is off until a system-tenant
administrator sets it with `qrdb admin update-node-compaction`. A tenant
cannot set it, and it never compacts a database's keys.

**`kv compact` says "compaction requires a database scope"** — you reached the
node's raw MVCC service rather than a database (the unscoped embedded client,
or a server built without a service registry). There is no store-wide
compaction; scope the client to a database (`--database`,
`Client.DatabaseScoped`) and compact that.

**`kv restore` exited non-zero but said it succeeded** — that is the conflict
signal. One or more keys were written by somebody else between the plan and the
apply, so they were left alone rather than clobbered. The conflicting keys are
listed. Re-run once the other writer has stopped, or re-run with `--force` if
you are certain you want to overwrite.

**`kv restore` refuses neither or both of `--prefix` and `--keys`** — it needs
exactly one of them. Passing neither would restore everything; passing
both is undecidable.

**A restore of a `__sys:` key was refused** — that is deliberate and has no
override. Rolling back `__sys:roles/` or `__sys:users/` would undo a permission
revocation.

**Keys you know exist do not appear in `kv list`** — you are probably looking at
a reserved prefix. Internal namespaces (`__sys:`, `__tbl:`, `__idx:`, `__seq:`,
`__tblgen:`, `__reclaim:job/`, `__reclaim:index/`, `ts:`, `idx:metric:`,
`idx:tag:`, `retention:`, `__vec:`) are filtered out of bulk enumeration.

**Everything returns "unavailable"** — check whether the barrier is sealed
(`qrdb barrier status`) and whether the node is healthy (`qrdb admin health`).

**A guarded verb keeps answering `false`** — that is the guard doing its job,
not a fault. For `put-if-absent` the key is already held; for the compare verbs
the stored value is no longer the one you expected. Read the key to see who
holds it and what it says now. Retrying unchanged will answer `false` forever.

**A client says it has no guarded verbs** — `client.KV().(transport.GuardedKVClient)`
reporting false means the client is talking to a server that does not carry
them, and a call made anyway returns an "unsupported capability" error rather
than a wrong answer. Every transport carries them, so a false here means the
server is older than the client. Upgrade the server, or fall back to a
transaction.
