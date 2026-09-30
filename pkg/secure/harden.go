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

	quicraftseal "github.com/jeremyhahn/go-quicraft/pkg/seal"
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
// Marking the process non-dumpable stops NEW attaches; a tracer that attached
// before HardenProcess ran keeps its access. So after the mechanism is applied,
// HardenProcess asks the kernel whether the process is being traced — on
// Linux, TracerPid in /proc/self/status — and refuses to continue if it is. On
// macOS PT_DENY_ATTACH already terminates a traced process.
//
// AllowProcessInspectionEnv opts out: the process is left inspectable, no
// tracer check is made, and a warning is logged. The caller that owns an audit
// trail records the opt-out there too, which is why the outcome is returned.
// In FIPS 140-3 mode (seal.FIPSEnabled, the stack's single FIPS predicate) the
// opt-out is REFUSED rather than honoured: a validated deployment does not run
// inspectable. A nil logger uses slog.Default.
//
// Every error means the process is NOT hardened, and a caller that holds
// secrets should refuse to continue:
//
//   - *ProcessInspectionOptOutError: the opt-out value does not parse;
//   - *ProcessInspectionOptOutRefusedError: the opt-out is set in FIPS mode;
//   - *ProcessHardeningError: the platform refused the mechanism, or the
//     tracer check could not be made;
//   - *ProcessTracedError: a tracer is already attached.
func HardenProcess(logger *slog.Logger) (ProcessHardening, error) {
	if logger == nil {
		logger = slog.Default()
	}
	optOut, err := processInspectionOptOut()
	if err != nil {
		return 0, err
	}
	if optOut {
		if fipsMode() {
			return 0, &ProcessInspectionOptOutRefusedError{Mode: "FIPS 140-3"}
		}
		logger.Warn("secure: process inspection hardening DISABLED by environment; "+
			"a debugger or any process running as this user can read protected memory",
			"env", AllowProcessInspectionEnv,
			"mechanism", inspectionMechanism)
		return ProcessInspectionAllowed, nil
	}
	if !inspectionHardeningSupported {
		logger.Warn("secure: this platform has no process inspection hardening; "+
			"a debugger or any process running as this user can read protected memory",
			"goos", runtime.GOOS)
		return ProcessHardeningUnavailable, nil
	}
	if err := denyInspection(); err != nil {
		return 0, &ProcessHardeningError{Mechanism: inspectionMechanism, Err: err}
	}
	tracer, err := processTracer()
	if err != nil {
		return 0, &ProcessHardeningError{Mechanism: tracerCheckMechanism, Err: err}
	}
	if tracer != 0 {
		return 0, &ProcessTracedError{TracerPID: tracer}
	}
	return ProcessHardened, nil
}

// ProcessHardening is what HardenProcess did to the process.
type ProcessHardening int

const (
	// ProcessHardened: the mechanism is applied and no tracer is attached.
	ProcessHardened ProcessHardening = iota + 1
	// ProcessInspectionAllowed: AllowProcessInspectionEnv was honoured; the
	// process is inspectable, and a caller with an audit trail records it.
	ProcessInspectionAllowed
	// ProcessHardeningUnavailable: the platform has no mechanism.
	ProcessHardeningUnavailable
)

// processHardeningNames maps each outcome to its String form.
var processHardeningNames = map[ProcessHardening]string{
	ProcessHardened:             "hardened",
	ProcessInspectionAllowed:    "inspection-allowed",
	ProcessHardeningUnavailable: "unavailable",
}

// String returns the outcome's name, or "unknown".
func (h ProcessHardening) String() string {
	if name, ok := processHardeningNames[h]; ok {
		return name
	}
	return "unknown"
}

// tracerCheckMechanism names the tracer check in errors.
const tracerCheckMechanism = "tracer check"

// processTracer returns the PID of the process tracing this one, or 0. It is a
// variable so in-process tests can present a tracer; the real lookup is
// platform code (tracerPID).
var processTracer = tracerPID

// fipsMode reports whether the process runs in FIPS 140-3 mode. It is
// quicraftseal.FIPSEnabled, the stack's single FIPS predicate, and a variable
// only so tests can present either answer.
var fipsMode = quicraftseal.FIPSEnabled

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
