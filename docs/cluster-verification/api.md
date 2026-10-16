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

# Cluster Verification — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-12
> Conflicts with this document are defects. Fix them here first. The build is
> in progress: this folder is the requirement it builds against.
>
> Corrected 2026-10-12, when the soak landed: `cas` is the KV guarded
> compare-and-swap, not a transaction; the harness does not call Health —
> readiness after a fault is the E4 probe write — and E3 had no replica
> state source, so it reported unavailable.
>
> Corrected 2026-10-12, when the replica-state read landed in clustering: E3
> reads it from every node, so the soak's credential now needs
> `admin:replica_state`; the section below says how.
>
> Corrected 2026-10-12, when the seal types were ruled: the harness calls no
> barrier endpoint; unsealing a restarted node is the operator's command.

**No API surface.** This feature adds no endpoint, RPC, QUIC method, MCP tool,
Unix socket method or embedded call. It is a test harness that *consumes* the
existing surface as an ordinary client.

## What the harness calls

| Capability used | Documented in | Used for |
|---|---|---|
| KV put, get, delete | [../key-value/api.md](../key-value/api.md) | the workload's `put`, `get` and `delete`, the final read, and E4's probe write |
| KV compare-and-swap (`GuardedKVClient.CompareAndSwap`) | [../key-value/api.md](../key-value/api.md) | the workload's `cas` |
| Cluster status and peers | [../clustering/api.md](../clustering/api.md) | the leader `partition-isolate-leader` cuts off, and E4's check that every shard has a leader |
| Replica states | [../clustering/api.md](../clustering/api.md#replica-states) | E3's per-replica applied index and state-machine hash, read from every node |
| Barrier status and the first-boot ceremony | [../barrier-seal/api.md](../barrier-seal/api.md), [../firstboot/api.md](../firstboot/api.md) | BOOT-01's first boot, and its check that a node is already through it |
| Admin health, status, peers and topology | [../clustering/api.md](../clustering/api.md) | BOOT-01's checks on every node |
| Capabilities | [../capabilities/api.md](../capabilities/api.md) | BOOT-01's check that every node reports mode `raft` |

Transports exercised: REST, gRPC and QUIC — the transports a remote client
reaches a node over. A soak uses one per run; BOOT-01 checks all three. MCP,
Unix and embedded are not in the proof: MCP and Unix route to the same
service layer as gRPC, and embedded has no network path for a fault to act
on.

Outside the client surface, container mode also runs `qrdbd node-identity`
from the image to read each node's member identity
([../clustering/cli.md](../clustering/cli.md)).

## Permissions

The soak runs as a client with read and write permission on the one
database it exercises, read access to cluster status, and
`admin:replica_state` for E3. That permission is cross-scoped, so the
credential is a `system`-tenant identity, and no built-in role holds it but
Owner: grant it through a custom role rather than running the soak as Owner.
Without it, every node refuses the read and E3 reports `unavailable` with the
refusal as its reason; it never passes. The soak needs no Security Officer or
custodian permission.

BOOT-01 performs first boot, so it holds the Security Officer and user PINs
for the ceremony. Its client certificate is enrolled as the first
administrator, and that is the credential it uses for the admin reads.

The soak calls no barrier or seal endpoint. Under a sealed type a
restarted node is unsealed by the operator's `unseal` command from the
targets file, which uses the node's own credential through
[../barrier-seal/api.md](../barrier-seal/api.md); the harness sees only
whether it succeeded. The in-CI SIGKILL test, which spawns its own nodes and
holds their passphrases, reads the barrier status of each restarted node and
unseals it through the same API.

## Replica state comparison

E3 compares the state-machine hash every replica of a shard reports at the
same applied index. It reads them through the clustering capability built for
it, [replica states](../clustering/api.md#replica-states), over the run's
transport: after the workload, the final read and E4's probe write have
finished, it asks **every** node for its own replicas, since a node answers
for no other. A follower still applying the last entries trails the leader,
so while a shard's replicas stand at different applied indexes it asks again,
until they meet or `recovery_timeout` passes; the last answer is the one
judged.

| What every node answered | E3 |
|---|---|
| Replicas of each shard at one applied index report one hash | `pass` |
| Two replicas of a shard at one applied index report different hashes | `fail`, naming the shard, the index and both nodes |
| A shard's replicas are still at different applied indexes when `recovery_timeout` passes | `unavailable`: the cluster had not quiesced, so the reading proves nothing |
| A node could not be reached, or refused the read | `unavailable`, naming the node and its refusal |
| No node reported a replica | `unavailable` |

A node that does not answer never shrinks the comparison to the nodes that
did: two agreeing replicas out of three prove nothing about the third.

The read pairs the index and the hash without one exclusion held across both
(see [../clustering/api.md](../clustering/api.md#replica-states)). The error
is one-sided — it can make agreeing replicas look different, never different
ones look the same — so it can cost a run its `pass` but cannot forge one.
Reading only after the workload has stopped is what keeps it from firing.

The etcd-compatible `Hash` and `HashKV` calls are not used and still answer
`Unimplemented`: they hash etcd's revisioned key space, not a replica's state
machine.
