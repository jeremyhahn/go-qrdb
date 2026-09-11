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

// Package secure provides the canonical, project-wide abstraction for
// handling sensitive byte material (keys, passphrases, PINs, Shamir
// shares, recovery keys) in memory. It is a thin, opinionated facade over
// github.com/awnumar/memguard — raw memguard usage anywhere outside this
// package is forbidden.
//
// The ban is enforced by the go vet analyzer under pkg/secure/analyzer/,
// which `make secure-analyzer` runs over every module in this repository
// under every combination in the Makefile's SUPPORTED_TAG_SETS, in CI's
// Lint job. That sentence was FALSE from the day the analyzer was written
// until go-qrdb#429 armed it: the analyzer existed, was unit-tested, and
// ran nowhere, so the rules were enforced by the analyzer's own tests and
// not over this tree. A raw memguard import sat in pkg/ceremony's
// share-store test the whole time. Do not restore the claim without the
// target — an agent reading it concludes the invariant holds.
//
// The ban covers THIS repository. Downstream consumers (go-xkms,
// go-quicbaas) import this package and are bound by the same rules, but an
// analyzer runs where its make target runs; each of those repositories has
// to wire up its own, and until it does its compliance is unmeasured
// (go-xkms#549, go-quicbaas#827).
//
// # Types
//
// There are two core types:
//
//   - Secret: a long-lived, encrypted-at-rest-in-memory container. Wraps a
//     memguard.Enclave. Open() decrypts the material into a short-lived
//     Buffer that the caller is responsible for destroying.
//   - Buffer: a short-lived, mlocked, canary-guarded, guard-paged working
//     copy. Wraps a memguard.LockedBuffer. ALWAYS defer buf.Destroy()
//     immediately after construction.
//
// # Rules
//
//  1. The zero value of either type is unusable. Use the provided
//     constructors.
//  2. Always defer Destroy() immediately after constructing a Buffer.
//  3. Never store the []byte returned by Buffer.Bytes() beyond the
//     lifetime of the Buffer. Never copy it into an unprotected slice.
//  4. Secret is safe to hand to another goroutine (its Destroy is
//     idempotent and atomic). Buffer is single-owner: Open → use →
//     Destroy on one goroutine.
//  5. Constructors that take a []byte consume the caller's slice — the
//     bytes are wiped on entry. Do not reuse the slice after the call.
//  6. Prefer Buffer.WithBytes to Buffer.Bytes for anything that reads the
//     material. Only WithBytes holds the borrow across the read, which is
//     what makes it safe against a process-wide purge (see below). Never
//     call back into this package from inside a WithBytes callback — a
//     nested borrow deadlocks.
//  7. When the borrow must end in a caller-owned []byte because the calling
//     signature says []byte, use CopyOut rather than writing the copy by
//     hand. It holds ONE borrow across the whole copy and refuses an empty
//     source; the hand-written version sizes from Size() and then reads
//     Bytes(), which is two acquisitions with a purge-shaped gap between
//     them.
//  8. Arm the borrow guard in the tests for any path that reads protected
//     memory: `defer secure.GuardBorrows(t.Fatalf)()`. See below.
//
// # The borrow guard
//
// Rule 6 is the one that cannot be enforced by reading, because whether a
// callback is safe is a question about everything it can reach transitively,
// through interfaces the caller does not own. Violating it produces no error
// and no panic — the goroutine blocks on itself forever and the only symptom
// is a job that hangs until its wall-clock limit.
//
// GuardBorrows and EnableBorrowGuard arm a detector that answers "does THIS
// goroutine already hold a borrow" at every purge-class and borrow-class entry
// point, and reports a *NestedBorrowError BEFORE taking the lock that would
// wedge. A conversion that reaches an allocation then fails one test naming
// both sites, instead of hanging the suite.
//
// It is opt-in and off by default: arming it costs a runtime.Stack call per
// borrow, which is too much for the DEK read path. Disarmed, each instrumented
// operation pays one atomic load.
//
// # Purge coordination
//
// memguard's failure path is process-wide. A failed allocation calls
// core.Panic, which purges EVERY live enclave in the process and unmaps
// their pages — it is not scoped to the component that failed. Since the
// whole stack holds key material through this package, in one process,
// concurrently, an unrelated subsystem's allocation failure can invalidate
// key material another component is reading.
//
// This package serializes that internally: operations that can purge —
// every constructor, Secret.Open, both Destroy methods, and Purge itself —
// take a process-wide lock exclusively, and borrows take it shared. Callers
// need no change for it EXCEPT the one case a lock cannot cover: the slice
// returned by Buffer.Bytes outlives the call, so nothing protects it once
// Bytes has returned. That is why rule 6 exists (go-qrdb#71).
//
// Bytes still has callers, and they are not oversights. Each is a read whose
// borrow CANNOT be held, because the material crosses into an injected
// interface — seal.EncryptionBarrier, seal.SealingStrategy, ceremony's
// BarrierUnseal — whose implementations are supplied by consumers and may
// allocate protected memory, or into a go-quicraft enclave borrow, whose own
// documentation forbids nesting a second protected-memory borrow inside it.
// Converting those is go-qrdb#145 and rides on the go-enclave adoption wave;
// until then they carry the residual purge exposure, which is a timing fault
// rather than a key leak, and each one says so at the site.
//
// # Lifecycle
//
// The owning process binary MUST call CatchInterrupt() at startup (before
// any secret material is loaded) and defer Purge() at shutdown. These are
// the only hooks the binary is responsible for; the SDK and library code
// never touch them.
package secure
