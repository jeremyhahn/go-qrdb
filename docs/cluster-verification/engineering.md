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

# Cluster Verification — Engineering

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-13
> Conflicts with this document are defects. Fix them here first. The build is
> in progress: this folder is the requirement it builds against.
>
> The design is [architecture.md](architecture.md), whose
> [Decisions](architecture.md#decisions) settle the checker, the
> container, the seal types, the soak length and the read mode.
>
> Corrected 2026-10-12, when the soak landed, to the code as built: the
> history is one record per operation (call and return in one line), not
> invoke/complete pairs, and carries no revision, node or read mode; the
> ledger is a check over the history, not a file of its own; nemesis events
> go to their own file and meet the history in `timeline.jsonl`; the fault
> names are the registered ones; and the bundle holds what the run can
> observe today — node logs and metrics are not collected yet.
>
> Updated 2026-10-12 for container mode:
> [Container mode and BOOT-01](#container-mode-and-boot-01) and
> [The inventory](#the-inventory).
>
> Corrected 2026-10-12, when the seal types were ruled: the run names its
> seal type in `PROOF_SEAL` instead of the targets file declaring it;
> `declared.seal_type` is gone; `kill` re-unseals through a new `unseal`
> template; the bundle format is `2`.

## Exported surface

The harness is test code in the `test/clusterproof/` module
(`github.com/jeremyhahn/go-qrdb/test/clusterproof`), compiled only with the
`clusterproof` build tag, and exports nothing to go-qrdb or to the SDK. The
module requires go-qrdb and its SDK through `replace` directives to the
working tree, so it always tests the tree it sits in, and it is the only
module that requires porcupine.

| Package | Contract |
|---|---|
| `test/clusterproof/workload` | `Config` (`LoadConfig`, `Normalize`, `Validate`), `Workload` (`New`, `Run`, `FinalRead`, `Stats`), `Dialer` / `NewTransportDialer` for the workload's connections and `Connector` / `NewTransportConnector` for the soak's admin reads, and the `Classifier` that maps a transport error to an outcome |
| `test/clusterproof/ledger` | `Checker`, the acknowledged-write ledger as a `history.Checker` |
| `test/clusterproof/linearizability` | `Checker`, the porcupine linearizability check (E2) as a `history.Checker`, with its per-key `Timeout` (`DefaultTimeout` when zero); `Name`; the anomaly kinds `KindNonLinearizable` and `KindVacuous`; and the errors for a history it cannot judge, `TimeoutError` and `AmbiguousCASError` |
| `test/clusterproof/history` | `Op`, `History`, `Recorder`, `Encode` / `Decode`, and the `Checker` interface every check implements |
| `test/clusterproof/nemesis` | `Nemesis`, `Target`, `Restarter` and `Unsealer` (`RemoteTarget`, `LocalProcess`), the faults (`Kill`, and `KillSealed` for a sealed type), `Schedule` (`FixedSchedule`, `RandomSchedule`) and the `Player` that plays a schedule and records an `Event` per step |
| `test/clusterproof/soak` | `LoadOptions`, `Run`, `Check`, the entry points `Main` and `CheckMain`, and the seams `Cluster`, `Inspector` and `ReplicaStates` |
| `test/clusterproof/soak/cmd/clusterproof` | the `run` / `check` binary the make targets build |
| `test/clusterproof/container` | `Inventory` (`LoadInventory`, `ParseInventory`, `Validate`), `Cluster` (`NewCluster`, `Identities`, `Start`, `Logs`, `Remove`) over a `nemesis.Runner`, `Boot` (`FirstBoot`, `Verify`) with its `Dialer` / `NewDialer`, and the `Observation` each BOOT-01 check records |

The `clusterproof` tag set is declared in the Makefile's `SUPPORTED_TAG_SETS`,
and `make vet-tags` typechecks the module under every declared set that names
the tag on every CI run; a declaration with no such set fails the sweep
rather than vetting nothing. The BOOT-01 test itself is behind a second tag,
`clusterproof_container`: it needs a container runtime and a cluster that
no unit run has. Its own set, `codec_json,raft,clusterproof,clusterproof_container`,
is declared beside the first, so the test is typechecked on every CI run and
runs only under `make cluster-proof-boot`.

## The workload

- **Operations.** `put`, `get`, `cas` (compare-and-swap on the current value)
  and `delete`, over a key space small enough that operations contend on the
  same keys. Contention is what makes the history informative: a history with
  no two concurrent operations on one key is trivially linearizable.
- **Unique values.** Every written value is unique for the whole run
  (`c<client>-<sequence>`), so a read identifies exactly which write it
  observed.
- **Clients.** Each client issues one operation at a time on its own
  connection and waits for its completion before the next; concurrency comes
  from running many clients. A client keeps its identifier through an
  indeterminate outcome. A client whose connection drops dials again, moving
  to the next node when its own does not answer.
- **Placement and transport.** Clients are spread across the nodes in
  `PROOF_NODES` order, so leader forwarding is inside the proof. A run uses
  one transport, `quic`, `grpc` or `rest`; covering all three is three runs.
- **Final read.** After the load phase and the recovery check the workload
  reads every key once more. Those reads are in the history under client
  number `clients`, one past the last load client, and are outside E7.
- **Read mode.** The history does not record a read's consistency level;
  the operator declares the cluster's read mode in the targets file and the
  bundle records it. `raft.lease_read` stays at its default, `true`, and the
  gate runs with it on.

`PROOF_WORKLOAD` names a JSON file; without one the defaults apply over QUIC
with no client credential.

| Field | Default | Meaning |
|---|---|---|
| `endpoints` | the `PROOF_NODES` addresses | must equal the `PROOF_NODES` addresses, in order |
| `protocol` | `quic` | `quic`, `grpc` or `rest` |
| `credentials` | none | `ca_file`, `cert_file`, `key_file`; never written to the bundle |
| `clients` | `8` | concurrent clients |
| `keys` | `16` | size of the key space |
| `key_prefix` | the workload's | the namespace the run writes under; E4's probe writes go under `<key_prefix>/probe/` |
| `mix` | `put 4, get 3, delete 1, cas 2` | relative weights |
| `seed` | `PROOF_SEED` | overridden by the run's seed |
| `op_timeout` | `5s` | an operation still unanswered then is indeterminate |
| `duration` | `PROOF_DURATION` | must be unset or equal `PROOF_DURATION` |
| `redial_backoff` | `250ms` | pause before a client redials |

## The ledger (E1)

The ledger reads the history and nothing else; there is no ledger file. Per
key, where "before" means one operation returned before the other was called
on the run's clock:

- a write is a `put`, a `delete`, or a `cas` that swapped; an indeterminate
  write may or may not have applied, at any time after its call;
- the final read is the last `ok` `get` called after every write to the key
  returned;
- **lost**: the final read matches no write still allowed to be the last;
- **phantom**: a read observed a value no operation wrote, a value whose only
  writer certainly did not apply, or a value whose writer had not yet been
  called;
- **duplicated**: a value read, then another state, then the first value
  again — a replayed write or a stale read;
- **unread** (a key written and never finally read) and **vacuous** (no write
  acknowledged at all) are not violations, but the ledger could not decide,
  so E1 is unavailable and the run is invalid.

## The linearizability check (E2)

E2 is decided by [porcupine](https://github.com/anishathalye/porcupine), a
test-only dependency, and reported as `linearizability-porcupine`. Like the
ledger it reads the history and nothing else. Linearizability is local — a
history is linearizable when every key's sub-history is — so each key is a
register that starts absent, searched on its own and in parallel, and the
results are reported in key order:

- a `put` sets the value and a `delete` makes the key absent;
- a `cas` swaps exactly when the key is present and holds `expected`, and its
  recorded `swapped` must agree; an absent key never swaps;
- an `ok` `get` must observe the register: `found` with that value, or not
  found when the key is absent.

The translation from the history:

- a `fail` operation certainly had no effect and is dropped;
- an `indeterminate` `get` told the client nothing and is dropped;
- an `indeterminate` write may have applied at any instant after its call, or
  never: it is given no return, so porcupine may place it anywhere from its
  call on, and placing it last is the same as its never applying. An
  indeterminate `cas` swaps wherever it is placed if the register matches
  there, as the store would have;
- an `ok` `cas` without a recorded `swapped` applied with a result the client
  never learned, and steps the register as the model says;
- a `cas` with no `expected` value is refused, and the check returns an
  error: the format cannot tell an empty comparand from an absent one, so
  the model could only guess what the store did.

A key with no linearization is a **non-linearizable** anomaly. It names the
register's value where the longest linearizable prefix stops (`absent` when
the key holds none), and the operations that could have come next, none of
which is consistent with it. Porcupine reports its partial linearizations in
no fixed order, so the longest is chosen with ties going to the
lexicographically least: the anomaly is the same on every run, and a check
re-derives it byte for byte. A history with no acknowledged operation is
**vacuous**, and E2 is unavailable.

The search is exponential in the worst case, so each key is bounded, by
default to ten minutes. A key the search cannot decide in that time makes E2
unavailable, never passed.

The check is strictly stronger than the ledger. A read of an overwritten
value, called after the overwrite was acknowledged, passes the ledger, which
judges only a key's final value and its phantoms, and fails E2: it is the
stale read a lease that outlived a leader change would serve. The ledger
still runs beside it, on every history including one too long for E2 to
decide. Every lost write is also a linearizability violation, but the ledger
names it as a lost, phantom or duplicated value, which the register's
evidence does not.

## The history format

The history (`history.jsonl`) is **JSON Lines**, one operation per line, in
call order.

| Field | Type | Meaning |
|---|---|---|
| `id` | integer | the operation's identifier, unique in the run |
| `client` | integer | the client that issued it; `clients` (one past the last) is the final read |
| `op` | string | `put`, `get`, `delete` or `cas` |
| `key` | string | the key |
| `input` | object | `value` (a `put`'s value or a `cas`'s new value) and `expected` (a `cas`'s comparand) |
| `output` | object | on an `ok` record only: `value` and `found` for a `get`, `swapped` for a `cas` |
| `call_ns` | integer | nanoseconds since the run started, on the harness's monotonic clock |
| `return_ns` | integer | the same clock; for an indeterminate operation, when the client stopped waiting |
| `outcome` | string | `ok`, `fail` or `indeterminate` |
| `error` | string | the transport error, on `fail` and `indeterminate` |

A reader refuses a history with a duplicate `id`, an unknown `op` or
`outcome`, an empty key, `return_ns` before `call_ns`, an `output` on a
non-`ok` record, an `ok` `get` without one, or a write without its `input`.

The format is independent of the checker: the linearizability check
translates it into porcupine's model, as described under
[The linearizability check](#the-linearizability-check-e2).

## The nemesis

Every fault implements the same contract: **Inject**, **Heal**, and a name.
Heal is idempotent, heals every target the last Inject touched or tried to
touch, and runs even after a failed Inject. A fault reaches a node only
through its **target**, which offers two primitives: run a command on the
node (`exec`) and signal its server process (`signal`), plus `restart` for
the kill fault and, under a sealed type, `unseal`. On a real cluster the
target is a `RemoteTarget`: command templates in the targets file. The proof's nodes run the `qrdbd`
container, so the templates address the node's container; which runtime and
how it is reached is configuration, not code.

| Fault | Targets | Effect | Heal |
|---|---|---|---|
| `kill` | a minority | SIGKILL the server process | `restart`; under `passphrase` or `shamir`, then `unseal` |
| `pause` | a minority | SIGSTOP, so it holds its connections and stops answering | SIGCONT; the process kept its keys, so no unseal |
| `partition-isolate-leader` | all (≥ 2 nodes) | drop traffic between the node leading at inject time and every other node | remove the filter |
| `partition-majority-minority` | all (≥ 3 nodes) | split the nodes into a minority and a majority | remove the filter |
| `partition-bridge` | all (≥ 3 nodes) | two halves joined by a middle node that sees both | remove the filter |
| `clock-rate` | one node | after `clusterproof clock-rate -check` passes on it, set the kernel's tick and frequency through `adjtimex(2)` so the node's clocks run `ppm` parts per million fast or slow | `clusterproof clock-rate -ppm 0`: the nominal tick and frequency |
| `disk-full` | one node | fill the filesystem holding the data directory | remove the fill files |
| `slow-disk` | one node | cap the server's block I/O through cgroup v2 `io.max` | lift the cap |

Under `PROOF_SEAL=passphrase` or `shamir` a restarted node comes back
sealed, so `kill` heals with `restart` and then `unseal`, and refuses to load
when the targets file has no `unseal` template. A heal whose unseal fails
stays pending and is retried as a whole: `restart` is a no-op on a running
process and `unseal` is one on an unsealed node. Under `none` the node
unseals itself and `kill` needs no `unseal`.

The schedule is drawn from `PROOF_SEED`: fault windows with a random gap
before each (10–30 s by default) and a random length (10–30 s), each window
one fault from `PROOF_NEMESIS` on targets picked as the table says, until the
run's duration is used up. A duration too short for one window is refused.
The schedule is recorded in the bundle, so the same seed and configuration
replay it. After the schedule every fault is healed once more, on a context
an interrupt does not cancel, before any check runs.

**Faults never touch the host running the tests.** No unit test runs
iptables, tc, a clock change, a mount or a signal to a process the test did
not start. Unit tests drive every fault through a fake target that records
the operations it was asked to perform. Real faults run only against the
remote targets a soak is configured with.

### The targets file

`PROOF_TARGETS` names a JSON file, required when any fault is configured.
Unknown fields are refused.

| Field | Meaning |
|---|---|
| `exec`, `signal`, `restart`, `unseal` | the `RemoteTarget` templates (`unseal` optional, required for `kill` under a sealed type), one argv element per word, rendered with `{{.Node}}`, `{{.Vars.<name>}}`, `{{shquote .Argv}}` (the fault's command, for `exec`) and `{{.Signal}}` (`KILL`, `STOP`, `CONT`, for `signal`) |
| `nodes.<name>.vars` | the node's template variables: its host, container name and so on; every `PROOF_NODES` name needs an entry and no other name may have one |
| `nodes.<name>.addresses` | the IPs or prefixes its peers reach it on; required by the partitions |
| `inspect.version`, `inspect.config` | commands run through `exec` that print the node's version and its configuration; the bundle keeps the version and the configuration's SHA-256, never the configuration |
| `partition.drop`, `partition.heal` | override the default iptables commands |
| `clock_rate.ppm`, `clock_rate.command` | the rate change, non-zero and within ±100000 (required for `clock-rate`; `-5000` is 0.5% slow), and an override of the default `["clusterproof", "clock-rate", "$@"]`, rendered with `{{.Node}}` and run through `exec`; the `$@` element, which it must contain, becomes the subcommand's flags |
| `disk_full.path`, `disk_full.fill`, `disk_full.free` | the data directory as the fault command sees it (required for `disk-full`) and overrides of the default commands |
| `slow_disk.device`, `slow_disk.cgroup`, `slow_disk.bytes_per_second`, `slow_disk.throttle`, `slow_disk.unthrottle` | the `MAJ:MIN` device, the server's cgroup v2 directory and the cap (all required for `slow-disk`), and overrides of the default commands |
| `schedule.min_gap`, `max_gap`, `min_length`, `max_length` | the schedule bounds |
| `heal_timeout` | the bound on each heal (default `1m`) |
| `recovery_timeout` | E4's recovery bound after the last heal, and the final read's (default `2m`) |
| `declared.read_mode` | what the operator states about the cluster's read mode; recorded in the bundle as `undeclared` when absent. The gate runs with `raft.lease_read` on, once per seal type the release candidate ships (`PROOF_SEAL`, not this file) |

A container deployment over ssh might use:

```json
{
  "exec":    ["ssh", "{{.Vars.host}}", "--", "docker", "run", "--rm", "--privileged",
              "--net=container:{{.Vars.container}}", "{{.Vars.fault_image}}", "{{shquote .Argv}}"],
  "signal":  ["ssh", "{{.Vars.host}}", "--", "docker", "kill", "--signal={{.Signal}}", "{{.Vars.container}}"],
  "restart": ["ssh", "{{.Vars.host}}", "--", "docker", "start", "{{.Vars.container}}"],
  "unseal":  ["ssh", "{{.Vars.host}}", "--", "{{.Vars.unseal_command}}"],
  "nodes": {
    "node-1": {"vars": {"host": "db1.example.com", "container": "qrdbd", "fault_image": "<fault-image>",
                        "unseal_command": "<install-dir>/bin/unseal-node"},
               "addresses": ["192.0.2.1"]}
  }
}
```

The `unseal` template is the operator's: it names a command on the node's
side that reads that node's passphrase or gathers its shares and unseals it.
The harness renders and runs it and sees only its exit status and output; no
seal material is in the targets file, the harness or the bundle.

### Clock faults and the Raft lease

The clock fault exists to test lease reads. With `raft.lease_read` on (the
default), a leader answers a linearizable read without a heartbeat round
while it holds a lease. It holds one when a quorum has acknowledged heartbeat
rounds it **sent** less than the lease duration ago, measured on its own
monotonic clock. A follower that acknowledges a round refuses to vote for
anyone else for at least the election timeout less two ticks. The lease is
that span less 1%. With go-quicraft's defaults (200 ms ticks, election
timeout 10 ticks), the follower's window is at least 1.6 s and the lease is
1.584 s.

- **A wall-clock step is a no-op for the lease, so the harness has no
  clock-skew fault.** Neither the lease nor the followers' election timers
  read the wall clock. Setting `CLOCK_REALTIME` leaves `CLOCK_MONOTONIC`
  alone, so a skewed node keeps its lease exactly as before. The earlier
  `clock-skew` fault was replaced by `clock-rate` for that reason.
- **A rate difference is what the 1% margin is for.** If the leader's clock
  runs slow by a fraction `r`, its lease lasts `0.99 / (1 − r)` of the
  follower's window in real time. A follower whose clock runs fast shortens
  that window in the same proportion. Up to a 1% difference the lease ends
  before any follower can vote again. That is the bound, and a run at
  `ppm: -5000` (0.5%) must stay linearizable. At `ppm: -20000` (2%) the bound
  no longer holds. A slowed leader's lease outlasts a follower's shortest
  window by about 1%, 16 ms with the defaults. The leader cannot measure its
  own rate, so it does not fall back to the heartbeat round; the lease is
  simply that much too long.
- **Rate alone cannot produce a stale read.** A stale read needs another
  leader, and a follower that keeps receiving heartbeats never campaigns,
  however fast or slow its clock runs. The out-of-bound case also needs the
  slowed node to be leader and to be cut off. The majority must then elect,
  and the new leader must commit a write, inside that 16 ms margin. The
  window is narrow: a follower's election timer is randomised over
  `[1, 2)` election timeouts, and the vote and the commit each cost round
  trips. `clock-rate` picks one node at random, and the schedule never
  overlaps windows, so a soak exercises the rate on its own. Composing it
  with `partition-isolate-leader` is a separate run.
- **A pause cannot extend the lease.** `pause` stops the server process
  (SIGSTOP, or `docker kill --signal=STOP` for a container), but its
  monotonic clock keeps running. A leader resumed after its followers
  elected a successor finds every acknowledgement older than the lease. It
  sends a heartbeat round before reading, and the round cannot reach a
  quorum that has moved on.

#### The rate nemesis

Because only rate matters, the clock nemesis perturbs rate, in two forms:

- **`pause` stops a node's clocks.** To the paused process its clocks stand
  still, and then jump ahead by the pause on resume. This is the extreme of a
  slow clock, and it runs on any deployment, including one kernel. The
  schedule's `min_length` and `max_length` bound each pause. A pause shorter
  than the follower's window (1.6 s with the defaults) is **in bound**: a
  paused leader keeps its leadership, and a paused follower misses rounds
  but is never outvoted. A pause longer than twice the election timeout
  (4 s with the defaults) is **out of bound**: a paused leader is replaced
  while it is stopped, and on resume it holds a lease that has run out.
- **`clock-rate` slows or speeds one kernel.** On a node whose kernel the
  harness does not share, `clusterproof clock-rate` sets the kernel's tick
  and frequency with `adjtimex(2)` (through `golang.org/x/sys/unix`). The
  nominal tick is 10000 µs, so one microsecond of tick is 100 ppm. The fault
  sets the tick to `10000 + ppm / 100` and puts the remainder in the
  frequency, in units of 2⁻¹⁶ ppm, which the kernel keeps within ±500 ppm.
  `-ppm -5000` (0.5% slow) is tick 9950 and frequency 0. `-ppm -20000` (2%
  slow) is tick 9800. `-ppm -150` is tick 9999 and frequency −50 ppm. Within
  ±1% is **in bound**, and beyond 1% is **out of bound**. Heal sets ppm 0,
  which is the nominal tick and frequency.

`clock-rate` refuses, before it changes anything, in two cases:

- **The node shares the harness's kernel.** The harness passes its own boot
  ID (`/proc/sys/kernel/random/boot_id`) as `-not-kernel`, and the
  subcommand exits 4 when the node's boot ID is the same. Containers on one
  kernel share a boot ID, so this also refuses a container on the harness's
  host.
- **The node lacks `CAP_SYS_TIME`.** The subcommand exits 3.

The fault runs `-check` on its target before it injects, and maps either
exit to a typed `RefusedError`. A refusal fails the inject, so no window
runs against a node it would not reach.

#### Lease-read results (2026-10-12)

`TestClusterFault_LeaseReadAfterLeaderChange` reads one key through the
leader from eight clients while the majority elects a successor and
overwrites it. `lease_read` was on, and the test ran four times on one
kernel. **No stale read was observed in any case.**

| Case | What the old leader served |
|---|---|
| isolated | the old value until 0.6–1.55 s after the cut, inside the 1.584 s lease and always before the overwrite was acknowledged; then `NOT_LEADER` |
| paused and isolated | nothing: every read after it resumed was refused with `NOT_LEADER` |
| paused, links restored | only the new value |

An isolated leader often stepped down before its lease ran out. That costs
availability, not safety.

#### Rate-nemesis results (2026-10-13)

The `pause` gate ran on three local nodes (one kernel) with seal `none`,
`lease_read` on and debug logs. The workload was gRPC, six clients on 16
keys, with a 3 s operation timeout. Lease use was counted from the leaders'
`read index fast path` lines (`lease_valid=true`), against reads that fell
back to `read index queued for heartbeat confirmation`.

| Bound | Pauses | Duration | Operations (failed, indeterminate) | Verdict | Leader | Lease reads (heartbeat fallbacks) |
|---|---|---|---|---|---|---|
| in | 24, 0.2–1.2 s; 10 on the leader | 90 s | 48,618 (2, 10) | proven: E1–E4 pass, no linearizability violation | unchanged | 161,835 (78) |
| in | 24, 0.2–1.2 s | 90 s | 37,295 (3,783, 3,733) | **undecided**: the checker was killed for memory | changed under host load (below) | not counted |
| in | 24, 0.2–1.2 s (repeat of the row above); 11 on the leader | 90 s | 42,676 (0, 0) | proven | unchanged | 142,288 (65) |
| in | 22, 0.2–1.2 s; 7 on the leader | 90 s | 58,277 (0, 0) | proven | unchanged | 193,785 (71) |
| out | 8, 5.1–11.6 s | 120 s | 66,710 (22, 38) | proven | replaced twice | 221,657 (117) |
| out | 9, 5.9–10.5 s | 120 s | 45,016 (88, 93) | proven | replaced three times | 150,062 (114) |

**No stale read was found.** In bound, a paused leader kept its leadership
and its lease through every pause, up to 1.15 s. Out of bound, the
expectation held in every case:

- The majority elected a successor while the leader was stopped.
- On resume, the old leader served no read from its lease. Its first reads
  queued for heartbeat confirmation, and it then learned the new term and
  stepped down (`leader lost`).
- The uncommitted tail of its log was overwritten (`WAL index truncated:
  leader overwrote uncommitted entries`).

go-quicraft's own lease guards showed up in the logs:

- A follower whose acknowledgement might still count toward a lease refused
  to campaign (`election skipped: a leader may be counting this replica's
  ack toward a read lease`).
- A leader with an active lease dropped a higher-term vote request
  (`dropped higher-term RequestVote, leader lease active`).

Two findings bear on running the gate, not on its verdict:

- **The checker's cost grows with indeterminate writes.** An indeterminate
  write never returns, so porcupine must place it everywhere after its
  invoke. The undecided run had 3,733 indeterminate operations on 16 keys,
  and its checker was killed for memory before the 10-minute per-key
  timeout. Size `keys` against the expected indeterminate count, or give the
  checker host more memory. The repeat of the same seed, without the host
  load, was proven.
- **A stalled step loop looks like a lost quorum.** In the undecided run,
  the 80-thread host was at a load of 110–136. After two follower pauses,
  the leader saw one active replica and stepped down (`leader lost quorum,
  stepping down to follower`). The followers' queued messages then arrived
  in one burst, about 2 s late. The followers were not slow; the leader's
  step loop had stalled. That is a liveness cost under CPU starvation, and
  it did not reproduce on the repeat.

**The `clock-rate` runs have not been executed.** They need one kernel per
replica with `CAP_SYS_TIME`, and the subcommand refuses the harness's kernel
by design. Its arithmetic and its refusals are covered by unit tests. On one
kernel, without the capability, it exits 3, and with `-not-kernel` set to
its own boot ID it exits 4. The expected outcomes follow from the arithmetic
above:

- at `ppm: -5000` (0.5% slow) the checker must find no violation;
- at `ppm: -20000` (2% slow), run alone, no stale read is reachable, because
  no election happens;
- composed with `partition-isolate-leader`, a stale read is possible only
  inside the 16 ms overshoot. Any such read is a defect, and must be reported
  with its bundle.

### Operator prerequisites

- **The default fault commands have not yet run against real nodes.** Unit
  tests prove what each fault renders and that heal follows inject; they
  cannot prove that iptables, `adjtimex`, `fallocate` and `io.max` behave as
  intended on a given host. Rehearse each fault by hand on the target
  cluster before the first gating run.
- **`clock-rate` needs a kernel per replica, with time synchronisation off.**
  `adjtimex(2)` is not namespaced: the fault changes the rate of every clock
  on the kernel the node runs on, so replicas that share a kernel (containers
  on one host) drift together and the fault tests nothing. Each replica runs
  on its own host or VM, and a time-sync daemon on that kernel would steer
  the rate back. The subcommand refuses the harness's own kernel (exit 4),
  but it cannot tell two replicas on one kernel apart.
- **`clock-rate` runs `clusterproof clock-rate` on the node.** The fault
  container, or whatever `exec` reaches, needs the `clusterproof` binary on
  its `PATH`, or `clock_rate.command` names where it is.
- `partition-*` and `clock-rate` need a privileged fault container in the
  node's network namespace (`CAP_NET_ADMIN`, `CAP_SYS_TIME`); `disk-full`
  needs the data volume mounted in it; `slow-disk` needs write access to the
  server's cgroup v2 directory.

## Container mode and BOOT-01

Container mode runs every container command through the inventory's
`docker` template, using the same renderer as the targets file. The default
is the local `docker` CLI. A wrapper such as
`["ssh", "{{.Vars.host}}", "--", "docker", "{{shquote .Argv}}"]` reaches a
remote node's runtime instead; `shquote` keeps the command one word per
argument through the remote shell. The commands it runs are:

| Step | Command (after the template) |
|---|---|
| identity | `run --rm --network none -v <tls_dir>:/etc/qrdb/tls:ro <image> node-identity --cert /etc/qrdb/tls/node.pem`; the last line of output is the identity, and it must be `spki:` followed by 64 lower-case hex digits |
| start | `run -d --name clusterproof-<name> --network <network> [--ip <address>] --cap-add IPC_LOCK --ulimit memlock=-1:-1 -v <tls_dir>:/etc/qrdb/tls:ro -v <data>:/var/lib/qrdb <image> start --config /etc/qrdb/qrdb.yaml --listen 0.0.0.0:63001 --raft-address <address>:63001 --data-dir /var/lib/qrdb --discovery static --peers <id>=<address>:63001,... --member-identities <id>=<identity>,... --replica-id <id> --enable-quic --tls-ca-file /etc/qrdb/tls/ca.pem --tls-cert-file /etc/qrdb/tls/node.pem --tls-key-file /etc/qrdb/tls/node-key.pem` |
| logs | `logs --tail <n> clusterproof-<name>` |
| remove | `rm -f clusterproof-<name>`, on every node, before the run and after it |

The flags override the image's configuration file, so the image runs
unmodified. A node serves Raft on 63001, REST on 63002, gRPC on 63003 and
QUIC on 63004 at its inventory address.

`--ip` is passed only on a user-defined network, where the address must be
an IP on that network's subnet. On the default `host` network, the address
is the node's own and can be an IP or a name.

First boot dials each node over REST with the inventory's credential. It
reads the node's barrier status and skips a node that is unsealed and has an
initialization time. Otherwise it sends the ceremony with:

- the Security Officer PIN;
- the user PIN;
- the barrier secret, when one is configured;
- the credential's certificate as the first administrator.

After a ceremony error it reads the barrier status again, so a request that
succeeded but lost its reply is not retried into a failure. Each node is
retried until the shared five-minute bound expires. The nodes run in
parallel, and every node's failure is reported.

`Verify` makes one round per transport, in the order QUIC, gRPC, REST. A
round calls health, status, topology, peers and capabilities on every node,
then checks the round as a whole. A failing round is retried until it holds
or until the bound expires. The error names the check that failed last:
`health`, `status`, `topology`, `peers` or `capabilities`.

### The inventory

`PROOF_INVENTORY` names a JSON file. Unknown fields are refused, and the
file is read before any container command runs.

| Field | Required | Meaning |
|---|---|---|
| `image` | yes | the release image to run, by tag or digest |
| `docker` | no | the container runtime command, one argv element per word; `$@` stands for the command and may not lead; rendered with `{{.Node}}`, `{{.Vars.<name>}}` and `{{shquote .Argv}}`. Default `["docker", "$@"]` |
| `network` | no | the container network; default `host` |
| `credentials.ca_file`, `cert_file`, `key_file` | yes | the client credential every check dials with, and whose certificate first boot enrolls as administrator |
| `first_boot.so_pin_file`, `user_pin_file` | yes | files holding the Security Officer and user PINs; a trailing newline is dropped and an empty file is refused |
| `first_boot.secret_file` | no | a file holding the barrier secret, for a seal type that takes one |
| `nodes[].name` | yes | letters, digits, `.`, `_` and `-`, at most 64 characters; the container is `clusterproof-<name>` |
| `nodes[].replica_id` | yes | non-zero and unique |
| `nodes[].address` | yes | the address peers and clients reach the node on; unique |
| `nodes[].tls_dir` | yes | the node's directory on its host, holding `ca.pem`, `node.pem` and `node-key.pem`; the certificate must name `address` |
| `nodes[].data` | yes | the node's data directory on its host; it must be empty before a run |
| `nodes[].vars` | no | the node's template variables for `docker` |

At least three nodes are required.

```json
{
  "image": "<registry>/qrdbd:<tag>",
  "docker": ["ssh", "{{.Vars.host}}", "--", "docker", "{{shquote .Argv}}"],
  "credentials": {"ca_file": "ca.pem", "cert_file": "admin.pem", "key_file": "admin-key.pem"},
  "first_boot": {"so_pin_file": "so.pin", "user_pin_file": "user.pin"},
  "nodes": [
    {"name": "node-1", "replica_id": 1, "address": "192.0.2.1",
     "tls_dir": "<install-dir>/tls", "data": "<install-dir>/data",
     "vars": {"host": "db1.example.com"}}
  ]
}
```

The example shows one node; a real inventory lists every founder.

## The soak and its evidence bundle

`make cluster-proof` loads the configuration, then:

1. records each node's identity through `inspect`, if configured;
2. runs the workload and plays the schedule together for `PROOF_DURATION`; a
   fault that fails to inject stops the load, since the run can no longer
   prove anything;
3. heals every fault again, whatever happened;
4. **E4**: waits up to `recovery_timeout` for every node to report every
   shard it hosts led and to accept a probe write;
5. runs the final read;
6. **E3**: asks the `ReplicaStates` seam for every replica's state;
7. writes the evidence, reads it back, derives the verdict from what was
   written, and writes the derived files and the manifest.

A gating soak runs for 24 h, and one 72 h soak runs before the
release-candidate tag. The duration is a parameter, never a constant.

The evidence bundle is one directory per run. Every JSON file carries a
`format` version (`2`), and a check refuses another.

| File | Content |
|---|---|
| `manifest.json` | the harness's version, commit and Go version, the creation time, and the SHA-256 of every other file |
| `history.jsonl` | the full history (recorded) |
| `nemesis.jsonl` | every nemesis event — the schedule, then each inject and heal with its phase, window, targets and error — with its time on the history's clock (recorded) |
| `run.json` | the run's inputs and what it observed outside the history: nodes, duration, faults, seed, `seal` (the `PROOF_SEAL` type), schedule, the workload without its credentials, the declared read mode, the workload's counts, the E3 and E4 observations, and the run's own errors, unhealed faults and interruption (recorded) |
| `nodes/<name>/node.json` | each node's address, version and configuration SHA-256, or `not available` and why (recorded) |
| `timeline.jsonl` | every call, return and nemesis event on the one clock, in order (derived) |
| `timings.json` | throughput and ok-latency percentiles overall, outside and inside fault windows, and per window the first acknowledged write after inject and after heal (derived) |
| `verdict.json` | the outcome, its exit code, the seal type it was reached under, the reasons it is not proven, each criterion E1–E7 with its status and detail, and every checker's full result (derived) |

The bundle contains no credential, and the node configuration only as a
digest. Node logs and metrics are not collected yet.

`make cluster-proof-check` verifies every file against the manifest, refuses
a missing, extra or non-regular file and any path outside the bundle, decodes
the recorded files, runs the same checkers again, and derives the timeline,
the timings and the verdict afresh: each must equal the bundle's byte for
byte. A bundle edited and resealed with new checksums still fails, unless the
verdict genuinely follows from the edited evidence: a `run.json` whose `seal`
was changed no longer matches the verdict's.

### Verdict

| Status | Meaning |
|---|---|
| `pass` / `fail` | E1–E4 decided |
| `unavailable` | E1–E4 could not be decided, with the reason |
| `measured` | E5–E7, reported without a bound |

The outcome is **violated** (exit `1`) if any of E1–E4 is `fail`; otherwise
**invalid** (exit `2`) if any of E1–E4 is `unavailable` or the run has a
reason — a seal type the release does not ship, interrupted, a recorded
error, a fault that failed or never healed, or no fault injected; otherwise **proven** (exit `0`).

### Checker and replica seams

- **E2.** `Options.Checkers` runs any `history.Checker` beside the ledger,
  and `cmd/clusterproof` adds the porcupine check through `Env.Configure`,
  for a run and for a check alike. A soak given no checker reports E2
  `no linearizability checker configured`. A checker that examined nothing
  (`Checked` 0) makes E2 unavailable, never failed. The check refuses a
  bundle when it is given other checkers than the run used.
- **E3.** `Options.Replicas` takes a `ReplicaStates`: every replica's shard,
  replica, applied index and state-machine hash. Replicas of a shard at the
  same applied index must report the same hash. A follower still applying the
  last entries trails the leader, so while a shard's replicas stand at
  different indexes the read repeats; if they have not met when
  `RecoveryTimeout` passes, the cluster had not quiesced and E3 is
  unavailable. `LoadOptions` wires
  `SDKCluster`, which asks every node for its own replicas through
  `AdminClient.ReplicaStates` (see
  [../clustering/api.md](../clustering/api.md#replica-states)) and tags each
  row with the node it came from. One node that cannot be reached or refuses
  fails the whole read, so E3 is unavailable rather than decided on the nodes
  that answered — see [api.md](api.md#replica-state-comparison). A soak built
  without `LoadOptions` and given no source still reports E3 unavailable,
  never passed.

## Configuration

The make targets take their parameters from make or environment variables;
[cli.md](cli.md) is the reference.

| Variable | Default |
|---|---|
| `PROOF_NODES` | none — required for `cluster-proof` |
| `PROOF_DURATION` | `5m`; a gating run gives `24h`, and the soak before the release-candidate tag `72h` |
| `PROOF_NEMESIS` | every fault |
| `PROOF_BUNDLE` | `./clusterproof-out/<UTC start>` for a run; the newest such directory for a check |
| `PROOF_TARGETS` | none — required when a fault is configured |
| `PROOF_WORKLOAD` | the workload defaults |
| `PROOF_SEED` | drawn at random and recorded |
| `PROOF_SEAL` | none — required for `cluster-proof`; `none`, `passphrase` or `shamir` |
| `PROOF_INVENTORY` | none — required for `cluster-proof-boot` |

No configuration key is added to `qrdbd` for this feature.

## Error semantics

- An outcome is classified at the client from the typed error the transport
  returns: an error that guarantees the operation was not applied (a refused
  precondition, an authorization failure, a refusal before proposal) is
  `fail`; a timeout, a cancelled context, or a connection lost after the
  request was sent is `indeterminate`. Unknown errors are `indeterminate` —
  the conservative choice, since it can never produce a false loss.
- A refused configuration, a run that cannot start, or a bundle that cannot
  be written exits `2` before any verdict. Everything the run itself
  observed — an unhealed fault, a failed inject, an interrupt — is recorded
  in `run.json` and makes the verdict invalid, so the bundle still shows it.

## Test strategy

| Layer | What it covers | Where |
|---|---|---|
| Unit | workload generation, ledger rules, the linearizability model and its evidence (a stale read, including one the ledger passes, a lost write, a failed write that shows, a wrong CAS result, every indeterminate case, and the same anomaly on every rerun), history format, every fault through a fake target, the schedule, configuration parsing, and the soak end to end on a fake cluster: a clean run exits `0`, a lost write `1`, an unhealed or failed fault, an interrupted run and a fault-free run `2`, an unset or unknown seal type is refused, `kill` under a sealed type restarts then unseals and retries a failed unseal, and every bundle tampering — a changed seal type included — fails the check; plus one fault-free run against an in-process standalone node, whose history passes E1 and E2 | each `test/clusterproof/...` package, tag `clusterproof`, `make test-clusterproof` |
| In CI | a SIGKILL of the leader, then of a follower, under a running write load, once per seal type as the subtests `none` and `passphrase` (`TestClusterFault_SIGKILLUnderLoad`; under `passphrase` each restarted node must report sealed and refuse a data operation before it is unsealed), and a partition of the leader (`TestClusterFault_PartitionIsolatesLeader`), each followed by the ledger and replica checks on a self-spawned three-node cluster; and linearizable reads through a leader that is isolated, paused and isolated, or paused while a successor overwrites the key, with `lease_read` on (`TestClusterFault_LeaseReadAfterLeaderChange`) | `test/integration/cluster_crash_test.go`, `cluster_partition_test.go` and `cluster_lease_test.go`, selected by `make integration-test-cluster-verification` |
| In CI, ports | the pinned port set ([user.md](user.md#pinned-port-set)): a self-spawned three-node cluster on operator-supplied TLS, with every port flag set and every optional protocol on, must listen on exactly the pinned sockets. The test reads each process's sockets from the kernel — the socket inodes in `/proc/<pid>/fd` matched against `/proc/<pid>/net/{tcp,tcp6,udp,udp6}`, TCP in `LISTEN` and unconnected UDP outside the ephemeral range — and fails on an extra socket, a missing one, or one on another address than `--host`. It checks twice: sealed (a subset of the set) and serving (the set exactly). The trust-anchor and CSR-join ports are set and must stay unbound, because operator TLS holds no cluster CA. The nodes run with `QRDB_SECURE_ALLOW_PROCESS_INSPECTION=true`, since a non-dumpable process's `/proc/<pid>/fd` is unreadable. Not exercised: the per-database etcd listener (its `listen_addr` is replicated, so three nodes on one host cannot each bind it), multicast discovery, and the anchor and join listeners of a qrdb-managed CA (`cluster_ca_anchor_spof_test.go` covers those functionally) (`TestClusterPorts_PinnedSetIsExactlyWhatQrdbdBinds`) | `test/integration/cluster_ports_test.go`, selected by `make integration-test-cluster-verification` |
| Unit, container mode | inventory parsing and every refusal, the exact container commands (host and user-defined network, through a remote wrapper), identity parsing, first boot (every node, a booted node skipped, a ceremony error after the barrier came up, retry to the bound, unreachable barrier, missing secrets) and every BOOT-01 check failing on its own — all through a fake runner and fake transports | `test/clusterproof/container`, tag `clusterproof`, `make test-clusterproof` |
| Bring-up | BOOT-01 on real containers: static bootstrap from the release image, first boot, and the admin checks on every node over every transport | `test/clusterproof/container`, tags `clusterproof,clusterproof_container`, `make cluster-proof-boot` |
| Soak | the gating run: real nodes, real faults, the full duration | `make cluster-proof`, then `make cluster-proof-check` |

Every checker test includes a history the checker **must reject** — a lost
acknowledged write, a phantom, a duplicated write, a read that goes back in
time after a newer read. A checker that has only
seen passing histories has not been shown to be able to fail.

The in-CI tests use processes the test itself started, so signalling them is
within the unit-of-test rule; their partition is simulated at the transport
seam of the self-spawned cluster — a harness UDP relay in front of every
node's Raft listener, cut and healed by the test — not with host packet
filters.

## Observability

The harness adds no metric or log event to the server. It prints one
progress line per 10 seconds (elapsed time, operations by outcome, faults
injected) and the verdict at the end.
