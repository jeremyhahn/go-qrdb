# Run Modes — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26
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

**Pick any backend.** The memory, file, pebble and objstore backends all serve
the whole data plane, revisions, watch, leases and locks included.

**Compile in the optional features you want.** Build tags are the outermost
gate — config cannot switch on something the binary does not contain. SQL,
the MySQL and PostgreSQL protocols, etcd, time series and vectors are built in
by default; `=0` drops one. Full-text search is opt-in:

```bash
make build WITH_QRDB_FTS=1
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

**"requires mode: raft" or "cluster operation not supported".** The feature
needs a cluster: the etcd listener, membership or a snapshot. Rebalance and
shard backup need one too. Move to raft mode — a single-node raft server is enough — or do without
it.

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

**A restarted raft node has not started serving `/health`.** It is waiting on
its cluster, and its log says on what: every 5 seconds it logs a WARN line,
`startup read still waiting` or `startup still waiting`, naming the step, the
time spent and the last error. A quorum of the cluster has to be running for the
wait to end. A wait that runs out of its budget stops the start with an error
naming the step, and the node can simply be started again.

**Switching modes.** The mode is fixed at composition. Changing it is a restart,
not a runtime operation.
