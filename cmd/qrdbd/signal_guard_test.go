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
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jeremyhahn/go-enclave"

	secure "github.com/jeremyhahn/go-qrdb/sdk/go/secure"
)

// Many tests in this package drive runStart's shutdown path by sending a
// signal to their own process. That is only safe while some handler is
// registered: with none, SIGINT, SIGTERM and SIGHUP keep their default
// disposition and terminate the whole test binary, taking every remaining
// test with them and reporting nothing more useful than "signal: interrupt".
//
// Two things made that reachable. A test whose handler is installed by the
// code under test races the signal it sends -- on a slow runner the signal
// wins. And every one of those tests ends with signal.Reset, which re-arms the
// default for whatever comes next, including a goroutine leaked from the test
// that just finished. CI hit both: run 363 died on a SIGTERM mid-startup, and
// run 375 on a SIGINT delivered while an unrelated later test was running.
//
// signalGuard keeps a registration alive for the lifetime of the test binary
// so no delivery can be fatal, and resetSignalHandlers re-arms it immediately
// after any deliberate reset. Handlers are additive, so the code under test
// still observes everything it registers for.
//
// The trade-off is that Ctrl-C no longer interrupts a local `go test` run for
// this package; the test binary's own timeout still applies.
var signalGuard = make(chan os.Signal, 8)

