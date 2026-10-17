# Run Modes — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06
> Conflicts with this document are defects. Fix them here first.

Test reference: `make integration-test-run-modes`; coverage row: `test/integration/COVERAGE.md` `[run-modes](../../docs/run-modes/)`.

A qrdb server runs in one of two modes. The mode decides whether there is a
cluster, so it is the answer when a cluster feature "is not there".

## The two modes

**Standalone** — one process, no consensus. Fast, simple, no cluster to
operate. You get the whole data plane: keys and values, batch,
compare-and-swap transactions, **revisions and historical reads, watch,
leases and locks**, indexes, collections, databases, SQL, time series and
vectors. Your application code is the same as against a raft server.

**Raft** — the consensus layer. Everything standalone has, plus what needs a
cluster: replication, cluster membership, rebalance, shard backup/restore and
etcd compatibility. A single-node raft deployment is a one-replica group —
same write path as a cluster, and it scales out cleanly later.

*(Watch, leases, locks and revisions were previously listed here as raft-only.
They are served in standalone too since 2026-10-01; corrected to match the
code. Transactions were listed as raft-only before that, and were not.)*

**Run standalone** when one process is enough and you want no cluster to
operate. **Run raft** when you need replicas, or expect to grow into a cluster.

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
standalone, not a third mode, and it serves watch, leases and locks:

```go
node, err := node.NewStandaloneNode(...)
client := node.Client()
defer node.Close()

lease, _ := client.Lease().LeaseGrant(ctx, 10)
lock, _ := client.Lock().LockTryLock(ctx, &transport.LockAcquireRequest{
	Key: "scheduler", HolderID: "node-a", TTL: 10,
})
```

**Leader election on a single process.** A lock is a lease: the holder keeps
it alive, and if the process dies the lease expires and the lock is free for
the next holder. On a standalone node the server runs the expiry itself.

**Pick a backend that serves what you need.** The pebble and memory backends
serve every data-plane family. The file and objstore backends keep no
revisions and no leases, so they serve watch and value/version transactions
but refuse leases, locks, revisions, revision compares and any write that
names a lease with a typed "unsupported" error.

**Compile in the optional features you want.** Build tags are the outermost
gate — config cannot switch on something the binary does not contain.

```bash
make build WITH_QRDB_SQL=1 WITH_QRDB_PGSQL=1 WITH_QRDB_VECTOR=1 WITH_QRDB_TS=1
```

**Turn something off that is compiled in.** Config can narrow, never widen:
`mvcc.enabled`, `watch.enabled` and `lease.enabled` can be set false in either
mode.

## What survives a restart

Leases do. A lease held when the server stops is still there when it starts
again, with its keys, and gets **one full TTL** from the restart to be renewed.
A holder that is still running renews as usual; a holder that died with the
process loses its keys — and its locks — one TTL after the restart. This is the
same in both modes. Watches do not survive: a consumer reconnects and resumes
from the last revision it saw.

## Troubleshooting

**"requires mode: raft".** The feature needs a cluster: membership, rebalance,
shard backup, or the etcd listener. Move to raft mode — a single-node raft
server is enough — or do without it.

**A lease or lock call is refused as "unsupported" on a standalone node.** The
node runs on the file or objstore backend, which keeps no leases. Use pebble or
memory.

**A capability reports `false` and you do not know why.** Three possibilities:
it was not compiled in, the run mode forbids it, or config disabled it. Check
the `mode` field, then check how the binary was built. The report cannot tell
you which of the three on its own.

**The daemon refused to start, naming a build tag.** You enabled a wire protocol
whose tag is absent. Rebuild with the tag, or remove the protocol from the
config. This is a deliberately separate check from ordinary config validation,
so config files stay valid independent of how a binary was built.

**The daemon refused to start on a standalone config.** You asked for a
cluster: a `raft.join`, or a multi-member `raft.peers` list. Remove it, or
switch to raft mode.

**`raft.sm_storage` was rejected.** Objstore is a snapshot and backup tier and
is refused as a hot apply path. Use `pebble` (the default), `file` or `memory`.
Note also that `sm_storage` applies only to `sm_type: disk` and `fast`; every
other state machine carries its own storage and rejects a non-pebble
`sm_storage`.

**Time series or vector operations are unavailable even though the tags are
compiled in.** Both need the MVCC-capable engine, so they do not work over the
file or objstore backends.

**A restarted raft node has not started serving `/health`.** It is waiting on
its cluster, and its log says on what: every 5 seconds it logs a WARN line,
`startup read still waiting` or `startup still waiting`, naming the step, the
time spent and the last error. A quorum of the cluster has to be running for the
wait to end. A wait that runs out of its budget stops the start with an error
naming the step, and the node can simply be started again.

**Switching modes.** The mode is fixed at composition. Changing it is a restart,
not a runtime operation.
