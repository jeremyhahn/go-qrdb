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

// Package abac provides attribute-based access control (ABAC) as a
// decorator over the qrdb RBAC authorizer.
//
// The package ports the core of go-quicbaas's tested IAM policy engine —
// the YAML policy schema, the policy compiler, the resource trie, the
// condition parser, and the evaluator — into go-qrdb as its canonical
// home. Distribution concerns (policy storage, Raft sync, the PEP cache)
// are intentionally NOT ported; policies are loaded at construction from
// a directory of YAML files or injected directly by embedded hosts.
//
// The active policy set can be replaced at runtime, without restarting the
// embedding process, by calling Decorator.Reload — the seam a downstream
// policy store (e.g. a policy CRUD API) drives after persisting a change.
// Reload compiles and validates the new set before swapping it in, so a
// failed reload leaves the previous set fully in force, and the swap is a
// single atomic pointer store: Authorize always reads a complete policy
// set, lock-free, never a partially applied reload. Construct a Decorator
// that supports Reload even before any policy exists by passing
// WithLiveReload to Decorate.
//
// The Decorator implements auth.Authorizer and composes with the RBAC
// authorizer using hybrid semantics:
//
//  1. An explicit DENY rule match denies immediately — ABAC deny
//     overrides an RBAC allow.
//  2. An explicit ALLOW rule match allows immediately — ABAC can grant
//     beyond RBAC.
//  3. When no policy rule matches, the decision is delegated to the
//     wrapped (inner) authorizer — RBAC decides.
//
// Policies are tenant-scoped by metadata.tenant. For a request targeting
// tenant T, the decorator evaluates T's policies plus the reserved system
// tenant's policies: system-tenant policies act as platform-wide
// guardrails that apply to every tenant, while a tenant's own policies
// never affect any other tenant.
package abac
