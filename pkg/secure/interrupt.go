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
	"sync/atomic"
	"syscall"

	"github.com/awnumar/memguard"
)

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
	// memguard.CatchSignal installs a handler that calls memguard.Purge
	// and os.Exit(1) on the listed signals. We use both SIGINT and
	// SIGTERM to cover Ctrl-C and orderly termination (systemd, docker
	// stop, k8s preStop).
	memguard.CatchSignal(func(_ os.Signal) {}, syscall.SIGINT, syscall.SIGTERM)
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
	memguard.Purge()
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
