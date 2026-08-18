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
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"
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

// resetSignalHandlers drops the handlers a test installed -- including any the
// code under test left behind -- and then restores the guard. Use it in place
// of signal.Reset, which would leave the default disposition armed.
func resetSignalHandlers(sigs ...os.Signal) {
	signal.Reset(sigs...)
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
// There is deliberately no timeout: any bound tight enough to fail fast would
// be another bet on startup duration under load, and a runStart that never
// returns is a real hang, best reported by the test binary's own timeout with
// a full goroutine dump rather than by a bare "did not return" message.
func signalUntilReturn(t *testing.T, done <-chan error, sig syscall.Signal) error {
	t.Helper()

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
	}
}

func TestMain(m *testing.M) {
	armSignalGuard()
	os.Exit(m.Run())
}
