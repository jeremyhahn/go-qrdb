# Watch — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

One verb: `qrdb watch`. It accepts the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags).

## `qrdb watch <key>`

Watch for changes to a key or prefix. The command streams until interrupted or
until the server cancels it.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--prefix` | bool | false | watch all keys beginning with `<key>` rather than the exact key |
| `--prev-kv` | bool | false | include the previous key-value in each event |
| `--start-revision` | int | 0 (live only) | replay committed history from this revision forward, then continue live |

`--start-revision` requires the daemon to run `raft.sm_type: mvcc`.

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

**Exit codes.** Interrupting the command (Ctrl-C) exits zero. A server-side
cancellation — a compacted start revision, a store that cannot replay, a
permission failure — exits non-zero with the reason on stderr, so a supervising
script can tell "you stopped it" from "it stopped on you".

**Output.** Each event carries the event type (`PUT` or `DELETE`), the key, the
new value, the revision, and — with `--prev-kv` — the previous value.

## Transport notes

Watch is a streaming operation and works on every network transport as well as
embedded. `--transport unix` is the default and the usual choice on the same
host.

## Parity

`qrdb watch` has a `CommandDefinition` in the integration parity suite.
