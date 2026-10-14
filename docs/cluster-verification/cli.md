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

# Cluster Verification — CLI

> **Status: DRAFT — requirements for new work; build in progress.** · Scope:
> go-qrdb · Last verified: 2026-10-13
>
> Corrected 2026-10-12, when the soak landed: the fault names are the ones
> the code registers (`partition-isolate-leader`,
> `partition-majority-minority`, `disk-full`, `slow-disk`; there is no `stop`
> or `restart-all`), the default duration is 5 minutes, and the exit-code
> table now says which outcomes are `1` and which are `2`. Three variables
> the draft did not name — `PROOF_WORKLOAD`, `PROOF_TARGETS`, `PROOF_SEED` —
> carry what a run cannot do without.
>
> Updated 2026-10-12 for container mode: `make cluster-proof-boot` and
> `PROOF_INVENTORY`.
>
> Corrected 2026-10-12, when the seal types were ruled: `PROOF_SEAL` is
> required, the outcome line names the seal type, and the in-CI SIGKILL test
> runs once per seal type.
>
> Updated 2026-10-13 for the rate nemesis: `clusterproof clock-rate`, the
> command the `clock-rate` fault runs on a node.

**No `qrdb` or `qrdbd` verb belongs to this feature.** The harness is a
client of the existing surface and adds nothing to it. Its command-line entry
points are the make targets below. Two of them build and run
`bin/clusterproof run` and `bin/clusterproof check`, and `cluster-proof-boot`
runs the BOOT-01 test. The same binary's `clock-rate` subcommand is what the
`clock-rate` fault runs on a node.

## `make cluster-proof-boot`

