# Run Modes — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-10

A qrdb server runs in one of two modes. The mode decides which features exist,
so it is usually the answer when something "is not there".

## The two modes

**Standalone** — one process, no consensus. Fast, simple, no cluster to
operate. You get the standard database operations: keys and values, batch,
**compare-and-swap transactions**, indexes, collections, databases, SQL, time
series and vectors.

**Raft** — the consensus layer. Everything standalone has, plus the things that
need a replicated log: revisions and historical reads, watch, leases, locks,
etcd compatibility, cluster membership, rebalance and shard backup/restore. A
single-node raft deployment is a one-replica group — same write path as a
cluster, and it scales out cleanly later.

*(Transactions were previously listed here as raft-only. They are not: a
standalone node is single-process, so its compare-and-swap is already
linearizable, and the server passes the transaction family through in both
modes. Corrected to match the code.)*

**If in doubt, run raft.** A single-node raft server gives you the full feature
set and can grow; standalone cannot.

## Which mode am I on?

```bash
qrdb admin capabilities --output json
```

The `mode` field is the answer. The rest of the report is the effective
capability profile.

## Choosing the mode

The mode is a **server** setting. In the daemon's config:

```yaml
mode: raft          # "standalone" | "raft"; absent means raft
```

or on the command line:

```bash
qrdbd start --mode standalone
```

## Common tasks

**Run embedded, in your own process.** Embedded is a deployment shape of
standalone, not a third mode:

```go
node, err := node.NewStandaloneNode(...)
client := node.Client()
defer node.Close()
```

For the full feature set in process, use `node.NewClusteredNode` instead.

**Compile in the optional features you want.** Build tags are the outermost
gate — config cannot switch on something the binary does not contain.

```bash
make build WITH_QRDB_SQL=1 WITH_QRDB_PGSQL=1 WITH_QRDB_VECTOR=1 WITH_QRDB_TS=1
```

**Turn something off that is compiled in.** Config can narrow, never widen:
`mvcc.enabled`, `watch.enabled` and `lease.enabled` can be set false even in
raft mode.

## Troubleshooting

**"requires mode: raft (standalone serves standard database operations only)".**
Exactly what it says. The feature needs the consensus layer. Either move to raft
mode — a single-node raft server is enough — or do without it. It is an error
rather than a silent no-op on purpose: a watch that never fires would be far
harder to diagnose.

**A capability reports `false` and you do not know why.** Three possibilities:
it was not compiled in, the run mode forbids it, or config disabled it. Check
the `mode` field, then check how the binary was built. The report cannot tell
you which of the three on its own.

**The daemon refused to start, naming a build tag.** You enabled a wire protocol
whose tag is absent. Rebuild with the tag, or remove the protocol from the
config. This is a deliberately separate check from ordinary config validation,
so config files stay valid independent of how a binary was built.

**The daemon refused to start on a standalone config.** You asked for something
standalone cannot do: `mvcc.enabled`, `watch.enabled` or `lease.enabled` set
true, a `raft.join`, or a multi-member `raft.peers` list. Remove it, or switch
to raft mode.

**`raft.sm_storage` was rejected.** Objstore is a snapshot and backup tier and
is refused as a hot apply path. Use `pebble` (the default), `file` or `memory`.
Note also that `sm_storage` applies only to `sm_type: disk` and `fast`; every
other state machine carries its own storage and rejects a non-pebble
`sm_storage`.

**Time series or vector operations are unavailable even though the tags are
compiled in.** Both need the MVCC-capable engine, so they do not work over the
file or objstore backends.

**Your SDK standalone code stopped composing MVCC and lease services.** That is
the ratified behaviour change: those became raft-mode-only everywhere, and
standalone compositions no longer build them. Move to `NewClusteredNode` if you
need them. **Transactions are not in that list** — this paragraph used to name
them and was wrong; compare-and-swap still works standalone, on every
transport.

**Switching modes.** The mode is fixed at composition. Changing it is a restart,
not a runtime operation.
