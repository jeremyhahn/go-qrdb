# Watch — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

## Prerequisites

A reachable server, client certificates, an unsealed barrier, and the
`watch:watch` permission, which covers key and prefix watches alike.

To replay history rather than only watch live, a raft node must run
`raft.sm_type: mvcc`; a standalone node replays on every backend.

## First run

```bash
qrdb watch app/config/timeout
```

The command blocks and prints an event every time the key changes, until you
stop it or the root `--timeout` (default `10s`) elapses; add `--timeout 0` to
watch with no bound. In another terminal:

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
qrdb --timeout 0 watch app/config/myservice | while read -r _; do
  systemctl reload myservice
done
```

## Troubleshooting

**The command exits immediately with "required start revision has been compacted".**
The history you asked for has been discarded by compaction. There is no way to
recover it through a watch — re-read the current state with `kv get` or
`kv range`, note the revision, and start a live watch from there.

**"start revision replay is not supported by this store".** The server keeps no
revision history. Drop `--start-revision` and watch live; getting replay is a
server configuration change (`raft.sm_type: mvcc` on a raft node), not a
client flag.

**Nothing ever prints.** Check that you are watching the key you think you are:
a prefix watch needs `--prefix`, and without it `app/config/` is an exact key
that probably does not exist. Also confirm the database scope — `--database`
must match where the writes are going.

**The watch stopped on its own.** A watch the server refused at open exits
non-zero with the reason on stderr. A cancellation after the stream opened
prints `watch canceled: <reason>` and exits zero. A watch that reached
`--timeout`, or that you stopped with Ctrl-C, exits zero with no message.

**You need guaranteed delivery over hours or days.** A watch replays only as far
back as compaction retains. If you need durability beyond that window, record
the revision you have processed and re-read state on reconnect, or use a queue
rather than a watch.
