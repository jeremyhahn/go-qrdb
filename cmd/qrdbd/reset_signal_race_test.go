// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// go-qrdb#442.
//
// resetSignalHandlers used to pair signal.Reset(sigs...) with armSignalGuard()
// to drop a test's own handler and restore the guard. signal.Reset removes
// EVERY channel registered for the named signals -- guard included -- and
// unconditionally reverts the OS disposition to default before the following
// armSignalGuard() call re-registers it. That is a real window: a SIGTERM
// already in flight from a concurrent retry loop (exactly what
// signalAfterReady and stopDaemon run) can be delivered while the process has
// no handler at all, and the OS default disposition kills the whole test
// binary with "signal: terminated" and no failing assertion.
//
// This is proven with process-boundary fidelity, the same way
// pkg/seal/encfs/pebble_sigkill_test.go proves a SIGKILL survives a crash:
// re-exec this binary as a subprocess that hammers SIGTERM against its own
// pid while calling resetSignalHandlers back-to-back, and assert the
// subprocess exits cleanly rather than being killed by a signal. Run against
// the pre-fix resetSignalHandlers (signal.Reset then armSignalGuard as two
// separate calls) this test reliably fails; against the fix (armSignalGuard
// only, never disabling the guard's own registration) it cannot fail by
// construction, because no code path in the fixed function ever calls
// anything that disables the signal for every registered channel.

// resetRaceHelperEnvVar gates TestHelperProcess_ResetSignalHandlersRace so it
// only runs the race scenario when re-exec'd by
// TestResetSignalHandlers_NoUnhandledSignalWindow; a normal `go test` run
// skips it immediately.
const resetRaceHelperEnvVar = "QRDB_RESET_SIGNAL_RACE_HELPER"

// resetRaceIterations is the number of back-to-back resetSignalHandlers
// calls the helper races against a concurrent SIGTERM-delivery loop. The
// pre-fix window is on the order of a couple of runtime instructions
// (sigdisable, then sigenable), so a single iteration only rarely lands in
// it -- this count was measured against the pre-fix code to fail reliably
// (every run, in local testing) while keeping the whole test under a second.
const resetRaceIterations = 20000

// TestHelperProcess_ResetSignalHandlersRace is not a real test: it is
// re-exec'd as a subprocess by
// TestResetSignalHandlers_NoUnhandledSignalWindow. It races a tight
// SIGTERM-delivery loop against resetRaceIterations calls to
// resetSignalHandlers, exactly as the retry loops in signalAfterReady and
// stopDaemon race a test's own cleanup in production use.
func TestHelperProcess_ResetSignalHandlersRace(t *testing.T) {
	if os.Getenv(resetRaceHelperEnvVar) != "1" {
		t.Skip("helper process; only runs when re-exec'd by TestResetSignalHandlers_NoUnhandledSignalWindow")
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				// A discarded error here would let a runner whose security
				// profile denies a self-directed signal pass this test
				// vacuously: the hammering loop would spin sending nothing,
				// resetSignalHandlers would never race an in-flight SIGTERM,
				// and the helper would exit cleanly for a reason that proves
				// nothing about the fix. t.Errorf is safe to call from a
				// goroutine other than the test's own (unlike Fatalf); Fatalf
				// itself is reserved for the test goroutine below.
				if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
					t.Errorf("syscall.Kill(self, SIGTERM) failed, the race was never exercised: %v", err)
					return
				}
			}
		}
	}()

	for i := 0; i < resetRaceIterations; i++ {
		resetSignalHandlers(syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	}
	close(stop)
	<-done

	// Give a SIGTERM already in flight when the loop above ended time to be
	// delivered before this process exits cleanly, so a would-be crash
	// cannot lose a race against os.Exit itself.
	time.Sleep(100 * time.Millisecond)
}

// TestResetSignalHandlers_NoUnhandledSignalWindow re-execs this test binary
// and asserts the helper above exits cleanly rather than being killed by a
// signal -- the regression test for go-qrdb#442.
func TestResetSignalHandlers_NoUnhandledSignalWindow(t *testing.T) {
	// #nosec G204 -- re-exec of this test binary: argv[0] is os.Args[0], the
	// path `go test` invoked, and every other argument is a compile-time
	// literal. No shell is involved and nothing from outside the process
	// reaches this argv.
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess_ResetSignalHandlersRace$", "-test.v")
	cmd.Env = append(os.Environ(), resetRaceHelperEnvVar+"=1")
	out, runErr := cmd.CombinedOutput()
	t.Logf("helper process output:\n%s", out)

	if runErr == nil {
		return
	}

	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) {
		t.Fatalf("helper process failed to start: %v", runErr)
	}
	if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		t.Fatalf("helper process was killed by %v: resetSignalHandlers left a window with no signal handler installed (go-qrdb#442)", ws.Signal())
	}
	t.Fatalf("helper process exited with an unexpected error: %v", runErr)
}
