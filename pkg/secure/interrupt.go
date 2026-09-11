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
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	"github.com/awnumar/memguard"
)

// signalExit is os.Exit, indirected so a test can drive the handler without
// terminating the test binary.
var signalExit = os.Exit

// purgeFn is memguard.Purge, indirected for the same reason and a sharper one:
// a test that drives the real purge destroys every enclave in the TEST BINARY,
// so every later test in the package runs against purged memory. That is the
// hazard this file exists to coordinate, arriving via the test suite — it was
// measured, not theorised (the full run crashed in TestNewSecret).
//
// Swapping it changes nothing about the locking under test: coordinatedPurge
// takes purgeMu and then calls this, so a test can assert the coordination
// without the destruction.
var purgeFn = memguard.Purge

// initialized is set by CatchInterrupt. Purge refuses to run before it
// is set — this surfaces lifecycle misuse at development time.
var initialized atomic.Bool

// CatchInterrupt wires the process signal handler so that SIGINT and
// SIGTERM trigger a secure wipe of all mlocked memory before the process
// exits. It must be called once, early in main(), before any Secret or
// Buffer is allocated. Calling it more than once is a no-op.
//
// Binaries that own the secure package (qrdbd, xkmsd, quicbaasd, xkey,
// quicbaas) are responsible for this call. Library code MUST NOT call
// it.
func CatchInterrupt() {
	if !initialized.CompareAndSwap(false, true) {
		return
	}
	// NOT memguard.CatchSignal. Its handler calls memguard.Purge directly,
	// which takes none of this package's locks — so a signal arriving while a
	// borrow is live unmaps the borrowed pages underneath the reader, which is
	// precisely the fault purgeMu exists to prevent (go-qrdb#71). The signal
	// path is the one purge this package does not initiate, and it was the one
	// left uncoordinated.
	//
	// Package-owned handling instead, routed through the same coordinated purge
	// as every other caller. Behaviour is otherwise what memguard's handler
	// gave: purge, then exit non-zero.
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go watchSignals(ch)
}

// watchSignals wipes and exits on the first signal delivered to ch.
//
// Split out from CatchInterrupt so it can be driven directly. A test that
// delivered a REAL signal proved dangerous rather than merely awkward: signal
// handlers are process-global and outlive the test that installed them, so a
// stray handler receiving the raised SIGTERM called os.Exit and killed the test
// binary mid-run — a fast FAIL with no panic and no failing test name, which is
// exactly as confusing to diagnose as it sounds. Driving this function needs no
// signal at all and cannot leak.
func watchSignals(ch <-chan os.Signal) {
	<-ch
	coordinatedPurge()
	signalExit(1)
}

// coordinatedPurge wipes all mlocked memory with purgeMu held exclusively.
//
// It WAITS for in-flight borrows. That is deliberate and it is a trade worth
// stating: shutdown is delayed by any borrow still running, where the previous
// behaviour terminated immediately and unmapped pages underneath readers. A
// borrow in this package is required to be short — read the material and
// return, no nested borrows — so a borrow that blocks shutdown indefinitely is
// a caller bug, and the alternative is a use-after-unmap fault on the way out.
//
// Daemons that want to control their own exit code should use EnablePurge with
// a deferred Purge rather than CatchInterrupt; that path is coordinated too.
func coordinatedPurge() {
	lockPurge("Purge")
	defer unlockPurge()
	purgeFn()
}

// EnablePurge marks the secure subsystem initialized so Purge works,
// WITHOUT installing any process signal handler. Use this instead of
// CatchInterrupt in binaries that own their own signal handling and drive
// a graceful shutdown — they receive SIGINT/SIGTERM via their own
// signal.NotifyContext, shut down cleanly, and wipe mlocked memory via a
// deferred Purge in main(). Unlike CatchInterrupt, EnablePurge does not
// force os.Exit on SIGINT/SIGTERM, so the binary controls its own exit
// code (a graceful SIGTERM exits 0, as orchestrators expect, instead of
// memguard's hard os.Exit(1)).
//
// Calling it more than once, or after CatchInterrupt, is a no-op.
func EnablePurge() {
	initialized.CompareAndSwap(false, true)
}

// Purge performs an immediate, synchronous wipe of all mlocked memory
// allocated by the secure package. Intended to be deferred in main() so
// the process zeroes sensitive state on any exit path (panic, graceful
// shutdown, signal). Returns *NotInitializedError if CatchInterrupt has
// not been called.
func Purge() error {
	if !initialized.Load() {
		return &NotInitializedError{}
	}
	// THE purge, taken exclusively — this is the operation every borrow in the
	// package is protected against, not merely one that might trigger it. A
	// deferred Purge in main() racing a live borrow is the most direct instance
	// of the hazard in this package. See purgeMu.
	coordinatedPurge()
	return nil
}

// PurgeOrPanic is a convenience for deferred cleanup in main(). It
// panics if CatchInterrupt was not called. Use Purge in tests and any
// code path that needs to handle the error.
func PurgeOrPanic() {
	if err := Purge(); err != nil {
		panic(err)
	}
}
