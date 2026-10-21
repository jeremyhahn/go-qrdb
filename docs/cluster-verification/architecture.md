# Cluster Verification — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

<!--
Copyright 2026 Jeremy Hahn

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

Covers what "Raft clustering and storage proven" means for go-qrdb and the
harness that proves it: the gate, the exit criteria, the workload and its
ledger, the history format, the nemesis faults, the soak and its evidence
bundle, and the short fault tests that run in CI. **Raft mode only** — see
[../run-modes/](../run-modes/architecture.md) and
[../clustering/](../clustering/architecture.md).

## Problem

Unit tests and the per-feature integration suites prove that each operation
does what it says on a healthy cluster. They do not prove the property a
database is bought for: that an acknowledged write survives every failure the
cluster claims to tolerate, that every read observes a state consistent with a
single serial order, and that every replica converges to the same state. Those
properties fail only under concurrency and faults — a leader killed between
commit and acknowledgement, a partition that heals mid-election, a disk that
stalls under fsync — and a test suite that never injects them cannot fail on
them.

"It ran for a while and nothing looked wrong" is not evidence. The proof has to
be a recorded history checked by a tool that can say *no*, against criteria
written down before the run.

## Shape chosen

A **client-side harness** drives a real three-node cluster (`node-1`,
`node-2`, `node-3`) through its public transports while a **nemesis** injects
faults. Every operation goes into a **history** and every fault into the
nemesis's **event log**, both on one monotonic clock. After the run the
**ledger** and any configured **checker** decide, from the history alone,
whether acknowledged writes survived and the history is linearizable; the
liveness and replica checks decide the rest.

```
            ┌──────────── harness (test/clusterproof, tag clusterproof) ───────────┐
            │                                                                      │
            │  workload ──► clients ──REST/gRPC/QUIC──►  node-1  node-2  node-3    │
            │                  │                           ▲       ▲       ▲       │
            │                  └──► history                │       │       │       │
            │  nemesis player ────► event log              └─── targets ───┘       │
            │                                                                      │
            │  after the run: heal all, liveness (E4), final read, replicas (E3)   │
            │  then over the history: ledger (E1) + checkers (E2)                  │
            │                 ──► verdict + evidence bundle                        │
            └──────────────────────────────────────────────────────────────────────┘
```