Brings up a three-node cluster from the release image on the inventory's
nodes. It takes every node through first boot, then checks BOOT-01 over
QUIC, gRPC and REST
([architecture.md](architecture.md#bring-up-boot-01)). It removes the
containers when it ends. It runs `TestBOOT01` under the tags
`codec_json,raft,clusterproof,clusterproof_container`.

| Variable | Type | Default | Required | Meaning |
|---|---|---|---|---|
| `PROOF_INVENTORY` | path to a JSON file | — | yes | the image, the container runtime command, the network, the admin credential, the first-boot secrets and the nodes ([engineering.md](engineering.md#the-inventory)) |

Exit codes: `0` when BOOT-01 passed. Any other code is a failure, and that
includes a missing `PROOF_INVENTORY`. The target never skips.

Output: each node's replica ID, Raft address and member identity, then one
line per node and transport. For example:

```
node-1 over quic: health ok, leader 2 (self false), raft 192.0.2.1:63001, system shard members 1,2,3, shards 1, mode raft
```

On a failure, the output gives the failing check and its reason, followed by
the last 200 lines of every container's log.

```sh
make cluster-proof-boot PROOF_INVENTORY=$XDG_CONFIG_HOME/clusterproof/inventory.json
```

## `make cluster-proof`

Runs the soak against a running cluster: the workload and the nemesis for the
run's duration, every fault healed, the recovery and replica checks, the
final read, and the evidence bundle. Every variable may be given on the make
command line or in the environment.

| Variable | Type | Default | Required | Meaning |
|---|---|---|---|---|
| `PROOF_NODES` | comma-separated `name=address` | — | yes | the nodes under test, in order; a name is letters, digits, `.`, `_` and `-`, at most 64 characters |
| `PROOF_DURATION` | Go duration (`30m`, `24h`) | `5m`, a smoke | no | how long the workload and the nemesis run; a gate run is `24h`, once per shipped seal type, and one `72h` run precedes the release-candidate tag; the gating length is always given explicitly |
| `PROOF_NEMESIS` | comma-separated fault names, or `none` | every fault | no | the faults the schedule draws from; names are listed in [engineering.md](engineering.md#the-nemesis) |
| `PROOF_BUNDLE` | path | `./clusterproof-out/<UTC start>` | no | the evidence bundle directory; it must not exist yet |
| `PROOF_TARGETS` | path to a JSON file | — | when a fault is configured | how to reach each node to inject faults, and every fault's parameters ([engineering.md](engineering.md#the-targets-file)) |
| `PROOF_WORKLOAD` | path to a JSON file | the workload defaults over QUIC | no | the workload: transport, credentials, clients, keys, mix, timeouts ([engineering.md](engineering.md#the-workload)) |
| `PROOF_SEED` | positive integer | drawn from the system's random source | no | seeds the workload and the fault schedule; the bundle records it, so a run can be replayed with the same schedule |
| `PROOF_SEAL` | `none`, `passphrase` or `shamir` | — | yes | the node barrier seal type the cluster under test runs ([architecture.md](architecture.md#seal-types)); recorded in the bundle. Under `passphrase` or `shamir`, `kill` needs the targets file's `unseal` command |

Exit codes:

| Code | Meaning |
|---|---|
| `0` | **proven**: the run injected its faults, every fault healed, and E1–E4 all passed |
| `1` | **violated**: at least one of E1–E4 failed — a lost or phantom write, a linearizability violation, diverging replicas, or a cluster that did not recover within the recovery bound; `verdict.json` names it |
| `2` | **invalid**: the configuration was refused (an unset or unknown `PROOF_SEAL` included); or the run cannot support a verdict — it was interrupted, a fault failed to inject or never healed, no fault was injected (`PROOF_NEMESIS=none` included), the history recorded errors, or one of E1–E4 could not be decided |

A violation outranks invalidity: a run that shows a lost write exits `1`
even if it was also interrupted. Make itself exits `2` whenever the runner
fails; the runner's own code is on make's `Error N` line and in
`verdict.json`.

Output: one progress line per 10 seconds on standard error (elapsed time,
operations by outcome, faults injected so far); then on standard output the
outcome and the seal type it was reached under, the reasons a run is not proven, one line per criterion E1–E7, the
bundle's path and the SHA-256 of its `manifest.json`.

```sh
make cluster-proof \
  PROOF_NODES=node-1=db1.example.com:7443,node-2=db2.example.com:7443,node-3=db3.example.com:7443 \
  PROOF_TARGETS=$XDG_CONFIG_HOME/clusterproof/targets.json \
  PROOF_SEAL=passphrase \
  PROOF_DURATION=2h \
  PROOF_NEMESIS=kill,partition-isolate-leader,partition-majority-minority
```

A run with the nemesis off, to measure the baseline for E7. It exits `2` by
design — it proves nothing about a fault — and needs no targets file:

```sh
make cluster-proof PROOF_NODES=... PROOF_SEAL=none PROOF_DURATION=1h PROOF_NEMESIS=none
```

## `make cluster-proof-check`

Verifies an existing evidence bundle without a cluster: every file against
its SHA-256 in `manifest.json`, no file missing and none added, then the
verdict, the timeline and the timings derived again from the recorded
evidence and compared byte for byte with the bundle's. It is how a reader
verifies a published bundle.

| Variable | Type | Default | Required | Meaning |
|---|---|---|---|---|
| `PROOF_BUNDLE` | path | the newest directory under `./clusterproof-out/` | no | the evidence bundle directory to check |

Exit codes: `2` when the bundle does not verify — a checksum mismatch, a
missing, added or non-regular file, an unknown format, or a derived file that
does not follow from the evidence — a run record whose seal type was edited
included. Otherwise the code of the run's verdict:
`0`, `1` or `2`, as for `cluster-proof`.

```sh
make cluster-proof-check PROOF_BUNDLE=./clusterproof-out/2026-10-12T00-00-00Z
```

## `clusterproof clock-rate`

Checks whether a node's kernel can take the `clock-rate` fault, or sets that
kernel's clock rate. The fault runs it on the node through the targets
file's `exec` template ([engineering.md](engineering.md#the-rate-nemesis)).
An operator runs it by hand to rehearse the fault. It is not built for, and
refuses, the machine the harness runs on.

```
clusterproof clock-rate -not-kernel <boot-id> (-check | -ppm <n>)
```

| Flag | Type | Required | Meaning |
|---|---|---|---|
| `-not-kernel` | boot ID | yes | the harness's kernel, as `/proc/sys/kernel/random/boot_id` reads there; the command refuses a node whose boot ID is the same |
| `-check` | flag | one of `-check` and `-ppm` | check the refusals and change nothing |
| `-ppm` | integer within ±100000 | one of `-check` and `-ppm` | set the kernel's tick and frequency so its clocks run `n` parts per million fast (positive) or slow (negative); `0` restores the nominal tick and frequency |

Exit codes:

| Code | Meaning |
|---|---|
| `0` | the check passed, or the rate is set |
| `1` | the kernel call failed |
| `2` | a usage error: a flag missing or unknown, both or neither of `-check` and `-ppm`, or `ppm` out of range |
| `3` | refused: the process lacks `CAP_SYS_TIME` |
| `4` | refused: the node runs on the harness's kernel |

The harness's own kernel is checked first, so a node on that kernel exits
`4` whether or not it has the capability. Off Linux every call exits `1`.

Output: on a rate change, one line with the rate before and after it. For
example:

```
tick 10000 freq 0.000 ppm (0.000 ppm) -> tick 9950 freq 0.000 ppm (-5000.000 ppm)
```

```sh
clusterproof clock-rate -not-kernel "$HARNESS_BOOT_ID" -check
clusterproof clock-rate -not-kernel "$HARNESS_BOOT_ID" -ppm -5000
clusterproof clock-rate -not-kernel "$HARNESS_BOOT_ID" -ppm 0
```

## `make integration-test-cluster-verification`

Runs this feature's in-CI tests on a self-spawned three-node cluster, with
no Docker and no remote hosts: leader then follower SIGKILL under load,
leader partition, and lease reads through a leader that loses its followers.
It runs `TestClusterFault_SIGKILLUnderLoad`,
`TestClusterFault_PartitionIsolatesLeader` and
`TestClusterFault_LeaseReadAfterLeaderChange`.

The SIGKILL test runs once per seal type a three-node cluster can be
initialized under, as the subtests `none` and `passphrase`
([architecture.md](architecture.md#seal-types)). Under `passphrase` every
restarted node must report sealed and refuse data operations before the test
unseals it with that node's own passphrase. The partition test runs under
`passphrase`. To run one seal type:

```sh
go test -tags "$(make -s print-build-tags | tr ' ' ','),integration" \
  -run 'TestClusterFault_SIGKILLUnderLoad/passphrase' ./test/integration/
```

```sh
make integration-test-cluster-verification
```

## `make test-clusterproof`

The harness's own unit tests, under the `clusterproof` tag: fakes for the
cluster, the faults and the container runtime, and one smoke run against an
in-process standalone node. It never touches a remote cluster, the host or
a container runtime. BOOT-01 is not among them: its own tag,
`clusterproof_container`, keeps it to `make cluster-proof-boot`.

## Transport selection and server-less use

A run uses one transport, the workload file's `protocol`: `quic` (the
default), `grpc` or `rest`. To cover all three, run once per transport.
`cluster-proof-boot` checks all three in one run, and does first boot over
REST. `cluster-proof-check` needs no server at all.
