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

func TestMain(m *testing.M) {
	armSignalGuard()
	os.Exit(m.Run())
}
