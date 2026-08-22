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

package secure

import "sync"

// purgeMu serializes every memguard operation that can PURGE against every
// borrow of protected memory in this process.
//
// It exists because memguard's failure path is process-wide, not scoped to the
// caller. When memguard cannot allocate, lock, or release pages it calls
// core.Panic, which calls core.Purge first (memguard v0.23.0 core/exit.go);
// Purge snapshots every live buffer and destroys each one, ending in
// memcall.Free — the pages are UNMAPPED, not merely wiped. Purge holds only
// memguard's own registry locks, so it cannot see, and does not wait for, any
// lock in this package.
//
// A per-Buffer or per-Secret lock therefore cannot make a borrow safe.
// Goroutine A can be reading the slice from a Buffer while goroutine B
// exhausts locked memory constructing an unrelated Secret and unmaps A's pages
// underneath it. That is a fault, not an error return, and no amount of
// per-object locking prevents it.
//
// This package is where the whole stack reaches for protected memory —
// go-xkms, go-quicbaas and go-trusted-platform all hold key material through
// it, in one process, concurrently — which is precisely the shape the hazard
// needs (go-qrdb#71, and go-objstore 274d149 for the same class).
//
// # Which operations are which
//
// PURGE-CLASS operations take this lock EXCLUSIVELY. Anything that can reach
// core.Panic belongs here, and that is wider than "allocation":
//
//   - constructing a Buffer or Secret, and Secret.Open, which allocates the
//     LockedBuffer it returns;
//   - Buffer.Destroy and Secret.Destroy. Destroy is NOT a read: memguard's
//     buffer destroy calls core.Panic when memcall.Free, Unlock or Protect
//     fails (core/buffer.go). Treating it as a borrow leaves the hole open;
//   - Purge, which is the process-wide purge itself and the most direct
//     instance of the hazard in this package — a deferred Purge in main()
//     racing a live borrow is exactly what this lock is for.
//
// BORROW-CLASS operations take it SHARED: Buffer.WithBytes, Buffer.Bytes and
// Buffer.Size. Size looks like a scalar read but is len(buffer.Bytes()), a
// slice header a concurrent purge rewrites.
//
// Borrows still run concurrently with each other. Only the purge-class
// operations are exclusive, and those are setup and teardown.
//
// # Lock order
//
// purgeMu first, then any per-object state, always in that order, so the two
// can never deadlock against each other.
//
// # Nested borrows are forbidden
//
// Do not call into this package from inside a WithBytes callback, and do not
// hold a borrow while starting another. Go's sync.RWMutex blocks NEW readers
// once a writer is queued, so a second borrow taken from inside the first
// deadlocks against any purge-class operation that began in between. The
// callback should read the bytes and return.
var purgeMu sync.RWMutex
