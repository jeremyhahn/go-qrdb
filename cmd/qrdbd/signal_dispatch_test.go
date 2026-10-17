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
	"errors"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// starveSignalDispatch pins the test binary to one P for the rest of t, so
// the os/signal goroutine a self-sent signal wakes cannot run until the test
// goroutine blocks. That turns "the dispatcher has not got to it yet" -- a
// scheduling accident on a loaded runner -- into the state every assertion
// below starts from.
func starveSignalDispatch(t *testing.T) {
	t.Helper()
	prev := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(prev) })
}

// staleSignalWindow bounds how long assertNoStaleSignal listens. A signal
// already raised reaches a registered channel within microseconds of the
// dispatcher getting a P; this is generous on a loaded runner.
const staleSignalWindow = 4 * signalRetryInterval

// assertNoStaleSignal registers a handler the way the next runStart does and
// reports a signal that reaches it although it was sent before it existed.
func assertNoStaleSignal(t *testing.T, sig os.Signal) {
	t.Helper()
	next := make(chan os.Signal, 1)
	signal.Notify(next, sig)
	defer signal.Stop(next)

	// Blocking here is what lets a starved dispatcher run.
	timer := time.NewTimer(staleSignalWindow)
	defer timer.Stop()
	select {
	case s := <-next:
		t.Fatalf("a handler registered after the daemon returned received %v, "+
			"sent to that daemon: the next runStart would shut down on it", s)
	case <-timer.C:
	}
}

// TestSignalUntilReturn_NoSignalOutlivesTheDaemon is CI run 55245 reduced to
// its mechanism. runStart's deferred sigStop deregisters its handler before
// runStart returns, and the retry loop can send one more SIGTERM in that gap.
// That signal is still undelivered when the loop sees done; whatever
// registers next -- the following test's runStart -- receives it and ends
// its unseal wait at once.
func TestSignalUntilReturn_NoSignalOutlivesTheDaemon(t *testing.T) {
	starveSignalDispatch(t)

	// A daemon that has deregistered and returned before the first signal.
	done := make(chan error, 1)
	done <- nil
	if err := signalUntilReturn(t, done, syscall.SIGTERM); err != nil {
		t.Fatalf("signalUntilReturn() = %v, want the daemon's nil", err)
	}

	assertNoStaleSignal(t, syscall.SIGTERM)
}

// TestStopDaemon_NoSignalOutlivesTheDaemon drives stopDaemon against a double
// of runStart's shutdown: a signal.NotifyContext registration, released by
// its stop function before the daemon returns, as runStart's deferred sigStop
// is. The daemon lingers past a retry, so stopDaemon signals it after it has
// deregistered.
func TestStopDaemon_NoSignalOutlivesTheDaemon(t *testing.T) {
	starveSignalDispatch(t)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	done := make(chan error, 1)
	go func() {
		<-ctx.Done()
		stop()
		time.Sleep(2 * signalRetryInterval)
		done <- nil
	}()

	if !stopDaemon(done, time.Now().Add(shutdownGuardBound)) {
		t.Fatalf("stopDaemon() = false, want the daemon stopped within %v", shutdownGuardBound)
	}
	assertNoStaleSignal(t, syscall.SIGTERM)
}

// TestSignalSelf_ReturnsOnceDelivered asserts the contract the retry loops
// rest on: with the dispatcher starved, the signal is already in a channel
// registered before the send when signalSelf returns.
func TestSignalSelf_ReturnsOnceDelivered(t *testing.T) {
	starveSignalDispatch(t)

	registered := make(chan os.Signal, 1)
	signal.Notify(registered, syscall.SIGTERM)
	defer signal.Stop(registered)

	if err := signalSelf(syscall.SIGTERM, readyDeadline(t)); err != nil {
		t.Fatalf("signalSelf() error = %v", err)
	}
	select {
	case <-registered:
	default:
		t.Fatal("signalSelf returned before its signal reached a registered handler")
	}
	assertNoStaleSignal(t, syscall.SIGTERM)
}

// TestSignalSelf_ReportsASendFailure asserts a signal the kernel refuses is
// returned as a *signalSelfError carrying the kernel's error, not waited for.
func TestSignalSelf_ReportsASendFailure(t *testing.T) {
	err := signalSelf(syscall.Signal(-1), readyDeadline(t))
	var sse *signalSelfError
	if !errors.As(err, &sse) || !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("signalSelf(-1) error = %v, want a *signalSelfError wrapping EINVAL", err)
	}
}