// armSignalGuard registers the guard for the signals these tests deliver.
// signal.Notify is idempotent for a channel/signal pair, so calling it again
// after a reset simply restores the registration.
func armSignalGuard() {
	signal.Notify(signalGuard, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
}

// resetSignalHandlers re-arms the guard for sigs. Use it in place of a bare
// signal.Reset, which would leave the default disposition armed with nothing
// to restore it.
//
// It used to also call signal.Reset(sigs...) first, to drop a handler a test
// or the code under test left behind. signal.Reset(sig...) removes EVERY
// channel registered for the named signals, guard included, and
// unconditionally disables the OS-level handler for them before the
// following armSignalGuard() call re-enables it -- a real window, between
// those two calls, in which a signal has no handler at all and the OS
// default disposition kills the whole test binary. signalAfterReady and
// stopDaemon's own retry loops make that window reachable: a SIGTERM they
// already sent can still be in flight when a test's cleanup calls this
// function, and landing in the window is what "signal: terminated" with no
// failing assertion looked like (go-qrdb#442).
//
// Dropping the Reset call removes the window rather than narrowing it: this
// function no longer calls anything that can disable a signal for every
// registered channel, so there is no instant at which none is registered.
// It is also not a loss of the old behaviour's purpose. Every production
// signal.Notify call site pairs itself with its own signal.Stop
// (signal.NotifyContext's stop function, or a deferred signal.Stop next to a
// raw Notify -- see start.go), so a leaked handler from the code under test
// is not this function's problem to clean up; and signal.Notify's delivery
// to a channel nobody is reading from any more is a non-blocking send that
// is silently dropped, so a stale test channel some earlier revision left
// registered was always inert, never a correctness hazard.
func resetSignalHandlers(sigs ...os.Signal) {
	armSignalGuard()
}

// signalRetryInterval paces the re-delivery in signalUntilReturn. It bounds
// only how soon a lost delivery is retried, never how long the helper waits:
// the loop ends when runStart returns, whatever that takes.
const signalRetryInterval = 25 * time.Millisecond

// signalUntilReturn drives a runStart goroutine to shutdown and returns its
// error, with no assumption about how long startup takes.
//
// runStart installs its SIGINT/SIGTERM handler partway through startup, after
// TLS resolution and database-manager setup. A test that sleeps for a fixed
// duration and then signals once is betting that startup reaches
// signal.NotifyContext within that sleep. When the bet loses -- which is what
// a loaded runner makes likely -- signalGuard absorbs the delivery, runStart
// never observes a cancellation, and the test waits for a shutdown that can no
// longer happen. Sleeping longer only lengthens the odds.
//
// Re-delivering until runStart returns removes the bet entirely. Whichever
// delivery lands after the handler is installed cancels sigCtx, and the
// synchronisation point is the value on done -- the real completion signal --
// rather than an elapsed duration. Deliveries before installation are absorbed
// by signalGuard, exactly as they are today, and deliveries after are received
// by an already-cancelled context, which is a no-op.
//
// The retry is bounded by the test's own deadline rather than by a fixed
// duration, so a runStart that genuinely hangs fails THIS test with a
// diagnostic instead of running the package into its global timeout and taking
// every other test's result with it. The bound is never a bet on startup
// duration: it is whatever the run was already going to allow, less a margin
// for the rest of the package.
//
// It signals from t=0, which is correct for a caller that only wants runStart
// to RETURN. A caller that needs startup to have reached a particular state
// first must use signalAfterReady -- see the warning there.
func signalUntilReturn(t *testing.T, done <-chan error, sig syscall.Signal) error {
	t.Helper()
	return signalAfterReady(t, done, sig, nil)
}

// signalDeadlineMargin is left for the remainder of the package when deriving
// a per-test bound from t.Deadline().
const signalDeadlineMargin = 30 * time.Second

// signalAfterReady waits until ready reports the daemon has reached the state
// the test means to exercise, then delivers sig until runStart returns.
//
// WHY THE GATE EXISTS, because removing it is an easy and invisible mistake.
// runStart installs its signal handler EARLY -- before the seal build,
// awaitUnseal, initializeNode, bootstrapAuth and warmup. Signalling at t=0
// therefore cancels it before most of startup has run, and the tests still
// PASS, because they accept any error from runStart via t.Logf. What they stop
// doing is testing anything: measured on the _FullStartup family, runFirstBoot
// coverage fell 60.6% -> 6.1% and adminExists and waitForAdminReplication both
// fell to 0%, while PACKAGE coverage did not move at all because other tests
// happen to cover the same functions. A test named _FullStartup that no longer
// reaches first boot is worse than a missing test: it reports success for work
// it never did (go-qrdb#83).
//
// A nil ready signals immediately, which is right only when the assertion is
// purely "a signalled runStart returns".
//
// The gate ASSERTS. Not becoming ready fails the test, and so does runStart
// returning before it does. An earlier revision capped the wait at five
// seconds and signalled anyway, on the reasoning that an early cap "costs
// coverage at worst, never a hang" -- but losing the coverage silently is
// precisely the defect this helper exists to remove, and instrumenting the
// predicate showed five of the six migrated tests hit that cap on EVERY run
// and never became ready at all. A wait whose cap is always reached is a
// time.Sleep wearing a predicate's clothes. What they were waiting for could
// not happen: a state machine that could not apply kv.OpPutIfAbsent meant
// first boot never finished, which is go-qrdb#86, fixed separately. With that
// fixed the listener accepts in about five seconds and the gate is an
// assertion again.
//
// The bound is the test's own deadline less a margin for the rest of the
// package, never a guess at how long startup takes: whatever the run was
// already going to allow.
func signalAfterReady(t *testing.T, done <-chan error, sig syscall.Signal, ready func() bool) error {
	t.Helper()

	limit := readyDeadline(t)

	if ready != nil {
		awaitReady(t, done, ready, limit)
	}

	ticker := time.NewTicker(signalRetryInterval)
	defer ticker.Stop()

	for {
		if err := syscall.Kill(os.Getpid(), sig); err != nil {
			t.Fatalf("Kill(%v) error = %v", sig, err)
		}
		select {
		case runErr := <-done:
			return runErr
		case <-ticker.C:
		}
		if time.Now().After(limit) {
			t.Fatalf("runStart did not return within the test deadline after repeated %v", sig)
		}
	}
}

// stopDaemon drives a running runStart to return, bounded, and reports
// whether it did.
//
// It calls no testing methods, so it is safe from a cleanup function or a
// failure path. That matters because t.Fatalf ends only the calling goroutine:
// a helper that fails while runStart is still running leaves a daemon owning
// listeners and PROCESS-WIDE signal registrations, free to consume the signals
// a later test sends. That is the same class of cross-test interference this
// file was written to remove, reintroduced through the failure path
// (go-qrdb#83).
func stopDaemon(done <-chan error, limit time.Time) bool {
	select {
	case <-done:
		return true
	default:
	}

	ticker := time.NewTicker(signalRetryInterval)
	defer ticker.Stop()
	for {
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		select {
		case <-done:
			return true
		case <-ticker.C:
		}
		if time.Now().After(limit) {
			return false
		}
	}
}

// guardedDaemons records the (test, done) pairs that already carry a shutdown
// guard.
//
// The guard is registered by the readiness helpers, and a test that launches a
// daemon may also register it at the launch site so the invariant is visible
// where the goroutine is created rather than as a side effect three calls
// away. Both are wanted, so registering twice has to be harmless — and without
// this it is not. Two cleanups would each call stopDaemon; the first drains
// done, and the second then signals a daemon that is already gone until
// shutdownGuardBound expires and reports a failure that is entirely its own.
// That is the same trap the t.Failed check below exists for, reached by a
// different door.
var guardedDaemons = struct {
	sync.Mutex
	// keys maps each guarded daemon to whether its result has been received.
	keys map[daemonGuardKey]bool
}{keys: make(map[daemonGuardKey]bool)}

// daemonGuardKey identifies one guarded daemon. Both fields are comparable, so
// the pair is usable as a map key directly.
type daemonGuardKey struct {
	t    *testing.T
	done <-chan error
}

// claimDaemonGuard reports whether this call is the one that owns the guard
// for done, releasing the claim when the test ends.
func claimDaemonGuard(t *testing.T, done <-chan error) bool {
	key := daemonGuardKey{t: t, done: done}

	guardedDaemons.Lock()
	defer guardedDaemons.Unlock()

	if _, claimed := guardedDaemons.keys[key]; claimed {
		return false
	}
	guardedDaemons.keys[key] = false
	t.Cleanup(func() {
		guardedDaemons.Lock()
		defer guardedDaemons.Unlock()
		delete(guardedDaemons.keys, key)
	})
	return true
}

// daemonReturned records that the guarded daemon's result has been received
// from done, so its guard does not wait for a return that has already happened.
func daemonReturned(t *testing.T, done <-chan error) {
	key := daemonGuardKey{t: t, done: done}

	guardedDaemons.Lock()
	defer guardedDaemons.Unlock()

	if _, claimed := guardedDaemons.keys[key]; claimed {
		guardedDaemons.keys[key] = true
	}
}

// daemonHasReturned reports whether daemonReturned recorded the daemon's result.
func daemonHasReturned(t *testing.T, done <-chan error) bool {
	guardedDaemons.Lock()
	defer guardedDaemons.Unlock()
	return guardedDaemons.keys[daemonGuardKey{t: t, done: done}]
}

// guardDaemonShutdown registers a cleanup that stops runStart if the test did
// not, and restores the signal handlers either way.
//
// Registered by the readiness helpers rather than left to each caller: the
// paths that need it most are the ones that fail, and a failing test does not
// reach its own cleanup code. Callers may also register it explicitly; a
// repeat registration for the same daemon is a no-op.
func guardDaemonShutdown(t *testing.T, done <-chan error) {
	t.Helper()
	if !claimDaemonGuard(t, done) {
		return
	}
	t.Cleanup(func() {
		// Runs on the test goroutine, so reporting here is safe.
		defer resetSignalHandlers(syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

		// Only a FAILED test can have left the daemon running. The normal
		// paths return only once runStart has returned — and in doing so they
		// consume the single value on done, so the guard cannot observe it a
		// second time. Checking done here on a passing test therefore finds an
		// empty channel and signals a daemon that stopped long ago, waiting
		// out the bound before reporting a failure that is entirely its own
		// (measured: this cleanup failed every passing run until it asked
		// t.Failed first).
		if !t.Failed() || daemonHasReturned(t, done) {
			return
		}
		if !stopDaemon(done, time.Now().Add(shutdownGuardBound)) {
			t.Errorf("runStart did not return within %v of the test failing; it "+
				"still owns its listeners and signal registrations",
				shutdownGuardBound)
		}
	})
}

// A daemon is guarded once, however many times the guard is registered.
//
// The consequence this pins is not cosmetic. Every extra registration is
// another cleanup calling stopDaemon on the same channel, and only the first
// can receive from it: the rest signal a process whose daemon has already
// returned until shutdownGuardBound expires, then fail the test for a leak
// they invented. Deduplication is what lets a test state the guard at its
// launch site while the readiness helpers keep registering it for the tests
// that do not.
func TestClaimDaemonGuard_OnlyTheFirstClaimOwnsTheDaemon(t *testing.T) {
	first := make(chan error, 1)

	if !claimDaemonGuard(t, first) {
		t.Fatal("the first claim on a daemon did not take ownership")
	}
	if claimDaemonGuard(t, first) {
		t.Error("a second claim on the same daemon took ownership as well: " +
			"the daemon would be stopped twice")
	}
	if claimDaemonGuard(t, first) {
		t.Error("a third claim on the same daemon took ownership as well")
	}

	// Keyed on the daemon, not on the test: one test may launch more than one
	// runStart, and each needs its own guard.
	second := make(chan error, 1)
	if !claimDaemonGuard(t, second) {
		t.Error("a second daemon in the same test was refused a guard, so " +
			"nothing would stop it when the test fails")
	}
}

// readinessTimeout bounds a wait for something the daemon should do promptly
// once it is serving. Long enough that a loaded runner never trips it, short
// enough that a defect reports rather than being waited out.
const readinessTimeout = 30 * time.Second

// shutdownGuardBound is how long the cleanup waits for a daemon the test left
// running. Generous, because it only runs when something has already gone
// wrong, and bounded so a stuck daemon reports rather than hanging the package.
const shutdownGuardBound = 30 * time.Second

// awaitReady blocks until ready reports the daemon has reached the state the
// caller means to exercise, failing if it never does or if runStart exits
// first.
//
// Separate from signalAfterReady because an INTERMEDIATE signal needs the wait
// without the shutdown. SIGHUP is the case: it must reach runStart's own
// handler, which is installed only after initializeNode completes, but
// delivering it must not end the daemon — the test goes on to shut down with
// SIGTERM afterwards. Before this existed, the SIGHUP test slept three seconds
// and hoped, so on a slow runner the signal arrived before the handler did,
// the test's own notify channel absorbed it, and handleSIGHUP was never
// exercised. The test passed having covered nothing on the reload path, which
// is the exact failure this file exists to remove (go-qrdb#83).
func awaitReady(t *testing.T, done <-chan error, ready func() bool, limit time.Time) {
	t.Helper()
	if exited := waitReady(t, done, ready, limit); exited != nil {
		// Not a condition to signal into: a runStart that exits during
		// startup has failed, and reporting its error to a caller that
		// treats any error as acceptable is how a daemon that never
		// started passed as a full-startup test for months.
		t.Fatalf("%v", exited)
	}
}

// waitReady is awaitReady for a caller that decides what an early return
// means: it returns nil once ready reports true, and the daemon's result if
// runStart returns first. A daemon that is still not ready at limit is stopped
// and fails the test, as in awaitReady.
//
// An early return consumes runStart's single result from done, so waitReady
// records it as returned: the shutdown guard would otherwise find done empty
// on a failed test and spend its whole bound signalling a daemon that stopped
// long ago, then report that it never did (run 51450 did exactly that).
func waitReady(t *testing.T, done <-chan error, ready func() bool, limit time.Time) *daemonExitedError {
	t.Helper()

	// However this test ends -- including on the Fatalf below, which ends only
	// this goroutine -- the daemon is stopped and the handlers restored.
	guardDaemonShutdown(t, done)

	started := time.Now()
	poll := time.NewTicker(signalRetryInterval)
	defer poll.Stop()
	for !ready() {
		if time.Now().After(limit) {
			// Shut the daemon down BEFORE failing. Fatalf ends this goroutine
			// only, and a daemon left running consumes later tests' signals.
			stopped := stopDaemon(done, time.Now().Add(shutdownGuardBound))
			t.Fatalf("startup never became ready within %v (daemon stopped: %v): "+
				"the test claims to exercise a fully started daemon and never "+
				"reached one", time.Since(started), stopped)
		}
		select {
		case runErr := <-done:
			daemonReturned(t, done)
			return &daemonExitedError{After: time.Since(started), Err: runErr}
		case <-poll.C:
		}
	}
	return nil
}

// readyDeadline is the bound awaitReady and signalAfterReady share: the test's
// own deadline less a margin for the rest of the package, never a guess at how
// long startup takes.
func readyDeadline(t *testing.T) time.Time {
	t.Helper()
	limit := time.Now().Add(2 * time.Minute)
	if d, ok := t.Deadline(); ok {
		if adjusted := d.Add(-signalDeadlineMargin); adjusted.After(time.Now()) {
			limit = adjusted
		}
	}
	return limit
}

// listenerReady reports whether addr accepts a TCP connection, which is the
// observable proof that runStart has finished startup and is serving -- the
// state the _FullStartup tests are named for.
func listenerReady(addr string) func() bool {
	return func() bool {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}
}

func TestMain(m *testing.M) {
	armSignalGuard()
	restoreSecureLifecycle := installTestSecureLifecycle()
	defer restoreSecureLifecycle()
	releaseNoisePort := holdDefaultNoisePort()
	defer releaseNoisePort()
	os.Exit(m.Run())
}

// installTestSecureLifecycle replaces runStart's three go-enclave lifecycle
// seams (newSecureAllocator, setSecureAllocator, wipeSecureMemory) with
// doubles safe to call an unbounded number of times in one process, and
// returns a restore function.
//
// Why the real functions cannot be left wired for this binary: this package
// calls runStart directly, and drives it through cmd.Execute(), FAR more than
// once per run (every TestRunStart_*, every *_FullStartup test, every test
// exercising newStartCmdWithFlags's RunE). secure.SetAllocator can be
// satisfied only once per process, and secure.WipeAll permanently destroys
// the process Sealer's keys the moment ANY test's runStart reaches shutdown —
// so the second test to call SetAllocator, or the first to reach WipeAll,
// would fail or poison every seal-using test after it. Measured, not
// theorized: wiring the real functions here produced exactly that cascade.
//
// newSecureAllocator still returns a REAL, functioning *secure.Allocator
// (via enclave.NewAllocator, so a test asserting on a bad budget still gets a
// real rejection) — only the process-wide INSTALLATION and the destructive
// SWEEP are doubled, since those are the two operations this binary cannot
// repeat safely.
//
// enclave.NewAllocator, not secure.NewAllocatorWithinRLimit: the RLimit-aware
// constructor refuses a budget above the HOST's discovered RLIMIT_MEMLOCK
// ceiling, which makes this test binary's default 64 MiB budget fail on any
// runner whose ceiling is lower — common in a constrained or sandboxed CI
// worker — before a single test under it even runs (go-qrdb#114 review,
// round 2: this doubling was host-dependent for exactly that reason).
// enclave.NewAllocator validates only that the requested size is positive, so
// it is a real allocator with a real rejection path, without making every
// test in this binary depend on the host's memlock limit.
// TestRunStart_SecureMemoryBudgetRejected, which specifically wants the
// RLIMIT-aware rejection, overrides newSecureAllocator itself via
// swapSecureLifecycle and does not rely on this default.
func installTestSecureLifecycle() (restore func()) {
	prevHarden, prevNew, prevSet, prevWipe, prevDiscover := hardenSecureProcess, newSecureAllocator, setSecureAllocator, wipeSecureMemory, discoverLockedMemoryLimit
	// Process hardening cannot be undone: a non-dumpable test binary stays so
	// for every later test. secure_lifecycle_test.go pins that runStart calls
	// it; pkg/secure proves the mechanism from a subprocess.
	hardenSecureProcess = func(*slog.Logger) (secure.ProcessHardening, error) { return secure.ProcessHardened, nil }
	newSecureAllocator = enclave.NewAllocator
	setSecureAllocator = func(*secure.Allocator) error { return nil }
	wipeSecureMemory = func(context.Context) (int, error) { return 0, nil }
	// An unconfigured budget is derived from RLIMIT_MEMLOCK; reporting it
	// unlimited keeps every runStart test in this binary off the host's own
	// limit, for the same reason newSecureAllocator is not the RLIMIT-aware
	// constructor. secure_budget_test.go presents specific limits itself.
	discoverLockedMemoryLimit = func() (secure.LockedMemoryLimit, error) {
		return secure.LockedMemoryLimit{Source: secure.LimitUnlimited}, nil
	}
	return func() {
		hardenSecureProcess, newSecureAllocator, setSecureAllocator, wipeSecureMemory, discoverLockedMemoryLimit = prevHarden, prevNew, prevSet, prevWipe, prevDiscover
	}
}
