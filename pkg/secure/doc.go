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
// github.com/jeremyhahn/go-enclave — raw go-enclave (and, historically, raw
// memguard) usage anywhere outside this package is forbidden.
//
// The ban is enforced by the go vet analyzer under pkg/secure/analyzer/,
// which `make secure-analyzer` runs over every module in this repository
// under every combination in the Makefile's SUPPORTED_TAG_SETS, in CI's
// Lint job.
//
// The ban covers THIS repository. Downstream consumers (go-xkms,
// go-quicbaas) import this package and are bound by the same rules, but an
// analyzer runs where its make target runs; each of those repositories has
// to wire up its own, and until it does its compliance is unmeasured
// (go-xkms#549, go-quicbaas#827).
//
// # go-enclave, not memguard (go-qrdb#114, go-qrdb#553)
//
// This package used to wrap github.com/awnumar/memguard directly. memguard's
// failure path is process-wide: a failed allocation calls core.Panic, which
// purges EVERY live enclave in the process and unmaps their pages before
// aborting it — not scoped to the component that failed, and with no
// recover. This package had no recover either, so an RLIMIT_MEMLOCK ceiling
// killed the daemon, and surviving that required a package-local
// process-wide lock (purgeMu) that every allocation, every Destroy and every
// borrow serialized on.
//
// It now wraps github.com/jeremyhahn/go-enclave: MemoryEnclave and
// SealedEnclave map their own pages, carry their own per-instance lock, and
// return a typed error — enclave.ErrLockedMemoryUnavailable — from a failed
// mlock instead of purging and aborting. Every Buffer allocation and every
// Secret.Open is admitted against a process-wide locked-memory BUDGET
// (enclave.ProcessAllocator), which the owning binary sizes once at startup
// from the discovered RLIMIT_MEMLOCK ceiling (sdk/go/secure.SetAllocator,
// wired to enclave.NewAllocatorWithinRLimit); an exhausted budget refuses
// with enclave.ErrBudgetExhausted before any syscall is made, so one
// caller's allocation failure can no longer take down an unrelated caller's
// live key material. Sealing a Secret (NewSecret, NewSecretFromBuffer) is
// NOT admitted — go-enclave has no budgeted seal path as of v0.6.0-alpha,
// which its own documentation records as a deliberate, scoped gap (sealing
// is a one-time cost per value, not a per-request one the way opening is).
// There is no process-wide purge lock in this package any more — see
// borrowguard.go for what a "nested borrow" now means without one.
//
// # Types
//
// There are two core types:
//
//   - Secret: a long-lived, encrypted-at-rest-in-memory container. Wraps an
//     enclave.SealedEnclave (AES-256-GCM under the process Sealer). Open()
//     decrypts the material into a short-lived Buffer that the caller is
//     responsible for destroying.
//   - Buffer: a short-lived, mlocked, canary-guarded, guard-paged working
//     copy. Wraps an enclave.MemoryEnclave. ALWAYS defer buf.Destroy()
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
//     bytes are wiped on entry, on every return path including failure. Do
//     not reuse the slice after the call.
//  6. Prefer Buffer.WithBytes to Buffer.Bytes for anything that reads the
//     material. Only WithBytes holds the borrow across the read, which is
//     what makes it safe against a concurrent Destroy of the SAME Buffer
//     (see below). Never call back into this package from inside a
//     WithBytes callback — a nested call on the SAME object deadlocks, and
//     the guard treats any nested call as a violation regardless of object.
//  7. When the borrow must end in a caller-owned []byte because the calling
//     signature says []byte, use CopyOut rather than writing the copy by
//     hand. It holds ONE borrow across the whole copy and refuses an empty
//     source; the hand-written version sizes from Size() and then reads
//     Bytes(), which is two separate reads with a gap between them.
//  8. Arm the borrow guard in the tests for any path that reads protected
//     memory: `defer secure.GuardBorrows(t.Fatalf)()`. See below.
//
// # The borrow guard
//
// Rule 6 is the one that cannot be enforced by reading, because whether a
// callback is safe is a question about everything it can reach transitively,
// through interfaces the caller does not own. A violation on the SAME
// object produces no error and no panic — the goroutine blocks on itself
// forever and the only symptom is a job that hangs until its wall-clock
// limit.
//
// GuardBorrows and EnableBorrowGuard arm a detector that answers "does THIS
// goroutine already hold a borrow" at every constructor/Destroy-shaped and
// borrow-shaped entry point, and reports a *NestedBorrowError before the
// call is allowed to proceed. A conversion that reaches a nested call then
// fails one test naming both sites, instead of risking a hang.
//
// It is opt-in and off by default: arming it costs a runtime.Stack call per
// borrow, which is too much for the DEK read path. Disarmed, each
// instrumented operation pays one atomic load.
//
// # Allocation and process lifecycle
//
// This package's constructors are LIBRARY code: the ones that admit
// (NewBuffer, NewBufferFromBytes, Secret.Open) call enclave.ProcessAllocator()
// to reach the process-wide locked-memory budget, and none of them ever
// install one themselves. The OWNING BINARY (qrdbd, xkmsd,
// quicbaasd, xkey, quicbaas) is responsible for sizing and installing that
// budget with enclave.SetAllocator before anything in the process allocates
// — re-exported, pure-alias, from sdk/go/secure — and for calling
// enclave.WipeAll on orderly shutdown.
//
// WipeAll's two effects are not the same thing, and a caller relying on it as
// "every secret is wiped" is relying on something it does not do. It destroys
// every live MemoryEnclave — every outstanding Buffer, and every plaintext a
// Secret.Open call has transiently produced — wiping and unmapping their
// pages, which IS a real wipe. A Secret's own SEALED form is different: its
// ciphertext and wrapped-key blob live on the ordinary (unlocked) heap by
// design (go-enclave's SealedEnclave documentation), so WipeAll does not
// touch those bytes directly. What it does instead is destroy the process
// Sealer's own key material, which is itself a MemoryEnclave on the same
// registry — so every live Secret becomes permanently unopenable
// (Secret.Open returns enclave.ErrSealerWiped) from that point on, without
// its ciphertext bytes being zeroed. Zeroing them is Secret.Destroy's job,
// and it is the caller's to invoke; WipeAll is a last-resort process-wide
// sweep, not a substitute for destroying what was constructed. Library code
// in this repository never calls either enclave.SetAllocator or
// enclave.WipeAll.
//
// The owning binary also calls HardenProcess once at startup, before it
// creates any protected memory: mlock keeps these pages out of swap but does
// not stop a same-user process reading them through ptrace, /proc/<pid>/mem
// or a core file, and HardenProcess closes those routes where the platform
// offers a way (go-qrdb#554). Library code never calls it either.
package secure
