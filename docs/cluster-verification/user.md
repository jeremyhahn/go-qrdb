# Cluster Verification — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-14
> Conflicts with this document are defects. Fix them here first. The build is
> in progress: this folder is the requirement it builds against.
>
> Updated 2026-10-14: the list of ports `qrdbd` listens on moved here from
> the clustering guide, as the pinned port set a test now checks. Corrected
> in the move: the trust-anchor (63006) and CSR-join (63007) listeners run
> only when qrdbd manages the cluster CA, never on operator-supplied TLS; the
> list now names the multicast discovery socket and the Unix socket, and says
> that ports are picked free when no config file is given.
>
> Corrected 2026-10-12, when the soak landed: a run needs a targets file to
> inject faults; the bundle holds no node logs or metrics, and an
> indeterminate outcome is `indeterminate`, not `info`; a cluster that does
> not recover is a violation (exit `1`), and exit `2` means the run cannot
> support a verdict.
>
> Corrected 2026-10-12, when the replica-state read landed in clustering: the
> third criterion is now decided, so a clean run can pass; the soak's
> credential needs `admin:replica_state`.
>
> Updated 2026-10-12 for container mode: the harness can bring the cluster
> up from the release image and check it as BOOT-01. The owner's decisions on
> the deployment form, the seal types and the read mode replace the open
> questions this guide listed.
>
> Corrected 2026-10-12, when the seal types were ruled: a run names its seal
> type in `PROOF_SEAL`, and a sealed type needs an `unseal` command in the
> targets file.

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

How to run the cluster proof against your own three-node cluster and how to
read what it tells you.

## What a passing run means

On the run you recorded, with the faults you scheduled:

- no write the cluster acknowledged was lost;
- every read and write fits a single serial order (linearizability);
- all replicas ended in the identical state;
- every shard recovered and accepted writes after the last fault.

It also measures how long failover and recovery took and how fast the cluster
ran. Those figures have no pass mark yet; they will once the project has
measured and ratified them.

