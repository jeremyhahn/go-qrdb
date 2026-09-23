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

# Deployment — Engineering

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-25

## Exported and package surface

- `sdk/go/secure.DiscoverLockedMemoryLimit() (LockedMemoryLimit, error)`,
  with `LockedMemoryLimit`, `LimitSource` and the `LimitRLimit`,
  `LimitUnlimited`, `LimitNotEnforced`, `LimitUnsupported`, `LimitUnknown`
  values — re-exports of go-enclave.
- `cmd/qrdbd.resolveSecureMemoryBudget(configuredMiB int64)
  (secureMemoryBudget, error)` — the derivation. Constants
  `secureMemoryHeadroomBytes` (2 MiB) and `minSecureMemoryBudgetBytes`
  (1 MiB); the ceiling is `pkg/config.DefaultSecureMemoryBudgetMiB` (64).

## Derivation

| Configured | Discovered limit | Budget | `source` |
|---|---|---|---|
| `> 0` | not read here | configured MiB, checked by `NewAllocatorWithinRLimit` | `configured` |
| `0` | unlimited / `CAP_IPC_LOCK` / unsupported | 64 MiB | `default` |
| `0` | bounded, `limit − 2 MiB ≥ 64 MiB` | 64 MiB | `default` |
| `0` | bounded, `1 MiB ≤ limit − 2 MiB < 64 MiB` | `limit − 2 MiB`, rounded down to whole pages | `derived from RLIMIT_MEMLOCK` |
| `0` | bounded, `limit − 2 MiB < 1 MiB` | — fails closed | — |
| `0` | unreadable | — fails closed | — |
| `< 0` or overflows MiB→bytes | not read | — fails closed | — |

`pkg/config.DefaultConfig()` leaves `server.secure_memory_budget_mib` at 0 so
a configured value is always distinguishable from the default.

## Error semantics (all fail closed, before any other startup work)

- `*SecureMemoryBudgetError{BudgetMiB, Err}` wraps every failure. `BudgetMiB`
  is 0 for a derivation failure. Its message appends how to raise the host
  limit whenever `Err` is `enclave.ErrBudgetExceedsRLimit` or a
  `*SecureMemoryLimitTooSmallError`.
- `*SecureMemoryLimitTooSmallError{LimitBytes, HeadroomBytes, MinimumBytes}`
  — the limit leaves less than the minimum after the headroom.
- `enclave.ErrBudgetExceedsRLimit` (`*RLimitError`) — an explicit budget
  above a binding limit.
- `enclave.ErrLimitDiscoveryFailed` (`*LimitDiscoveryError`) — the limit
  could not be read.
- `*SecureMemoryBudgetMismatchError` — an allocator was already installed
  with a different figure.

## Observability

One `INFO` log event per successful start: `secure memory budget installed`
with `budget_bytes`, `source`, `rlimit_memlock_source`,
`rlimit_memlock_bytes`.

## Test strategy

Unit tests in `cmd/qrdbd/secure_budget_test.go` present limits through the
`discoverLockedMemoryLimit` seam, never by changing the test process's own
`RLIMIT_MEMLOCK`: unlimited, `CAP_IPC_LOCK`, stock 8 MiB, exactly default plus
headroom, large, unaligned, tiny (64 KiB and the boundary values), and
unreadable; explicit within-limit and explicit over-limit through `runStart`.
`TestMain` presents an unlimited limit to the rest of the binary so no test
depends on the host.
