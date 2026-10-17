# Watch — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19
> Conflicts with this document are defects. Fix them here first.

## Prerequisites

A reachable server, client certificates, an unsealed barrier, and the
`watch:watch` permission (or `watch:watch_prefix` to watch a whole prefix).

To replay history rather than only watch live, the server must run
`raft.sm_type: mvcc`.

## First run

```bash
qrdb watch app/config/timeout
```

The command blocks and prints an event every time the key changes. In another
terminal:

```bash
qrdb kv put app/config/timeout 45s
```

Ctrl-C to stop.

## Common tasks

**Watch everything under a prefix.**

```bash
qrdb watch service/registry/ --prefix
```

**See what the value was before each change.**

```bash
qrdb watch app/config/ --prefix --prev-kv
```

**Feed a script.** JSON output gives you one object per event.

```bash
qrdb --output json watch app/config/ --prefix | while read -r ev; do
  echo "$ev" | jq -r '"\(.events[0].type) \(.events[0].kv.key)"'
done
```

**Catch up after a disconnect without missing changes.** Record the revision you
last processed, then start from it. The server replays the gap and continues
live with no seam.

```bash
qrdb kv revision                 # note this number before you go offline
qrdb watch app/config/ --prefix --start-revision 4711
```

This is the difference between a watch and a poll: with `--start-revision` you
see every change in order, including two changes to the same key.

**React to configuration changes.** A common pattern is a supervisor that
restarts a process when its config key moves:

```bash
qrdb watch app/config/myservice | while read -r _; do
  systemctl reload myservice
done
```

## Troubleshooting

**The command exits immediately with "start revision has been compacted".**
The history you asked for has been discarded by compaction. There is no way to
recover it through a watch — re-read the current state with `kv get` or
`kv range`, note the revision, and start a live watch from there.

**"start revision replay is not supported by this store".** The server keeps no
revision history. Drop `--start-revision` and watch live; getting replay is a
server configuration change (`raft.sm_type: mvcc`), not a client flag.

**Nothing ever prints.** Check that you are watching the key you think you are:
a prefix watch needs `--prefix`, and without it `app/config/` is an exact key
that probably does not exist. Also confirm the database scope — `--database`
must match where the writes are going.

**Permission denied when adding `--prefix`.** Prefix watching is a separate,
broader permission (`watch:watch_prefix`) from single-key watching. Ask for it
explicitly.

**The watch stopped on its own.** Look at the exit status and the message on
stderr. A server-side cancellation always says why; a clean Ctrl-C exits zero.

**You need guaranteed delivery over hours or days.** A watch replays only as far
back as compaction retains. If you need durability beyond that window, record
the revision you have processed and re-read state on reconnect, or use a queue
rather than a watch.
