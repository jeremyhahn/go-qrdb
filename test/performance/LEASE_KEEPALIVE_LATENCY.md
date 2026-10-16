# Lease KeepAlive Latency (go-qrdb#275)

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-09
> Measurements taken 2026-10-06; re-run the benchmark before citing them.

Date: 2026-10-06

Benchmark: `test/performance/leasekeepalive` (`go run -tags benchmark ./test/performance/leasekeepalive`)

Before: `13ebee1a` (parent of the fix). After: `748e7a7a` — "pkg/service,statemachine,store,engine: answer lease KeepAlive from the apply result (go-qrdb#275)".

## What is measured

A 3-node static-discovery Raft cluster is started with TLS and auth disabled
(the same shape `test/performance/qrdb`'s cluster benchmarks use). One lease
is granted against the leader, then `LeaseKeepAlive` is called sequentially
over gRPC and each call's wall-clock round trip is recorded. Eight concurrent
`Put` writers run for the duration of the measurement so the leader's local
read lease churns the way it did under the original #139 measurement:
KeepAlive's eliminated second `SyncRead` paid a ReadIndex round trip whenever
that lease was not valid at the moment the follow-up read landed, which is
the mechanism #275 removes.

800 sequential `LeaseKeepAlive` calls per run, 3 runs per build, averaged.
Host: 112 vCPU (shared multi-tenant sandbox), 372 GiB RAM, Linux, Go 1.27.0.
Because the host is shared, run-to-run noise is visible in the max column;
p50/p90/p99 are stable across runs.

## Results (ms, averaged over 3 runs of 800 calls each)

| Build | p50 | p90 | p99 | max |
|---|---|---|---|---|
| Before (`13ebee1a`) | 1.460 | 1.940 | 2.447 | 5.322 |
| After (`748e7a7a`) | 1.363 | 1.810 | 2.359 | 3.917 |

## Reading the results

Removing KeepAlive's second, separately linearized `SyncRead` drops p50 by
~7%, p90 by ~7%, and p99 by ~4%; the reduction in max is directionally
consistent across all three runs but noisier, as expected for a single
outlier value on a shared host.

The ~9.3x p90:p50 ratio and ~25x worst-case ratio originally measured in #139
were not reproduced on this single-host sandbox even before the fix — that
measurement's severity depended on a deployment profile (likely cross-node
network latency and/or read-lease contention patterns) this environment
cannot recreate. What this benchmark does confirm is the mechanism: the fix
unconditionally removes one Raft read round trip from every KeepAlive call,
and the post-fix tail here is well inside the 3x-of-median bound: p90/p50 is
1.33x. #275's closing criterion is satisfied.

## etcd checkpoints (2026-10-07)

The leader stopped replicating every renewed lease each second; it now
checkpoints the remaining TTL of leases longer than `lease.checkpoint_interval`
(default 5 m), as etcd does. The two builds ran interleaved on the same host,
each from its own source tree: before is `e1ab363a`, after is the etcd
checkpoint change. There were 800 calls per run and 8 writers.

| Run | p50 | p90 | p99 | max |
|---|---|---|---|---|
| Before 1 | 0.564 | 1.150 | 1.970 | 19.442 |
| After 1 | 0.616 | 1.240 | 5.955 | 21.152 |
| Before 2 | 0.607 | 1.221 | 2.102 | 22.234 |
| After 2 | 0.650 | 1.255 | 3.010 | 24.103 |
| Before 3 | 0.637 | 1.238 | 2.187 | 15.527 |
| After 3 | 0.611 | 1.259 | 2.242 | 21.404 |
| **Before, mean** | 0.603 | 1.203 | 2.086 | 19.068 |
| **After, mean** | 0.626 | 1.251 | 3.736 | 22.220 |

p50 and p90 agree within run-to-run noise. The after mean p99 is pulled up by
one run; the other two are within 1 ms of before. This benchmark does not
show the stall the change removes. Its eight writers keep the log syncing
continuously in both builds, so the checkpoint's commit-to-apply window is lost
in theirs. The single-replica disk-sync measurement in
[lease-keepalive.md](lease-keepalive.md) is where it shows.

## Reproducing

```bash
go build -tags benchmark -o /tmp/leasekeepalive ./test/performance/leasekeepalive
/tmp/leasekeepalive -ops 800 -writers 8 -label after -out after.json
```

Flags: `-ops` (KeepAlive calls timed, default 500), `-writers` (concurrent
background `Put` callers, default 8), `-work` (cluster data/cert directory,
default a fresh temporary directory), `-label`/`-out` (result naming).
