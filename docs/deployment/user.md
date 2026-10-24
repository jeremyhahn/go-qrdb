# Deployment — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-23
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

## Network ports and firewall

Every socket `qrdbd` opens — port, protocol, flag and YAML key, build tag,
whether it is on by default and who must reach it — is the
[pinned port set](../cluster-verification/user.md#pinned-port-set), which a
test keeps exactly true. Every port can be moved, and every key can also be set
through its `QRDB_` environment variable (`server.host` → `QRDB_SERVER_HOST`).

**Every node must open the same set.** Every node serves every endpoint, and a
node that holds no replica of a database forwards the call to a replica's
leader over the same transport: REST to REST, gRPC to gRPC (the Unix socket
forwards over gRPC too), QUIC to QUIC and MCP to MCP. So each API port that is
enabled must be reachable from the other voters as well as from clients.

### Firewall recipe: three voters

Take voters at 192.0.2.11, 192.0.2.12 and 192.0.2.13, with clients on
198.51.100.0/24.

Peer-only rules. Allow these from the other two voters only:

- UDP 63001: Raft.
- TCP 63006: enrollment. Open it also to a new node while that node joins.
- TCP 63007: needed only when `cluster_join.enabled` is set, and only from a
  joining node.
- UDP 63777: needed only with `--discovery multicast`, on the voters' segment.

Client-facing rules. Allow these from clients **and** from the other two voters:

- TCP and UDP 63002: REST and HTTP/3. Metrics scrapers use this port too.
- TCP 63003: gRPC.
- UDP 63004 and TCP 63005: open these only if QUIC or MCP is enabled.

Client-only rules. Allow these from clients only, when the feature is on:

- TCP 3306 and TCP 5432.
- Each database's etcd `listen_addr`.

Deny everything else inbound. The Unix socket needs no rule; protect it with
file permissions.

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
  monotonic clock, and clock offset between nodes does not matter. A VM
  whose clock rate wanders after live migration or a long pause is the case
  to watch.
- **Run the same `ElectionRTT` and `RTTMillisecond` on every replica.**
  `qrdbd` exposes no setting for either; it takes them from go-quicraft's
  defaults. A program that embeds the engine and sets them must set the same
  values on every replica. A replica with a shorter
  election window backs no lease — still safe — but reads then take the
  heartbeat round.

Near or after lease expiry a read falls back to the heartbeat round on its own;
nothing needs configuring for that. See [../05-consistency.md](../05-consistency.md#3-leaseread) for why.

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
