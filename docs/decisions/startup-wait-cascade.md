# A bounded startup wait during leader churn exits the node and its peers

> **Status: RULED — option A (owner ruling 249), built** · Scope: go-qrdb
> Last verified: 2026-10-28
> The Problem describes the behaviour before the ruling. The owner chose
> option A for the system-shard settling waits; the Ruling section says what
> was built.

## Problem

The boot waits that sit after the system shard's leader wait are bounded and
fail the boot when their budget runs out
([../clustering/engineering.md](../clustering/engineering.md), "Every startup
read is bounded per attempt"): the settling reads
(`server.WhileSystemShardSettling`, `server.SystemShardSettleBudget`, 15s:
the feature-gate start in `cmd/qrdbd/feature_gate.go`, database provisioning
in `cmd/qrdbd/provision.go`, the dynamic-membership start), the
shard-serving wait (`server.WaitForShardServing`, 15s) and the first admin's
replication wait (`waitForAdminReplication` in `cmd/qrdbd/start.go`, 30s).

The leader wait itself runs in rounds for as long as the node runs, because
a node that gave up would exit and leave its peers waiting on it. The waits
after it do not, and the same thing happens to them.

On a three-node mvcc cluster under CPU contention alone (no fsync or I/O
load):

1. Leadership changes three times in about 20s, and each leader loses quorum.
2. Nodes 2 and 3 each exhaust the 15s budget of the feature-gate start on a
   `featuregate` `list activations` read that times out, log the error and
   exit.
3. Node 1 is left without a quorum. It campaigns, reaches its 30s admin-
   replication wait and exits too.

The product does what the contract says, and the contract turns a 20s
election storm into a cluster that is down for good. Without a supervisor
that restarts nodes the cluster does not recover; with one it recovers only
by restarting.

## Options

- **A (recommended).** A bounded wait counts its budget only while the system
  shard has a leader that has committed in its term. When the read fails with
  a settling answer (no leader, or a leader without an address), it goes back
  to the leader wait, which runs in rounds. A read against a stable leader
  that still does not answer fails the boot. The node then exits only for a
  fault, never for an election.
- **B.** Every startup wait runs in rounds, as the leader wait does, and only
  a signal ends it. This is the simplest, but a read that is genuinely stuck
  holds the boot forever, and the only sign is the progress log.
- **C.** Keep the contract. An exit is the signal, and a supervisor restart is
  the recovery. Tests on a stalled host stay red.

## Ruling

Owner ruling 249 chose **option A** for the system-shard settling waits: the
feature-gate start (its activation check and its self-advertisement),
database provisioning, the dynamic-membership start, the database-catalog
open and the volume-tenant restore, every wait that runs through
`server.WhileSystemShardSettling`.

- The 15s budget (`server.SystemShardSettleBudget`) counts only while the
  system shard has a leader that has committed in its term: a leader is known
  and a linearizable read probe answers.
- An attempt that fails with a settling answer, or is cut off unanswered,
  goes back to the open-ended leader wait under the boot's context. A node
  does not exit because the cluster is still forming.
- A leader that has committed and still does not answer within the budget
  fails the boot, as before.

The contract is in [../clustering/engineering.md](../clustering/engineering.md),
"A settling wait outlasts a forming cluster". The shard-serving wait and the
first admin's replication wait are outside this ruling and keep their
budgets.
