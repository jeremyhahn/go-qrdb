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

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-25

What a host must grant `qrdbd` to run as configured: today, locked memory.
Ports, TLS and cluster formation are covered by
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

## Not in scope

No systemd unit file ships in this repository — installation layouts differ
too much to pick one — so [user.md](user.md) documents the settings a unit
needs. cgroup memory limits are not read: they cap total memory, not locked
memory, and deriving a lock budget from them would invent a number.
