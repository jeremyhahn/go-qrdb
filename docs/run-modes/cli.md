# Run Modes — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-27
> Conflicts with this document are defects. Fix them here first.

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

`qrdb admin capabilities` reports `cluster`, `backup` and `rebalance` false on a
standalone daemon. The raft-only families are the etcd protocol, `backup`,
`rebalance` and the `admin` cluster verbs (`member`, `snapshot`). The etcd
listener's refusal names the remedy:

```text
etcd: not implemented on this server: requires mode: raft
```

The `admin member` and `admin snapshot` verbs fail with the standalone
backend's error, which names the operation:
`backend: cluster operation not supported: <op>`.

The families that work in standalone, exactly as in raft mode, on every
backend: `kv` (put/get/del/list/exists/range, `revision`, `get --at-revision`,
`compact`, `restore`), `watch`, `lease`, `lock`, `tx`, `batch`, `index`,
`collection`, `database`, `sql` (with `qrdb_sql`), `ts` and `vector` (with
their tags), plus the always-compiled core — `auth`, `users`, `roles`,
`barrier`, `ceremony`, `credential`, `ca`, `audit`.

## Exit codes

`qrdbd` and `qrdb` exit `0` on success and `1` on any error, which they print
to stderr. A mode refusal is an error like any other, so a raft-only verb run
against a standalone daemon exits `1`; `qrdb admin capabilities` itself exits
`0` whatever it reports.

## Build-time feature selection

Build tags are the outermost gate; config cannot enable what the binary lacks,
and a wire protocol enabled without its tag is a **boot-time error naming the
tag**.

The `WITH_QRDB_TS`, `WITH_QRDB_VECTOR`, `WITH_QRDB_SQL`, `WITH_QRDB_MYSQL`,
`WITH_QRDB_PGSQL` and `WITH_QRDB_ETCD` flags default to `1`; set one to `0` to
drop its feature. `WITH_QRDB_FTS` and `WITH_QRDB_GPU` default to `0`.

```bash
make build WITH_QRDB_FTS=1                       # add full-text search
make build WITH_QRDB_MYSQL=0 WITH_QRDB_PGSQL=0   # drop the SQL wire protocols
```

There is deliberately **no `qrdb_raft` tag** — raft is always compiled and the
mode selects it at runtime.

## Parity

`qrdb admin capabilities` runs on every transport in the integration suite
(`test/integration/admin_cluster_cli_test.go`,
`test/integration/run_modes_standalone_cli_test.go`). `qrdbd start` is a daemon entry point, not a client verb, and is
exercised by the daemon's own tests.
