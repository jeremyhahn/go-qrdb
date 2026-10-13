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

# Deployment — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-13

## Give qrdbd locked memory

`qrdbd` locks the pages that hold cluster keys, credentials and PINs so they
never reach swap. Grant it enough and an unconfigured node gets its full
64 MiB secure-memory budget; grant it less and it starts with a smaller budget
sized from what the host allows, and says so in its startup log.

### Docker

```bash
docker run --cap-add IPC_LOCK --ulimit memlock=-1:-1 \
  -v /srv/qrdb:/data <image> start --config /etc/qrdb/qrdb.yaml
```

### Docker Compose

```yaml
services:
  qrdbd:
    image: <image>
    cap_add:
      - IPC_LOCK
    ulimits:
      memlock:
        soft: -1
        hard: -1
```

### systemd

```ini
[Service]
ExecStart=<install-dir>/qrdbd start --config /etc/qrdb/qrdb.yaml
LimitMEMLOCK=infinity
# or, instead of lifting the limit, grant the capability:
# AmbientCapabilities=CAP_IPC_LOCK
```

### Kubernetes

```yaml
securityContext:
  capabilities:
    add: ["IPC_LOCK"]
```

## Check what you got

```text
level=INFO msg="secure memory budget installed" budget_bytes=67108864 source=default rlimit_memlock_source="RLIMIT_MEMLOCK not enforced (CAP_IPC_LOCK)" rlimit_memlock_bytes=8388608
```

`source=default` with 67108864 bytes is the full budget. `source="derived from
RLIMIT_MEMLOCK"` means the host limit sized it; grant one of the settings above
to lift it.

## Attaching a debugger

`qrdbd start` makes itself unreadable to other processes before it holds any
key (see [architecture.md](architecture.md#process-inspection-hardening)), so
`gdb -p`, `dlv attach` and `strace -p` are refused, and a crash writes no core
file. To debug a node, opt out for that run only:

```bash
QRDB_SECURE_ALLOW_PROCESS_INSPECTION=true <install-dir>/qrdbd start --config /etc/qrdb/qrdb.yaml
```

```text
level=WARN msg="secure: process inspection hardening DISABLED by environment; a debugger or any process running as this user can read protected memory" env=QRDB_SECURE_ALLOW_PROCESS_INSPECTION mechanism="prctl(PR_SET_DUMPABLE, 0)"
```

Remove it afterwards. Never set it in a unit file, a compose file or an image.
The node records every start with the opt-out in its compliance audit trail
(action `disable-inspection-hardening`), and a FIPS 140-3 build refuses the
opt-out altogether. Attach the debugger after start: a node started under a
debugger that is already attached refuses to run unless the opt-out is set.

## Keep replica clocks disciplined

Lease reads (`lease_read`, on by default) are linearizable only while the
clock rate drift between the replicas of a shard stays under 1 %, and only
while every replica uses the same Raft timing. For a cluster of more than one
node:

- **Run chrony on every replica.** The bound is on clock *rate*, not on the
  time of day: a wall-clock step does not affect the lease, which runs on the
  monotonic clock.
- **Run the same `ElectionRTT` and `RTTMillisecond` on every replica.**
  `qrdbd` exposes no setting for either; it takes them from go-quicraft's
  defaults. A program that embeds the engine and sets them must set the same
  values on every replica.

See [../05-consistency.md](../05-consistency.md#3-leaseread) for why.

## Troubleshooting

- **`cannot derive a secure memory budget ... less than the 1048576-byte
  minimum`** — the host limit is too small to run. Apply one of the settings
  above.
- **`secure memory budget N MiB rejected ... exceeds the process's
  locked-memory limit`** — you configured a budget larger than the host
  allows. Raise the limit as above, or lower `secure_memory_budget_mib`.
- **`QRDB_SECURE_ALLOW_PROCESS_INSPECTION="..." is not a boolean`** — the
  opt-out holds something other than `true`/`false`/`1`/`0`. Unset it, or set
  it to `true` to debug.
- **`process inspection hardening ... failed`** — the platform refused the
  mechanism (a seccomp profile that denies `prctl`, for example). `start`
  refuses to run inspectable; allow the call in the profile.
- **A monitoring agent lost `qrdbd`'s `/proc` entries** — expected: a
  non-dumpable process's `/proc/<pid>/` belongs to root. Run the agent with
  `CAP_SYS_PTRACE` or read the node's own metrics endpoint instead.
- **`process is already traced by pid N`** — something attached to `qrdbd`
  before it hardened itself (a debugger, `strace`, a profiler). Detach it, or
  set the opt-out for a debugging session.
- **`process inspection hardening tracer check failed: ...`** — on Linux,
  `start` could not read `TracerPid` from `/proc/self/status`. This is
  typical of a sandbox or minimal container with no `/proc` mounted. Without
  that line `qrdbd` cannot show that nothing is already attached to it, so it
  refuses to start (`ProcessHardeningError`, mechanism `tracer check`) rather
  than assume it is untraced. Mount procfs in the container (`/proc` is
  present by default in Docker, Kubernetes and systemd services), or set the
  opt-out for a debugging session.
- **`QRDB_SECURE_ALLOW_PROCESS_INSPECTION is refused in FIPS 140-3 mode`** — a
  FIPS build does not run inspectable. Unset the variable.
