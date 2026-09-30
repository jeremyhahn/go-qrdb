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

# Deployment — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-30

What a host must grant `qrdbd` to run as configured — locked memory — and
what `qrdbd` takes away from the host in return: the ability of other
processes to read its memory. Ports, TLS and cluster formation are covered by
[../configuration/](../configuration/) and [../clustering/](../clustering/).

## Problem

`qrdbd` keeps cluster keys, credentials and PINs in mlock'd pages, admitted
against a process-wide secure-memory budget
([../configuration/cli.md](../configuration/cli.md#secure-memory-budget)). The
kernel bounds how much a process may lock with `RLIMIT_MEMLOCK`, and the stock
limits are small: 8 MiB for a default Docker container or systemd service, 64
KiB on some older hosts. A fixed 64 MiB default budget could never be
satisfied there, so an unconfigured node failed at start on exactly the hosts
most deployments use.

## Shape chosen

Two halves, decided by the owner on 2026-09-25 ("derive from limit + ship
caps"):

1. **The binary adapts to the host.** An unconfigured budget is derived from
   the discovered `RLIMIT_MEMLOCK`: the smaller of 64 MiB and the limit less
   2 MiB of headroom for locked memory outside the budget. The figure and its
   source are logged at startup. An explicitly configured budget is the
   operator's choice and is never shrunk: above the limit it fails `start`
   with an error naming the limit and how to raise it.
2. **The deployment artifacts grant the full budget.** The compose files in
   this repository run `qrdbd` with `IPC_LOCK` and an unlimited memlock
   ulimit, the runtime image documents the same run-time flags, and
   [user.md](user.md) gives the systemd settings, so a production deployment
   built from them gets the full 64 MiB rather than a derived fraction.

## Rejected alternatives

- **Keep the fixed 64 MiB default and document the caps.** Rejected: every
  deployment that missed the documentation failed at start, on the most common
  hosts.
- **Install the whole limit as the budget.** Rejected: the headroom is real —
  sealing, Shamir unseal shares and the memguard-backed Noise key all lock
  pages against the same limit outside the budget, and a budget equal to the
  limit leaves them nothing.
- **Warn and continue with whatever fits when an explicit budget is too
  large.** Rejected: an operator who asked for a figure must not silently run
  with a smaller one; it fails closed instead.
- **Set capabilities inside the image.** Not possible: capabilities and
  ulimits are granted by the runtime, not the image.

## Component boundaries

- `cmd/qrdbd` (`resolveSecureMemoryBudget`, `runStart`) owns the derivation
  and the install. The budget belongs to the binary, not to a library.
- `sdk/go/secure` re-exports go-enclave's `DiscoverLockedMemoryLimit`,
  `NewAllocatorWithinRLimit` and `SetAllocator`; go-enclave owns the discovery
  and the ceiling check.
- `Dockerfile`, `.devcontainer/docker-compose.yml`,
  `.devcontainer/devcontainer.json` and `test/integration/api/docker-compose.yml`
  carry the caps.

## Trust boundaries

The budget bounds how much locked memory authenticated callers can drive the
process to hold; the headroom exists because some protected allocations are
not yet admitted against it (see
[../configuration/cli.md](../configuration/cli.md#locked-memory-outside-the-budget)).
Granting `IPC_LOCK` removes the kernel ceiling; the budget remains the bound
that qrdb itself enforces.

## Process inspection hardening

Locked memory keeps key material out of swap; it does not stop another process
running as the same user from reading it. ptrace, and the kernel's
`/proc/<pid>/mem` interface, read every page of a process regardless of mlock
or guard pages, and so does a core file written on a crash. So `qrdbd start`
hardens the process first, before it installs the budget or creates any
protected memory:

| Platform | Mechanism | Effect |
|---|---|---|
| Linux | `prctl(PR_SET_DUMPABLE, 0)` | ptrace attach and `/proc/<pid>/mem` need `CAP_SYS_PTRACE`; `/proc/<pid>/*` is owned by root; no core file |
| macOS | `ptrace(PT_DENY_ATTACH)` | no debugger can attach; a process already being traced is terminated |
| Windows and others | none exists | a documented no-op, logged as a warning |

It is unconditional on Linux and macOS. The one way to leave the process
inspectable is the environment variable `QRDB_SECURE_ALLOW_PROCESS_INSPECTION`,
for attaching a debugger; honouring it logs a warning at every start. It is an
environment variable and not a configuration key on purpose: a setting in a
config file outlives the debugging session it was added for. A value that is
not a boolean fails `start` rather than being read as either answer.

Hardening is process lifecycle, like the budget: it belongs to the binary, not
to a library. `sdk/go/secure.HardenProcess` is exported so an embedding binary
(go-xkms, go-quicbaas, or any composer of `sdk/go/node`) applies the same
posture at its own startup.

What it does not cover: root, or any process holding `CAP_SYS_PTRACE`, can
still read the process — that is the kernel's rule, and a host's Yama
`ptrace_scope` setting may restrict ptrace further but is the host's to set.
Monitoring agents that read `/proc/<pid>/` of other processes as a
non-root user lose access to `qrdbd`'s entries.

## Not in scope

No systemd unit file ships in this repository — installation layouts differ
too much to pick one — so [user.md](user.md) documents the settings a unit
needs. cgroup memory limits are not read: they cap total memory, not locked
memory, and deriving a lock budget from them would invent a number.
