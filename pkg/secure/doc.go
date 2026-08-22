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
// package is forbidden and enforced by a custom go vet analyzer under
// pkg/secure/analyzer/.
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
// # Lifecycle
//
// The owning process binary MUST call CatchInterrupt() at startup (before
// any secret material is loaded) and defer Purge() at shutdown. These are
// the only hooks the binary is responsible for; the SDK and library code
// never touch them.
package secure
