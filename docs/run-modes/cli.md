# Run Modes — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

The run mode is a **server** property, selected on `qrdbd`. The `qrdb` client
has no verb that changes it — it only reports the result.

## Selecting the mode — `qrdbd`

| Form | Value | Default |
|---|---|---|
| YAML | `mode: standalone \| raft` | absent means `raft` |
| Flag | `--mode standalone` | as above |

```bash
qrdbd start --mode standalone
qrdbd start                      # raft (the default when mode is absent)
```

Absent means `raft` for full backward compatibility with configurations written
before the mode existed.

Standalone skips the raft identity requirements; only `raft.data_dir` remains
mandatory.

## Reporting the effective mode — `qrdb admin capabilities`

This is the authoritative runtime report and the first thing to check when a
feature appears to be missing. It reports the sealed profile — build tags
intersected with the run mode intersected with config — including a `mode`
field.

```bash
qrdb admin capabilities --output json
```

A capability reported `false` means one of three things: not compiled in,
gated by the run mode, or disabled in config. The `mode` field plus knowing how
the binary was built is what tells them apart.

## Recognising a mode refusal

Cluster verbs in a standalone daemon fail with a typed error naming the
remedy:

```bash
$ qrdb admin member list
error: requires mode: raft
```

The families that refuse in standalone: the etcd protocol, `backup`, and the
`admin` cluster verbs (`member`, `peers`, `snapshot`, `topology`, rebalance).

The families that work in standalone, exactly as in raft mode: `kv`
(get/put/del/list/scan/range, `revision`, `get --at-revision`, `compact`,
`restore`), `watch`, `lease`, `lock`, `tx`, `batch`, `index`, `collection`,
`database`, `sql` (with `qrdb_sql`), `ts` and `vector` (with their tags), plus
the always-compiled core — `auth`, `users`, `roles`, `barrier`, `ceremony`,
`credential`, `ca`, `audit`.

*(Corrected: `watch`, `lease`, `lock` and the `kv` revision verbs were listed
as refusing until 2026-10-01, and `tx` was listed in error before that.)* On
the file and objstore backends, `lease`, `lock` and the revision verbs refuse
as unsupported, because those engines keep no leases or revisions; `tx` is
served there, except that a revision compare or an op naming a lease is
refused; see
[api.md](api.md#the-storage-engine-not-the-mode-bounds-the-coordination-families).

## Build-time feature selection

Build tags are the outermost gate; config cannot enable what the binary lacks,
and a wire protocol enabled without its tag is a **boot-time error naming the
tag**.

```bash
make build WITH_QRDB_SQL=1 WITH_QRDB_PGSQL=1 WITH_QRDB_VECTOR=1 WITH_QRDB_TS=1
```

There is deliberately **no `qrdb_raft` tag** — raft is always compiled and the
mode selects it at runtime.

## Parity

`qrdb admin capabilities` has a `CommandDefinition` in the integration parity
suite. `qrdbd start` is a daemon entry point, not a client verb, and is
exercised by the daemon's own tests.
