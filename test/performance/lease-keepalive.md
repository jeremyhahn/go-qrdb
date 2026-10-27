# Lease keep-alive latency tail

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-13
> Explains the lease keep-alive latency work; the benchmark output is the evidence.

A lease keep-alive used to be a replicated log entry, so every renewal waited on
a raft-log fsync. On a busy host that fsync is the heavy tail: the median stayed
low while p90 ran a multiple of it. A renewal is now answered by the leader from
memory after a quorum read confirms it still leads, and, as in etcd, a periodic
checkpoint records the remaining TTL of long leases for the next leader (see [leases architecture](../../docs/leases/architecture.md#keep-alive-leader-local-renewal)).

## Method

`TestRaft_LeaseKeepAliveLatencyTail` (`sdk/go/lease_keepalive_latency_test.go`)
boots a real single-replica raft group on real disk, acquires one lease, then
times 64 `Renew` calls separated by a fixed cadence. It fails when p90/median is
3x or more and the p90 is also above a floor of a tenth of the node's raft tick
(2.5 ms at the transport-less 25 ms tick). Under the floor, a high ratio is
scheduler jitter on a loaded host: a CI runner in FIPS 140-3 mode measured
4.75x on a 1.3 ms p90 with nothing on the renewal path waiting. The log-fsync
tail this test was written for sits above it (the "Before" writer rows below:
p90 3.2–3.4 ms), as does any wait on a tick-paced step. `TestLeaseKeepAliveTail_Waits`
checks the rule against those distributions.

Contention is applied from outside the test:

- pinned to two cores: `taskset -c 0,1`;
- hogs: eight `taskset -c 0,1 yes > /dev/null` processes running alongside;
- tmpfs control (fsync is nearly free): `TMPDIR=/dev/shm`.

```
cd sdk/go
go test -tags "$(make -s -C ../.. print-build-tags)" -c -o /tmp/sdk.test .
taskset -c 0,1 /tmp/sdk.test -test.run TestRaft_LeaseKeepAliveLatencyTail \
    -test.v -lease-keepalive-cadence=1500ms
```

## Before (every renewal through the raft log)

| Condition | Median | p90 | Max | p90/median |
|---|---|---|---|---|
| Real disk, no contention, 1.5 s cadence | 0.74 ms | 1.07 ms | 3.3 ms | 1.4x |
| Pinned to 2 cores, no hogs | 0.77 ms | 0.95 ms | 1.7 ms | 1.2x |
| Hogs, 1.5 s cadence | 0.48 ms | 6.0 ms | 13.8 ms | 12.4x |
| Hogs, 300 ms cadence | 4.7 ms | 12 ms | 100 ms | 2.6x |
| Hogs + tmpfs | 0.13 ms | 0.16 ms | 0.28 ms | 1.2x |

The tmpfs row is the control: with a cheap fsync the tail disappears, which
places the cause in the log fsync rather than in the network or the scheduler.

## After (leader-local renewal, 1 s checkpoints, since replaced)

| Condition | Median | p90 | Max | p90/median |
|---|---|---|---|---|
| Unconstrained, 100 ms cadence | 50 µs | 64 µs | 74 µs | 1.27x |
| Unconstrained, 1.5 s cadence | 56 µs | 72 µs | 93 µs | 1.29x |
| Pinned to 2 cores, 1.5 s cadence | 59 µs | 80 µs | 99 µs | 1.35x |
| Hogs, 1.5 s cadence | 29 µs | 36 µs | 141 µs | 1.25x |
| Hogs, 300 ms cadence | 12.5 µs | 21.8 µs | 45.6 µs | 1.74x |
| Hogs + tmpfs, 100 ms cadence | 16.8 µs | 30.7 µs | 33.7 µs | 1.83x |

Medians fall from hundreds of microseconds to milliseconds down to tens of
microseconds, and p90/median stays under 2x in every condition, including the
one that measured 12.4x before.

## Disk-sync contention: 1 s checkpoints replaced by etcd's (2026-10-07)

The "after" rows above were taken with the leader checkpointing every renewed
lease each second. That left a different tail. Each checkpoint was a Raft
proposal, so its commit-to-apply window — the quicraft `SaveState` fsync, then
the pebble apply sync — was open roughly once a second. The renewal's
linearizable `SyncRead` waits for the applied index to reach the read index, so
a renewal that landed inside that window waited for both syncs. Under a
competing disk-sync writer those syncs take hundreds of milliseconds to
seconds.

Contention here is a second process writing 64 MiB with `O_DSYNC` in a loop on
the same filesystem:

```
mkdir -p /tmp/diskhog
( while [ -d /tmp/diskhog ]; do
    dd if=/dev/zero of=/tmp/diskhog/f bs=1M count=64 oflag=dsync status=none
  done ) & HOG=$!
/tmp/sdk.test -test.run TestRaft_LeaseKeepAliveLatencyTail -test.v
rm -rf /tmp/diskhog; kill $HOG
```

**Before** (1 s checkpoint of every renewed lease), six runs:

| Run | Median | p90 | Max | p90/median |
|---|---|---|---|---|
| 1 | 57 µs | 81 µs | 214 ms | 1.42x |
| 2 | 55 µs | 107 µs | 248 ms | 1.95x |
| 3 | 70 µs | 21.0 ms | 4.94 s | **300x — fails** |
| 4 | 74 µs | 96 µs | 1.57 s | 1.29x |
| 5 | 68 µs | 105 ms | 2.76 s | **1541x — fails** |
| 6 | 53 µs | 36.6 ms | 1.00 s | **691x — fails** |

Two controls located the cause. With the checkpoint interval raised to 5 m and
nothing else changed, the same writer left max at 108–160 µs and p90/median at
1.16–1.37x. A standalone node, which has no checkpoints and no log, stayed at a
median of 16–22 µs and a max of at most 129 µs under the same writer. Making the
pebble apply `NoSync` did not remove the stalls, because the quicraft
commit-to-apply `SaveState` fsync is part of the wait. That fsync is go-quicraft's
and is out of scope here.

**After** (etcd checkpoints: every 5 m, only leases longer than the interval,
remaining TTL rather than a renewed deadline), five runs under the same writer:

| Run | Median | p90 | Max | p90/median |
|---|---|---|---|---|
| 1 | 66 µs | 86 µs | 108 µs | 1.30x |
| 2 | 66 µs | 84 µs | 110 µs | 1.27x |
| 3 | 63 µs | 79 µs | 94 µs | 1.25x |
| 4 | 57 µs | 77 µs | 107 µs | 1.34x |
| 5 | 65 µs | 82 µs | 32.6 ms | 1.27x |

With no writer, three runs gave a median of 52–58 µs, p90 72–79 µs, a max of
93–117 µs, and p90/median of 1.35–1.40x.

The test's lease has a TTL shorter than the 5 m interval, so it is never
checkpointed, as in etcd, and no proposal opens a window in front of its
renewals. A lease long enough to be checkpointed pays for it once per interval:
its next renewal goes through the log, which clears the checkpoint, and every
renewal after that is answered from memory again.

## Re-landed on main (2026-10-13)

The same test, run against main before the re-land (every renewal through
the raft log, answered from its apply result) and after it, at the test's
default 100 ms cadence, unpinned, with and without the disk-sync writer above:

| Tree | Condition | Median | p90 | Max | p90/median |
|---|---|---|---|---|---|
| Before | No writer, run 1 | 1.99 ms | 5.00 ms | 6.7 ms | 2.51x |
| Before | No writer, run 2 | 1.94 ms | 4.46 ms | 37.9 ms | 2.30x |
| Before | Writer, run 1 | 884 µs | 3.43 ms | 76.4 ms | **3.87x — fails** |
| Before | Writer, run 2 | 1.04 ms | 2.64 ms | 220 ms | 2.55x |
| Before | Writer, run 3 | 880 µs | 3.18 ms | 106 ms | **3.62x — fails** |
| After | No writer | 69 µs | 87 µs | 107 µs | 1.26x |
| After | Writer, run 1 | 69 µs | 87 µs | 896 ms | 1.25x |
| After | Writer, run 2 | 72 µs | 85 µs | 166 µs | 1.19x |
| After | Writer, run 3 | 71 µs | 86 µs | 136 µs | 1.21x |

Figures are from a single development host and vary run to run; the test's
assertion, not these numbers, is the contract.
