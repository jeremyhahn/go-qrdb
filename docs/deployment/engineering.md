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

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

## Exported and package surface

- `sdk/go/secure.DiscoverLockedMemoryLimit() (LockedMemoryLimit, error)`,
  with `LockedMemoryLimit`, `LimitSource` and the `LimitRLimit`,
  `LimitUnlimited`, `LimitNotEnforced`, `LimitUnsupported`, `LimitUnknown`
  values — re-exports of go-enclave.
- `cmd/qrdbd.resolveSecureMemoryBudget(configuredMiB int64)
  (secureMemoryBudget, error)` — the derivation. Constants
  `secureMemoryHeadroomBytes` (2 MiB) and `minSecureMemoryBudgetBytes`
  (1 MiB); the ceiling is `pkg/config.DefaultSecureMemoryBudgetMiB` (64).

- `sdk/go/secure.HardenProcess(*slog.Logger) (ProcessHardening, error)`,
  re-exporting `pkg/secure.HardenProcess`, with the outcomes
  `ProcessHardened`, `ProcessInspectionAllowed` and
  `ProcessHardeningUnavailable`, the constant `AllowProcessInspectionEnv`
  (`QRDB_SECURE_ALLOW_PROCESS_INSPECTION`), and the errors
  `*ProcessHardeningError{Mechanism, Err}`,
  `*ProcessInspectionOptOutError{Value, Err}`,
  `*ProcessInspectionOptOutRefusedError{Mode}` and
  `*ProcessTracedError{TracerPID}`. Platform files
  `pkg/secure/harden_linux.go`, `harden_darwin.go` and `harden_other.go`
  (`!linux && !darwin`) each supply the mechanism and its name.
- `cmd/qrdbd`'s start command calls it as the first statement of its RunE,
  through the `hardenSecureProcess` seam, before `resolveStartFlags` loads any
  configuration. The outcome travels in `startFlags.processHardening`;
  `runStart` passes it to `auditInspectionOptOut` once the compliance audit
  trail is built.
- `cmd/qrdb.run` calls it through `hardenAtStartup(os.Stderr)` (the
  `hardenProcess` seam) after the logger is initialised and before the
  GUI/CLI branch; a refusal exits 1.

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
- `*ProcessHardeningError` — the platform refused the hardening mechanism.
  `start` fails; the process is not hardened.
- `*ProcessInspectionOptOutError` — `QRDB_SECURE_ALLOW_PROCESS_INSPECTION`
  holds a value `strconv.ParseBool` rejects. `start` fails before anything is
  applied: a malformed security opt-out is not read as either answer.
- `*ProcessInspectionOptOutRefusedError` — the opt-out is set in FIPS 140-3
  mode. `start` fails.
- `*ProcessTracedError` — a tracer is attached (`TracerPid` is not 0). A
  `TracerPid` that cannot be read or parsed is a `*ProcessHardeningError`
  (mechanism `tracer check`), never "not traced". `start` fails.
- `*InspectionOptOutAuditError` — the opt-out was honoured but the compliance
  audit trail refused the record of it. `start` fails.

## Observability

One `INFO` log event per successful start: `secure memory budget installed`
with `budget_bytes`, `source`, `rlimit_memlock_source`,
`rlimit_memlock_bytes`.

One compliance audit event when the opt-out is honoured: actor `qrdbd`,
resource `process`, action `disable-inspection-hardening`, outcome `success`,
attribute `env` naming the variable.

One `WARN` event when the process is left inspectable: `secure: process
inspection hardening DISABLED by environment` with `env` and `mechanism` when
the opt-out is honoured, or `secure: this platform has no process inspection
hardening` with `goos` on a platform without a mechanism. A hardened start
logs nothing for it.

## Test strategy

Unit tests in `cmd/qrdbd/secure_budget_test.go` present limits through the
`discoverLockedMemoryLimit` seam, never by changing the test process's own
`RLIMIT_MEMLOCK`: unlimited, `CAP_IPC_LOCK`, stock 8 MiB, exactly default plus
headroom, large, unaligned, tiny (64 KiB and the boundary values), and
unreadable; explicit within-limit and explicit over-limit through `runStart`.
`TestMain` presents an unlimited limit to the rest of the binary so no test
depends on the host.

Process hardening cannot be undone, so no test applies it to its own test
binary. `pkg/secure/harden_test.go` drives `HardenProcess`'s decisions against
a double of the platform mechanism (applied when unset or false, skipped and
warned when true, refused when malformed, a platform refusal surfaced typed).
`pkg/secure/harden_linux_test.go` proves the real mechanism across three
processes: a reader that drops `CAP_SYS_PTRACE` (CI containers run as root,
and the capability bypasses the check) starts a target as its child (so Yama's
default `ptrace_scope=1` admits it), and the target reports `PR_GET_DUMPABLE`
and the address of a random marker. The opt-out run is the positive control —
the reader opens `/proc/<pid>/mem` and reads the marker back — and the
hardened run must refuse the open with `PR_GET_DUMPABLE == 0`.
`cmd/qrdbd`'s `TestMain` doubles `hardenSecureProcess`, and
`harden_start_test.go` pins that the production seam is
`secure.HardenProcess`, that a refusal fails `start` before any configuration
is read (a missing config file would otherwise be the error) or any budget is
built, that the outcome reaches `runStart`, and that `auditInspectionOptOut`
records exactly one event for an honoured opt-out, none otherwise, and fails
start when the sink refuses it. `pkg/secure/harden_test.go` presents a tracer,
a failed tracer lookup and FIPS mode through seams, and
`harden_tracer_linux_test.go` parses every shape of the status line. `cmd/qrdb/harden_test.go` pins the same seam, that a refusal makes `run`
exit 1, and that the opt-out warning goes to the writer it is given rather than
standard output.

## Integration Target

Run `make integration-test-deployment` for this feature. The target intentionally selects zero Go integration tests today because locked-memory enforcement is a host/kernel property; the gap is recorded in `test/integration/COVERAGE.md` under `deployment`.
