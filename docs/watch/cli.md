# Watch — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

One verb: `qrdb watch`. It accepts the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags).

## `qrdb watch <key>`

Watch for changes to a key or prefix. The command streams until interrupted,
until the server cancels it, or until the root `--timeout` (default `10s`)
elapses; pass a larger `--timeout` to watch longer, or `--timeout 0` for no
bound.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--prefix` | bool | false | watch all keys beginning with `<key>` rather than the exact key |
| `--prev-kv` | bool | false | include the previous key-value in each event |
| `--start-revision` | int | 0 (live only) | replay committed history from this revision forward, then continue live |

`--start-revision` works on a standalone node on every backend, and on a raft
node only with `raft.sm_type: mvcc`.

```bash
# Watch one key, live
qrdb watch app/config/timeout

# Watch a whole prefix and show what each value was before the change
qrdb watch app/config/ --prefix --prev-kv

# Catch up from a revision you recorded earlier, then keep watching
qrdb watch app/config/ --prefix --start-revision 4711

# Machine-readable event stream
qrdb --output json watch service/registry/ --prefix
```

**Exit codes.** Interrupting the command (Ctrl-C) or reaching `--timeout` exits
zero. A watch the server refuses at open — a compacted start revision, a store
that cannot replay, a permission failure — exits non-zero with the error on
stderr. A cancellation that arrives in-band, after the stream opened (on QUIC
and MCP a refusal arrives this way too), prints `watch canceled: <reason>` on
stdout and exits zero.

**Output.** With the default `--output`, one line per event:
`<PUT|DELETE> <key> <value>`, followed by the previous key and value when
`--prev-kv` is set. `--output json` prints each `WatchResponse` as one JSON
object, revision included.

## Transport notes

Watch is a streaming operation and works on every network transport as well as
embedded. `--transport unix` is the default and the usual choice on the same
host.

## Parity

`qrdb watch` is driven on every network transport by `watch_cli_test.go`
(`make integration-test-watch`), including the `--start-revision` replay case.
Per-transport accounting: the `watch` row of
[`test/integration/COVERAGE.md`](../../test/integration/COVERAGE.md).
