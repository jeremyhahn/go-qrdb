# Key-Value Store — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-01

Command group: `qrdb kv`. Three top-level aliases exist for the most common
verbs: `qrdb put`, `qrdb get`, `qrdb del` are identical to their `kv`
counterparts.

## Global flags

Every verb below accepts the root persistent flags:

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--server` | string | auto-detect | Server address. A URL scheme overrides `--transport`. |
| `--transport` | string | `unix` | `rest \| grpc \| quic \| mcp \| unix \| embedded` |
| `--database` | string | server default | Database to scope data-plane commands to |
| `--ca-file` / `--cert-file` / `--key-file` | string | — | Client mTLS material |
| `--output` | string | `table` | `table \| json \| yaml` |
| `--timeout` | duration | `10s` | Request timeout |
| `--log-level` | string | `info` | `trace \| debug \| info \| warn \| error` |
| `--log-file` | string | stdout | Log file path |

`embedded` requires the desktop/GUI build. Every verb in this group works
against a running daemon on all five network transports.

## `qrdb kv put <key> <value>`

Store a key-value pair.

| Flag | Type | Default | Required |
|---|---|---|---|
| `--lease-id` | int | none | no |
| `--write-mode` | string | server default | no — `sync`, `wal_durable`, or `batched` |

```bash
qrdb --server https://db.example.com kv put app/config/timeout 30s
qrdb kv put session/abc --lease-id 7 --write-mode sync
```

## `qrdb kv get <key>`

Retrieve a value by key.

| Flag | Type | Default | Required |
|---|---|---|---|
| `--consistency` | string | `linearizable` | no — `strong`, `linearizable`, `eventual` |
| `--revision` | bool | false | no — include MVCC revision metadata |
| `--at-revision` | int | none | no — read at a historical revision |

```bash
qrdb kv get app/config/timeout
qrdb kv get app/config/timeout --consistency strong
qrdb kv get app/config/timeout --revision
qrdb kv get app/config/timeout --at-revision 42
```

`--at-revision` requires `raft.sm_type: mvcc` on the daemon.

## `qrdb kv del <key>`

Delete a key. Flags: `--write-mode` (as for `put`).

```bash
qrdb kv del app/config/timeout
```

## `qrdb kv exists <key>`

Report whether a key exists. Flags: `--consistency`.

```bash
qrdb kv exists app/config/timeout --consistency eventual
```

## `qrdb kv list [prefix]`

List keys, optionally filtered by prefix. Flags: `--consistency`.

```bash
qrdb kv list app/
```

Reserved internal prefixes are filtered out of the result unless the requested
prefix lies inside one.

## `qrdb kv range <start> [end]`

Return key-value pairs in the half-open interval `[start, end)`. With `end`
omitted, returns all keys `>= start`.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--consistency` | string | `linearizable` | read consistency |
| `--limit` | int | unlimited | maximum keys returned |
| `--revision` | int | current | query at a specific MVCC revision |
| `--keys-only` | bool | false | omit values |
| `--count-only` | bool | false | return only the match count |
| `--sort-by` | string | — | `key`, `value`, `version`, `create`, `mod` |
| `--sort-order` | string | — | `asc`, `desc` |

```bash
qrdb kv range app/a app/z --limit 100 --sort-by key --sort-order asc
qrdb kv range app/ --revision 42 --keys-only
```

## `qrdb kv revision`

Print the store's current MVCC revision. No flags.

```bash
qrdb kv revision
```

## `qrdb kv compact`

Discard the MVCC history of the database the command is scoped to (`--database`,
or the server default) older than `--revision`. Only that database is
compacted: another database's history, and another tenant's, is untouched.
The caller must belong to the tenant that owns the database and hold
`mvcc:compact`; the system tenant is no exception.

| Flag | Type | Required |
|---|---|---|
| `--revision` | int | yes -- compaction to this store revision; must be >= 1 and not above the current revision |

```bash
qrdb --database billing kv compact --revision 10000
```

Compaction is irreversible: as-of reads, watch replays and restores of this
database below the new floor fail afterwards. Compacting to a revision the
database is already compacted to is refused (`revision ... has been
compacted`) and changes nothing.

## `qrdb kv restore`

Return a bounded set of keys to the value they held at an earlier MVCC
revision, by proposing **new** writes. A logical undo, never a Raft rewind.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--revision` | int | — | target MVCC revision |
| `--prefix` | string | — | restore every key with this prefix (mutually exclusive with `--keys`) |
| `--keys` | strings | — | explicit comma-separated or repeatable key list (mutually exclusive with `--prefix`) |
| `--dry-run` | bool | false | print the plan, write nothing |
| `--force` | bool | false | overwrite keys modified since the plan was computed |

Requires `raft.sm_type: mvcc`. Exactly one of `--prefix` or `--keys` must be
given; neither or both is refused.

```bash
# Preview
qrdb kv restore --prefix app/config/ --revision 42 --dry-run

# Apply with compare-and-swap protection
qrdb kv restore --prefix app/config/ --revision 42

# Apply unconditionally
qrdb kv restore --keys app/a,app/b --revision 42 --force
```

**Exit codes.** When one or more keys conflict and `--force` was not set, the
command exits **non-zero** even though the request itself succeeded — a
partially applied restore reporting exit 0 reads as a complete undo. Conflicting
keys are listed in the output; re-run with `--force`, or re-run once the
conflicting writers have quiesced.

Plan output columns: `ACTION`, `KEY`, `FROM-REV`, `TO-REV`, `VALUE-SIZE`.
Actions are `revert` (value differs), `recreate` (key deleted since) and
`delete` (key added since).

## The guarded write verbs have no CLI surface

`PutIfAbsent`, `CompareAndSwap` and `CompareAndDelete` — the conditional writes
that let exactly one of several callers win a race — are **not** `qrdb kv`
subcommands. There is no `qrdb kv put-if-absent`. This is recorded here rather
than omitted so the next reader does not go looking for a missing verb, the
same way api.md records that `Exists` has no wire operation.

The reason is what the verbs are for. Each answers a boolean — did I store,
did I swap, did I delete — and `false` is a normal outcome, not a failure. A
caller has to branch on that answer, which is a program's job rather than a
shell's: a command whose refusal is a success would have to signal it through
an exit code, and an operator pasting a one-off claim into a terminal has
nobody to hand the claim to afterwards.

They are reached through a client instead, on every transport:

- **Go SDK** — a capability interface obtained by type assertion,
  `client.KV().(transport.GuardedKVClient)`.
- **REST** — `POST /kv/put-if-absent`, `/kv/compare-and-swap`,
  `/kv/compare-and-delete`.
- **gRPC and Unix** — `PutIfAbsent`, `CompareAndSwap`, `CompareAndDelete`.
- **QUIC** — `kv.PutIfAbsent` and siblings.
- **MCP** — the tools `qrdb_put_if_absent`, `qrdb_compare_and_swap` and
  `qrdb_compare_and_delete`, which take the same `--database` scope the plain
  KV tools do and require `kv:put`, `kv:put` and `kv:delete` respectively. An
  agent driving `qrdb kv` through a shell has these as first-class tools and
  does not need a subcommand for them.

See [api.md](api.md) for the wire contract and [user.md](user.md) for when to
reach for each one.

## Parity

Every verb above has a `CommandDefinition` in the integration parity suite. A
verb here without one, or the reverse, is a defect in whichever is behind.
The guarded verbs are outside that correspondence by the section above: they
have no command, so they have no definition, and adding one to satisfy the
suite would be inventing a command to make a document true.
