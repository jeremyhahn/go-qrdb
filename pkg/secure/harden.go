// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package secure

import (
	"log/slog"
	"os"
	"runtime"
	"strconv"
)

// AllowProcessInspectionEnv names the environment variable that opts a
// process OUT of HardenProcess, leaving it attachable by a debugger and
// readable through /proc/<pid>/mem by any process running as the same user.
//
// It exists for one purpose — attaching a debugger to a process that holds
// protected memory — and it is deliberately an environment variable rather
// than a configuration key: a setting in a config file outlives the debugging
// session it was added for. The value is parsed with strconv.ParseBool; unset
// or empty means "harden". Any value ParseBool rejects is refused with
// *ProcessInspectionOptOutError rather than read as either answer, because a
// typo in a security opt-out must not silently pick a side.
//
// Every process that honours the opt-out logs a warning naming this variable.
const AllowProcessInspectionEnv = "QRDB_SECURE_ALLOW_PROCESS_INSPECTION"

// denyInspection is the platform mechanism HardenProcess applies. It is a
// variable only so this package's in-process tests can substitute a double: the
// real call changes the test binary itself (a non-dumpable process cannot be
// read by its own test tooling, and on macOS PT_DENY_ATTACH cannot be undone),
// so the real mechanism is exercised from a subprocess instead
// (harden_linux_test.go).
var denyInspection = denyProcessInspection

// HardenProcess stops other processes from reading this one's memory, so that
// the pages pkg/secure keeps mlocked and guard-paged cannot be read around the
// side through a debugger or the kernel's process-memory interface.
//
// mlock keeps protected memory out of swap and guard pages catch an overrun,
// but neither stops a process running as the same user from attaching with
// ptrace or opening /proc/<pid>/mem and reading every enclave directly —
// Yama's ptrace_scope aside, which is a host setting this process does not
// control. HardenProcess closes that route where the platform offers a way:
//
//   - Linux: prctl(PR_SET_DUMPABLE, 0). The process becomes non-dumpable, so
//     ptrace attach and /proc/<pid>/mem require CAP_SYS_PTRACE, /proc/<pid>/*
//     is owned by root, and no core file is written on a crash — a core dump
//     is the same disclosure by another route.
//   - macOS: ptrace(PT_DENY_ATTACH). A debugger can no longer attach. A process
//     that is ALREADY being traced when this runs is terminated by the kernel,
//     which is why the opt-out exists.
//   - Everything else, Windows included: there is no equivalent, and this is a
//     documented no-op that logs a warning saying so.
//
// It is PROCESS lifecycle, not library behaviour: nothing in this repository's
// library code calls it. The binary that owns the process calls it once, at
// startup, before it creates any protected memory — beside installing the
// locked-memory budget (SetAllocator), and for the same reason. cmd/qrdbd's
// start command is the reference wiring.
//
// AllowProcessInspectionEnv opts out: the process is left inspectable and a
// warning is logged. A nil logger uses slog.Default.
//
// Returns *ProcessInspectionOptOutError for an unparseable opt-out value and
// *ProcessHardeningError when the platform refuses the mechanism. Both mean the
// process is NOT hardened, and a caller that holds secrets should refuse to
// continue.
func HardenProcess(logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	optOut, err := processInspectionOptOut()
	if err != nil {
		return err
	}
	if optOut {
		logger.Warn("secure: process inspection hardening DISABLED by environment; "+
			"a debugger or any process running as this user can read protected memory",
			"env", AllowProcessInspectionEnv,
			"mechanism", inspectionMechanism)
		return nil
	}
	if !inspectionHardeningSupported {
		logger.Warn("secure: this platform has no process inspection hardening; "+
			"a debugger or any process running as this user can read protected memory",
			"goos", runtime.GOOS)
		return nil
	}
	if err := denyInspection(); err != nil {
		return &ProcessHardeningError{Mechanism: inspectionMechanism, Err: err}
	}
	return nil
}

// processInspectionOptOut reads AllowProcessInspectionEnv. Unset or empty is
// "no opt-out"; anything strconv.ParseBool rejects is an error.
func processInspectionOptOut() (bool, error) {
	raw, set := os.LookupEnv(AllowProcessInspectionEnv)
	if !set || raw == "" {
		return false, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, &ProcessInspectionOptOutError{Value: raw, Err: err}
	}
	return v, nil
}