All four are fully checked: a lost write, a read or write that fits no serial
order, replicas that differ at one applied index, or a cluster that does not
recover exits `1`. The third is read from every node after the workload ends
([../clustering/user.md](../clustering/user.md#watching-the-cluster) shows
the same read by hand); if any node cannot answer it, or a shard's replicas
had not caught up, the run exits `2` rather than passing.

## Prerequisites

- Three nodes, `node-1`, `node-2` and `node-3`, running `qrdbd` in Raft mode
  as one cluster ([../clustering/user.md](../clustering/user.md)).
- A client credential with read and write access to one database used only
  for the proof. The workload writes and overwrites keys freely. Give it to
  the harness in a workload file (`PROOF_WORKLOAD`). The same credential reads
  every node's replica states, so it must be a `system`-tenant identity that
  also holds `admin:replica_state`; grant that through a custom role rather
  than using an Owner credential ([api.md](api.md#permissions)).
- A targets file (`PROOF_TARGETS`) saying how to reach each node: a command
  that runs a fault command in the node's namespaces, one that signals its
  server, and one that starts it again
  ([engineering.md](engineering.md#the-targets-file)). The harness never
  injects a fault on the machine it runs on.
- A privileged fault container on each node for the partitions, the clock
  rate and the disk faults.
- **The clock-rate fault needs each node on its own kernel (a host or a VM)
  with time synchronisation turned off.** The fault changes how fast every
  clock on the kernel runs. Nodes that share a kernel speed up or slow down
  together and prove nothing, and a time-sync daemon would steer the rate
  back. The fault runs `clusterproof clock-rate` on the node, so the fault
  container needs that binary and `CAP_SYS_TIME`. It refuses a node without
  the capability, and a node on the harness's own kernel, before it changes
  anything ([cli.md](cli.md#clusterproof-clock-rate)). The harness has no
  fault that steps the wall clock: the Raft lease never reads it
  ([engineering.md](engineering.md#clock-faults-and-the-raft-lease)).
- **The clock nemesis perturbs rate, in two forms.** `pause` stops a node
  for a bounded time and runs anywhere. `clock-rate` slows or speeds one
  node's kernel and needs the setup above. Keep a pause under the follower's
  window (1.6 s with the default Raft timing) and a rate within 1% to test
  the lease in bound. Longer pauses and larger rates test the fallback to a
  heartbeat round
  ([engineering.md](engineering.md#the-rate-nemesis)).
- **Rehearse each fault by hand first.** The default fault commands have
  not yet been run against real nodes.
- The seal type the nodes run: `none`, `passphrase` or `shamir`
  ([architecture.md](architecture.md#seal-types)), given as `PROOF_SEAL`.
  Run the proof once per seal type you rely on; a run proves only its own.
- Under `passphrase` or `shamir`, an `unseal` command in the targets file
  that unseals one node with its own passphrase or shares. The `kill` fault
  restarts a node sealed and runs it before the node serves again. Keep the
  passphrase or shares on the node side of that command: they must not be in
  the targets file, and the harness never sees them.
- The nodes run the `qrdbd` container built from the release `Dockerfile`.
  The harness can bring them up itself ([Bring up the cluster](#bring-up-the-cluster-boot-01)).
- A gate run is made once for each seal type the release candidate ships,
  with `raft.lease_read` at its default, `true`. Give the seal type as
  `PROOF_SEAL` and declare the read mode in the targets file; the bundle
  records both. Leave lease reads on: a run with them off proves nothing
  about them.

## Pinned port set

This is the one authoritative list of what `qrdbd start` listens on. Every
listener binds `server.host` (`--host`, default `0.0.0.0`) unless the table
names its own address setting. In the table, a flag set on the command line
wins over its YAML key.

| Port (default) | Protocol | Purpose | Flag / YAML key | Build tag | On by default | Who must reach it |
|---|---|---|---|---|---|---|
| 63001 | UDP (QUIC) | Raft transport | `--listen` / `raft.listen_address`, else `raft.raft_address`; advertised as `--raft-address` | — | yes, in raft mode | other voters |
| 63002 | TCP (HTTPS) **and** UDP (HTTP/3) | REST API, unseal and ceremony, cluster key delivery, `/metrics`, `/health` | `--admin-port` / `server.admin_port` | — | yes (`--enable-rest` / `protocols.rest`) | clients, other voters, joiners using SPKI/Direct, monitoring |
| 63003 | TCP (gRPC over TLS) | gRPC API | `--grpc-port` / `server.grpc_port` | — | yes (`--enable-grpc` / `protocols.grpc`) | clients, other voters |
| 63004 | UDP (native QUIC) | native QUIC API | `--quic-port` / `server.quic_port` | — | no (`--enable-quic` / `protocols.quic`) | clients, other voters when enabled |
| 63005 | TCP (MCP over HTTPS) | MCP server | `--mcp-port` / `mcp.port` | — | no (`--enable-mcp` / `protocols.mcp`) | MCP clients |
| 63006 | TCP (Noise NK) | trust anchor: hands joiners the cluster CA | `--bootstrap-noise-port` / `bootstrap.noise_port`; host `--bootstrap-noise-host` / `bootstrap.noise_host` | — | yes, on every admitted, unsealed member **when qrdbd manages TLS**; never with `--tls-*-file`, never in FIPS 140-3 mode | joiners fetching the cluster CA |
| 63007 | TCP (Noise_NK) | CSR join: signs a joiner's certificate | `--cluster-join-listen-port` / `cluster_join.listen_port`; host `--cluster-join-listen-host` / `cluster_join.listen_host` | — | no (`--cluster-join-enabled` / `cluster_join.enabled`); needs a cluster-managed CA, so never with `--tls-*-file` | joiners submitting a CSR |
| 3306 | TCP (MySQL wire) | SQL | `--mysql-port` / `server.mysql_port` | `qrdb_mysql` | no (`--enable-mysql` / `protocols.mysql`) | SQL clients |
| 5432 | TCP (PostgreSQL wire) | SQL | `--pgsql-port` / `server.pgsql_port` | `qrdb_pgsql` | no (`--enable-pgsql` / `protocols.postgresql`) | SQL clients |
| none; `qrdb database create --etcd` uses `localhost:2379` | TCP (etcd gRPC) | etcd API of one database | `databases[].etcd.listen_addr` (host and port), required when enabled | `qrdb_etcd` | no | etcd clients |
| 63777 | UDP (multicast) | peer discovery; binds the group address, not `server.host` | `--multicast-group` / `discovery.multicast_group` (default `239.255.77.77:63777`) | — | no (`--discovery multicast`) | other voters on the same segment |
| a file, not a port | Unix stream socket | local API | `--unix-socket-path` / `unix.socket_path` (default `/var/run/qrdbd/qrdbd.sock`) | — | no (`--enable-unix` / `protocols.unix`) | local processes with file access |

The `qrdb_mysql`, `qrdb_pgsql` and `qrdb_etcd` tags are all in the default
build. A binary built without one has no code for that listener; without
`qrdb_mysql` or `qrdb_pgsql` it has no flags for it either.

With no `--config` file, `qrdbd start` picks free ports for Raft, REST, gRPC
and (when enabled) QUIC instead of using the defaults above, and binds Raft to
`0.0.0.0`. A cluster pins its ports by setting them, either in its config file
or with the flags.

What the table implies for a firewall:

- **Port 63002 needs both TCP and UDP.** The REST server always runs TLS and
  binds an HTTP/3 listener on the same number beside it.
- **Every member serves 63006, not only the first, when qrdbd manages TLS.**
  The node that generated the cluster CA advertises the anchor from first boot;
  every other member starts the same listener once it has joined, and again on
  every restart from its data directory. Open it on every voter a joiner may be
  pointed at. A cluster run on certificates you supply (`--tls-ca-file`,
  `--tls-cert-file`, `--tls-key-file`) has no cluster CA to hand out, so it
  never binds 63006 or 63007, even when they are configured.
- **Voters talk to each other on the API ports as well as on Raft.** Cluster
  key delivery (`/v1/cluster/keydelivery`) runs on the REST port, and replica
  forwarding goes voter to voter on the API ports. A voter-to-voter rule
  covering only 63001 leaves a cluster that elects a leader but cannot deliver
  keys or forward requests.
- **A sealed node binds no port outside this set.** While sealed, `qrdbd`
  serves the unseal and ceremony surface on the same public `--host` and the
  same REST and gRPC ports; there is no separate loopback-only sealed listener.
- **Outbound sockets are not listeners.** A node also holds UDP sockets on
  ports from the kernel's ephemeral range for the connections it opens. They
  need no inbound rule.

The table is kept true by a test: a three-node cluster with every port set
must listen on exactly these sockets and no others
([engineering.md](engineering.md#test-strategy)).

## Bring up the cluster: BOOT-01

`make cluster-proof-boot` starts the three nodes from the release image. It
takes each node through first boot and checks that the nodes formed one
Raft cluster, on every transport. Prepare the following on each node's host:

- the image, pulled or loaded;
- a TLS directory holding `ca.pem`, `node.pem` and `node-key.pem`, where
  the node certificate names the node's address;
- an empty data directory.

On the machine that runs the harness, prepare:

- an administrator client credential signed by the same CA;
- files holding the Security Officer PIN and the user PIN;
- a file holding the barrier secret, if the seal type takes one;
- an inventory naming all of it
  ([engineering.md](engineering.md#the-inventory)).

```sh
make cluster-proof-boot PROOF_INVENTORY=$XDG_CONFIG_HOME/clusterproof/inventory.json
```

A pass prints one line per node and transport, all naming the same leader
and mode `raft`. The containers are removed at the end, so start the soak's
cluster the same way. The containers are named `clusterproof-<name>`, and
that is the name the targets file's templates use.

To rehearse on one machine:

1. Create a user-defined network, such as
   `docker network create --subnet 192.0.2.0/24 proof`.
2. Set `"network": "proof"` in the inventory.
3. Give each node an IP on that subnet as its address.

The harness reaches each container at that IP.

## First run: a short smoke

```sh
make cluster-proof \
  PROOF_NODES=node-1=db1.example.com:7443,node-2=db2.example.com:7443,node-3=db3.example.com:7443 \
  PROOF_TARGETS=$XDG_CONFIG_HOME/clusterproof/targets.json \
  PROOF_WORKLOAD=$XDG_CONFIG_HOME/clusterproof/workload.json \
  PROOF_SEAL=passphrase \
  PROOF_DURATION=15m \
  PROOF_NEMESIS=kill
```

Every 10 seconds a progress line shows the elapsed time, operations by
outcome and the faults injected so far. At the end the harness prints the
outcome and the seal type it ran under, why it is not proven, each criterion, the bundle's path and its
manifest digest.

## Common tasks

**Run the full fault set.** Leave `PROOF_NEMESIS` unset.

**Measure a baseline.** `PROOF_NEMESIS=none` runs the same workload with no
faults, for throughput and latency to compare against. It always exits `2`:
it proves nothing about a fault.

**Replay a run.** Set `PROOF_SEED` to the `seed` and `PROOF_SEAL` to the
`seal` in its `run.json`, with the same nodes, duration, faults and targets:
the fault schedule is the same.

**Cover every seal type.** Run once with each `PROOF_SEAL` against a cluster
configured for it. A `shamir` cluster of three nodes cannot be initialized
yet (see [architecture.md](architecture.md#seal-types)).

**Run the gating soak.** Set `PROOF_DURATION=24h` for each gate run, once
per seal type the release ships, with lease reads on (the default). Before a
release candidate is tagged, one run is `PROOF_DURATION=72h`.

**Verify someone else's result.** Copy their evidence bundle and run
`make cluster-proof-check PROOF_BUNDLE=<bundle-dir>`. It checks every file
against the manifest and derives the verdict again from the evidence,
without needing a cluster.

## Reading the evidence bundle

- `verdict.json` — the outcome, the reasons it is not proven, and each
  criterion with its status and detail; for a ledger failure, the keys and
  values that broke it. Start here.
- `run.json` — what was run (nodes, duration, faults, seed, seal type,
  schedule, workload) and what the run saw outside the history: unhealed faults, its
  own errors, the recovery and replica observations.
- `history.jsonl` — every operation each client called and how it returned.
  `indeterminate` means the client could not tell whether the operation was
  applied; it is never counted as a loss.
- `nemesis.jsonl` and `timeline.jsonl` — every inject and heal, alone and
  merged with the operations on the run's clock.
- `timings.json` — throughput and latency overall, outside and inside fault
  windows, and per window the first acknowledged write after the fault and
  after the heal.
- `nodes/<name>/node.json` — each node's version and the SHA-256 of its
  configuration, when the targets file says how to read them. Node logs and
  metrics are not collected; keep the nodes' own.

## Troubleshooting

| Symptom | Likely cause | What to do |
|---|---|---|
| exit `1`, E4 fails | a node did not come back within the recovery bound, a shard has no leader, or a node refused the probe write | check that node's own log; confirm it restarts by hand; keep the bundle |
| exit `2`, "faults never healed" | the harness lost access to a node mid-fault | heal the fault by hand (the reason names it), restore access, rerun |
| exit `2` before the run, `PROOF_SEAL` | no seal type given, or one the release does not ship | set `PROOF_SEAL` to `none`, `passphrase` or `shamir` |
| exit `2` before the run, `PROOF_TARGETS unseal` | `kill` under a sealed type with no `unseal` command | add the `unseal` template to the targets file |
| exit `2`, "a fault did not heal: kill" under a sealed type | the `unseal` command failed, so the node stayed sealed | run the command by hand on that node; check its credential |
| exit `2`, "no fault was injected" | `PROOF_NEMESIS=none`, or a run too short for its first window | expected for a baseline; otherwise lengthen the run |
| exit `2`, E3 unavailable, "cannot read replica states" | a node was unreachable, or refused the read — most often the credential lacks `admin:replica_state` or is not a `system`-tenant identity | the reason names the node and its refusal; fix the credential's role and rerun ([api.md](api.md#permissions)) |
| exit `2`, E3 unavailable, "did not quiesce" | a shard's replicas were at different applied indexes when read | check the lagging node's log; a node still catching up after the last fault needs a longer `recovery_timeout` |
| exit `1`, E3 fails, "diverges at applied index" | two replicas of a shard hold different state at one index | keep the bundle; read `qrdb admin replica-states` on each node by hand on a quiet cluster to confirm, then report it |
| many `indeterminate` outcomes, few `ok` | client timeouts shorter than failover | lengthen `op_timeout` in the workload file; `indeterminate` is safe but uninformative |
| E1 fails | a real defect | keep the whole bundle and report it with `verdict.json`; do not rerun over it |
| E2 fails | a real defect: a stale read, a lost or reordered write, or a swap that saw the wrong value; the anomaly names the key, the value it held where no serial order could go on, and the operations that could have come next | as for E1; start reading `timeline.jsonl` at those operations |
| exit `2`, E2 unavailable, "undecided after" | a key's history was too contended for the checker to decide in its ten minutes | widen the key space (`keys`) or shorten the run so each key holds fewer concurrent operations, and rerun |
| `cluster-proof-boot`: "PROOF_INVENTORY must name a container inventory" | no inventory given | set `PROOF_INVENTORY`; the test fails rather than skipping |
| `cluster-proof-boot`: `exec: "docker": executable file not found` | no container runtime where the `docker` command runs | install one, or point the inventory's `docker` at the nodes through a remote shell |
| `cluster-proof-boot`: identity step fails | the image is missing on that node, or `tls_dir` holds no `node.pem` | load the image; check the TLS directory |
| `cluster-proof-boot`: first boot times out | the node did not start, or its certificate does not name its address | read the container log the failure prints |
| `cluster-proof-boot`: `peers` or `status` check fails | the nodes did not form one cluster: a data directory held an earlier cluster, or the peers cannot reach each other | empty every data directory; check the addresses and the network |