The harness is an ordinary black-box client. It needs nothing from the server
that a production client does not have, apart from the read-only per-replica
state comparison described under [Exit criteria](#exit-criteria).

## The gate

go-qrdb's Raft clustering and storage are **proven** when, on one recorded
soak against a three-node cluster, with the nemesis enabled, every exit
criterion below holds and the evidence bundle that shows it is published with
the run. Until that run exists the claim is not made, in documentation or in a
release note.

The gate is a property of a **run**, not of the tree. A change to the
consensus library, the storage engines, the state machines or the transport
that a release carries is re-proven by a new run before that release claims
the gate.

## Exit criteria

| # | Criterion | How it is decided | Target |
|---|---|---|---|
| E1 | Zero acknowledged-write loss | the ledger over the history: every key's final read, taken through the client surface after the cluster recovered, returns a value some acknowledged or possibly-applied write could have left last; no read returns a value never written; no value reappears after it was replaced | **0** |
| E2 | Zero linearizability violations | the checker finds a linearization for the whole recorded history (every key, every operation) | **0** |
| E3 | Zero replica divergence | after the cluster is quiesced and healed, every replica of every shard reports the identical state-machine hash at the identical applied index | **0** |
| E4 | Zero permanently halted shards | every shard has a leader and every node accepts a write within the recovery bound after the last fault heals | **0** |
| E5 | Failover time | per fault window, the first acknowledged write after the inject | **to be measured, then ratified** |
| E6 | Recovery time | the last heal to every shard led and every node writable | **to be measured, then ratified** |
| E7 | Soak throughput and latency | sustained operations per second and p50 / p99 / p99.9 latency, overall and inside and outside fault windows | **to be measured, then ratified** |

E1 to E4 are **absolute**: a single violation fails the gate, whatever else
the run shows, and a criterion that cannot be decided makes the run invalid,
never proven. E5 to E7 have no number yet. The first soak measures them; a
bound is ratified from that measurement; later runs are held to it. A run
before ratification reports the figures and makes no pass or fail claim on
them.

E2 is decided by porcupine, a linearizability checker the harness carries as
a test-only dependency. E3 is decided on the clustering capability that
reports each replica's applied index and state-machine hash, asked of every
node ([api.md](api.md#replica-state-comparison)); a node that cannot answer
leaves E3 unavailable, never passed.

## Seal types

The release ships three node barrier seal strategies, and the proof runs under
each of them:

| Seal type | After a restart | How the proof unseals a restarted node |
|---|---|---|
| `none` | unsealed: the node opens its own barrier at boot | nothing to do |
| `passphrase` | **sealed** until its own passphrase is supplied | the operator's unseal command for that node |
| `shamir` | **sealed** until a quorum of share holders unseals it | the operator's unseal command for that node |

Hardware-backed strategies (TPM 2.0, PKCS#11, cloud KMS) are not part of
go-qrdb; they are composed in by the products built on it and are proven
there.

A run names its seal type (`PROOF_SEAL`, [cli.md](cli.md#make-cluster-proof)),
and the run record and verdict carry it. A run that names a type the release
does not ship is refused before it starts, and a bundle that names one is
invalid. A gate claim is per seal type: a run proves the gate for the type it
ran under and says nothing about the others.

A restarted node under a sealed type must refuse every read and write until it
is unsealed. Serving while sealed is a defect, not an outcome the proof
tolerates: the in-CI SIGKILL test asserts that a restarted `passphrase` node
reports sealed and refuses data operations before it unseals it. A paused
process keeps its keys in memory and needs no unseal.

**Known limitation.** A three-node cluster cannot yet initialize a `shamir`
node barrier: share holders are enrolled with a certificate the founding node
signs on its first boot, and a node that joined an existing cluster holds no
key to sign one. The proof accepts `shamir`, and a run against such a cluster
works the same way as `passphrase`; the in-CI test runs `none` and
`passphrase` until a joined node can enroll holders. How a joined node does so
is not decided.

## Components

| Component | Package | Responsibility |
|---|---|---|
| Workload | `test/clusterproof/workload` | generates the operation mix against a key space; owns concurrency, reconnection and the final read |
| History | `test/clusterproof/history` | the record of every operation, call and return on the run's clock, and the `Checker` interface every check implements |
| Ledger | `test/clusterproof/ledger` | the acknowledged-write check over the history (E1) |
| Linearizability | `test/clusterproof/linearizability` | the porcupine check over the history (E2): every key a register, the whole history linearizable |
| Nemesis | `test/clusterproof/nemesis` | the faults, their targets, the seeded schedule, and the player that injects and heals them and records each step |
| Soak | `test/clusterproof/soak` | runs the workload and the nemesis for the configured duration, heals, runs the checks, writes the evidence bundle, and re-checks a bundle |
| Container mode | `test/clusterproof/container` | reads the node inventory, starts each node's `qrdbd` container from the release image with a static bootstrap, takes each node through first boot, and checks the result as BOOT-01 |
| In-CI fault tests | `test/integration/cluster_crash_test.go`, `cluster_partition_test.go`, `cluster_lease_test.go` | short, self-spawned SIGKILL, partition and lease-read tests that run on every CI build; SIGKILL runs once per seal type as subtests |

`test/clusterproof/` is a Go module of its own, and every file in it is behind
the `clusterproof` build tag, so it compiles only when asked for and never
ships in a binary. The separate module keeps porcupine, a test-only
dependency, out of the database's module graph: nothing the go-qrdb module
builds or resolves names it. It is a test harness: it imports go-qrdb through
the Go SDK the way any client does, and go-qrdb never imports it.

## Bring-up: BOOT-01

A soak needs a cluster, and bringing one up from the shipped artifact is the
first thing the proof checks. Container mode runs the image built from the
repository's release `Dockerfile` — the image a hosted deployment runs
([../deployment/](../deployment/architecture.md)) — on each node of an
**inventory**, a configuration file that names the image, how to reach each
node's container runtime, and each node's address, replica ID, TLS directory
and data directory. No host name or address is written into the harness.

1. **Identities.** For each node, the harness runs `qrdbd node-identity`
   from the image over that node's own certificate. Every founder's
   identity goes into every founder's member list.
2. **Static bootstrap.** Each node starts as container
   `clusterproof-<name>`. Every node gets the same `--peers` and
   `--member-identities` lists, its own `--replica-id`, and the TLS and data
   directories mounted in.
3. **First boot.** Each node goes through the first-boot ceremony over REST,
   in parallel ([../firstboot/](../firstboot/architecture.md)). A node whose
   barrier is already initialized is left alone.
4. **BOOT-01.** Over QUIC, gRPC and REST, every node must report:
   - health `ok`;
   - the same leader;
   - its own Raft address, in both status and topology;
   - at least one shard;
   - every founder in the system shard's peers, at that founder's Raft
     address;
   - mode `raft` in its capabilities.

   Exactly one node must claim leadership, and it must be the agreed leader.

BOOT-01 retries each transport's round until it holds or until the
five-minute bound expires. When the bound expires, it fails with the last
reason. On a failure it reports the tail of every container's log. The
containers are removed when it ends, so a stale container cannot hold a
name into the next run.

BOOT-01 injects no fault and records no history. It is a precondition for
the gate, not one of its criteria: a soak runs against a cluster brought up
this way.

## Data flow

1. The soak records each node's version and configuration digest, then
   starts the workload's clients against the configured nodes over one
   public transport, and the nemesis player on its seeded schedule.
2. Each client records one history entry per operation: its call and return
   times and its outcome — `ok`, `fail` (the server definitely did not apply
   it) or `indeterminate` (a timeout or a lost connection, where a write may
   or may not have been applied).
3. The player injects each fault, holds it, heals it, and records every step
   in its event log on the same clock. A fault that fails to inject stops
   the load. Under a sealed type, healing a killed node restarts it and then
   unseals it.
4. When the duration expires the soak heals every fault again, waits for
   every shard to be led and every node to accept a probe write (E4), runs
   the final read of every key, and asks for each replica's state (E3).
5. It writes the history, the event log and the run record, reads them back,
   and from those bytes derives the timeline, the timings and the verdict:
   the ledger over the history (E1), every configured checker (E2), and the
   recorded E3 and E4 observations.
6. It writes the manifest of every file's SHA-256 and exits `0` (proven),
   `1` (an absolute criterion failed) or `2` (the run cannot support a
   verdict).

No component crosses a host boundary except the clients (to the nodes, over
the public transports) and the nemesis (to the configured remote targets, to
inject faults). The evidence bundle is local files on the machine that runs
the harness, and a check of it needs no cluster.

## Trust boundaries

- The harness authenticates to the cluster as an ordinary client with a
  credential scoped to the tenant and database it exercises. The soak needs
  no Security Officer or custodian role and never handles seal material.
- BOOT-01 is the exception, because it performs first boot:
  - It reads the Security Officer PIN, the user PIN and an optional barrier
    secret from files the inventory names.
  - It sends them only in the first-boot request.
  - It clears them after use, and never logs them or writes them out.
  - The inventory's client certificate becomes the first administrator.
- Container mode needs the right to run containers on each node. It
  reaches the runtime through the inventory's `docker` command, such as a
  remote shell, and it does so only on the nodes the inventory names.
- Unsealing a restarted node is the operator's command, configured per node
  in the targets file. The harness runs it and sees only its exit status; the
  passphrase or shares stay wherever that command reads them from, and none
  reaches the harness, the history or the bundle.
- The nemesis needs privileged access to the **target hosts** — to signal and
  restart the daemon, to install packet filters, to set the clock, to fill or
  throttle a disk. That access is configured per run and pointed only at the
  hosts named in the run's configuration. It is never exercised against the
  machine running the harness, and never from a unit test: unit tests drive
  the nemesis through a fake target and modify nothing on the host.
- The evidence bundle contains keys and values the workload generated and the
  node configuration only as a digest, never credentials, and is safe to
  publish.

## Rejected alternatives

| Alternative | Why rejected |
|---|---|
| Rely on the existing integration suites | they assert the result of one operation on a healthy cluster; none records a concurrent history or checks it for linearizability, so none can fail on the properties the gate is about |
| Count errors during a load test | an error count says nothing about a write that returned `ok` and was later lost, or a read that returned a stale value — the two failures that matter |
| Check linearizability inside the server | the server cannot be its own witness; the history must be recorded by the clients that observed it |
| A long-running test inside `make integration-test` | a soak takes hours and needs real fault injection on real hosts; the CI suite carries the short SIGKILL and partition tests instead, and the soak runs on demand |
| Declare the failover, recovery and soak bounds up front | a number chosen before it is measured is either too loose to mean anything or too tight to pass; it is measured first and ratified second |

## Decisions

Each decision sits behind a seam, so a
later change is a configuration or a single implementation, not a redesign.

| Decision | Decided | Seam |
|---|---|---|
| Linearizability checker | **porcupine**, as a test-only dependency of the `test/clusterproof` module and of nothing else | `history.Checker`: the soak runs every checker in `Options.Checkers`, and `cmd/clusterproof` adds the porcupine check through `Env.Configure`. The history format is independent of it |
| Binary or container on the nodes | **the `qrdbd` container**: each node runs the image built from the repository's release `Dockerfile`, the same image a hosted deployment runs | the inventory names the image and the nodes, and container mode brings them up ([Bring-up: BOOT-01](#bring-up-boot-01)); the targets file's `exec`, `signal` and `restart` templates address the node's container, so every fault is a target operation, not a hard-coded command |
| Seal types | **every seal type the release candidate ships**: the gate is run once per seal type | `PROOF_SEAL`: a run names the seal type it proves, and the run record and verdict carry it ([Seal types](#seal-types)); the cluster under test is configured outside the harness |
| Soak length | **24 h** for every gate run, and **one 72 h** soak before the release-candidate tag | `PROOF_DURATION` |
| Lease reads | `raft.lease_read` **stays the default (`true`)**, and the gate runs with it on. A problem the proof finds in lease reads is fixed, not avoided by turning them off; the rate nemesis (`pause` on any deployment, `clock-rate` through `adjtimex(2)` on a kernel per replica) and the in-CI lease probe are what exercise them, in bound and out of it ([engineering.md](engineering.md#the-rate-nemesis)) | the cluster's configuration; the operator declares the read mode in the targets file and the bundle records it with the node's configuration digest |

## What this feature does not do

- It does not test a single-node or standalone deployment; those have no
  consensus to prove.
- It does not replace the per-feature integration suites, and it is not a
  performance benchmark: E7 is measured under faults, not tuned for.
- It does not ship in any binary, and adds no server endpoint, CLI verb or
  configuration key.
- It does not prove anything about a run it did not record. A green gate is
  the evidence bundle of a specific run, not a property of the code.
