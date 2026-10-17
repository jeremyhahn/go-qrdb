# A bounded startup wait during leader churn exits the node and its peers

> **Status: PROPOSED — pending owner ruling** · Scope: go-qrdb
> Last verified: 2026-10-17
> The Problem describes shipped behaviour; the Options are open, for the
> owner. Nothing here is built until the owner rules.

## Problem

The boot waits that sit after the system shard's leader wait are bounded and
fail the boot when their budget runs out
([../clustering/engineering.md](../clustering/engineering.md), "Every startup
read is bounded per attempt"): the settling reads
(`server.WhileSystemShardSettling`, 15s: the feature-gate start in
`cmd/qrdbd/feature_gate.go`, the boot tenant list, the migrator reads, the
dynamic-membership start), the shard-serving wait
(`server.WaitForShardServing`, 15s) and the first admin's replication wait
(`waitForAdminReplication` in `cmd/qrdbd/start.go`, 30s).

The leader wait itself runs in rounds for as long as the node runs, because
"a node that gave up would exit and leave its peers waiting on it". The waits
after it do not, and the same thing happens to them.

Reproduced on a three-node mvcc cluster with CPU contention only (twelve
busy loops; no fsync or I/O load):

1. Leadership changes three times in about 20s, and each leader loses quorum.
2. Nodes 2 and 3 each reach the 15s budget of the feature-gate start. The
   error is `featuregate: list activations: service: read failed: quicraft:
   timeout`. On node 3 that was four attempts, three of them cut off. Both nodes log the
   ERROR and exit.
3. Node 1 is left without a quorum. It campaigns, reaches its 30s admin-
   replication wait and exits too.

The product does what the contract says, and the contract turns a 20s
election storm into a cluster that is down for good. The harness does not
restart the nodes, so the test that was waiting fails. A supervisor that
restarts nodes would recover the cluster, but only by restarting it.

## Options

- **A (recommended).** A bounded wait counts its budget only while the system
  shard has a leader that has committed in its term. When the read fails with
  a settling answer (no leader, or a leader without an address), it goes back
  to the leader wait, which runs in rounds. A read against a stable leader
  that still does not answer fails the boot as it does today. The node then
  exits only for a fault, never for an election.
- **B.** Every startup wait runs in rounds, as the leader wait does, and only
  a signal ends it. This is the simplest, but a read that is genuinely stuck
  holds the boot forever, and the only sign is the progress log.
- **C.** Keep the contract. An exit is the signal, and a supervisor restart is
  the recovery. Tests on a stalled host stay red.
