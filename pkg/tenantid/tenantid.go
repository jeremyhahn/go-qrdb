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

// Package tenantid holds the reserved system tenant identifier, the two pure
// functions over tenant identifiers that every layer needs (IsSystem,
// Normalize) and the structural rule for a key's "<tenant>/<db>/" namespace
// (NamespaceOf). It is a leaf package -- it imports nothing from this module
// -- so packages below pkg/tenant in the import graph (pkg/ceremony,
// pkg/auth, pkg/compaction) can use them without importing pkg/tenant, which
// imports them.
package tenantid

// System is the reserved identifier for the platform's structurally
// privileged tenant. Membership in the system tenant grants cross-tenant
// scope to whatever roles a user holds inside it (see the system-tenant
// rule in pkg/auth/rbac). It is also the implicit fallback returned by
// tenant.FromContext when no tenant is present on a context — code paths that
// run without an explicit tenant binding (bootstrap, daemon-internal
// maintenance) execute against the system tenant.
//
// System is reserved: tenant.TenantServicer.CreateTenant rejects this id
// (and a small set of other reserved patterns) for all external callers.
// Only first-boot bootstrap may create the system tenant, by passing
// tenant.WithInternalCall(ctx) to bypass the reservation check.
const System = "system"

// IsSystem reports whether id denotes the structurally privileged
// system tenant. It returns true for both System and the empty
// string: per the documented rule, an identity or context with no tenant
// binding resolves to the system tenant (see tenant.FromContext), so "no tenant"
// and "system tenant" are the same scope for authorization purposes. This
// is the single canonical predicate for that question — callers must use
// it instead of re-deriving the `x == "" || x == System` (or bare
// `x == System`) comparison at each call site.
//
// The empty case is a PRECONDITION, not a convenience: on a normalized path
// the predicate must never observe "". Normalize is the single fold that
// establishes it, and every boundary that binds a tenant onto a context, a
// session or an identity calls it — see tenant.FromContext,
// auth/middleware.finalizeIdentity and sql/provider.QRDBSession.TenantID.
// Before #188 that precondition was documented here and enforced nowhere:
// auth/middleware folded the CONTEXT tenant but left identity.TenantID raw,
// and rbac.IsSystem reads exactly that raw field, so an identity
// authenticated without a tenant binding reached this predicate unnormalized.
//
// The empty branch is retained rather than removed because unscoped STORE
// callers depend on it (identity_index, store_resolver, kv_service and
// database/manager_tenant all ask "is this the unscoped keyspace?"), and
// because folding is what the fix does — "" is turned into System at
// the boundary, not made to mean something new here. It follows that this
// predicate cannot distinguish "unknown" from "system"; making an absent
// tenant fail CLOSED is a separate, deliberate decision that belongs to a
// distinct authorization-facing predicate (see #188, options 1 and 3).
func IsSystem(id string) bool {
	return id == "" || id == System
}

// Normalize folds a tenant identifier onto its canonical form: the empty
// string — "no tenant binding" — becomes System, and every other
// value is returned unchanged.
//
// This is the enforcement half of IsSystem's precondition. Boundaries
// that bind a tenant MUST call it rather than open-coding
// `if id == "" { id = System }`; five copies of that fold existed
// before #188 and the one that was missing — identity.TenantID in
// auth/middleware — is what let an unnormalized "" reach an authorization
// decision.
//
// Normalize does not decide privilege. It records that a caller arrived
// without a tenant and states the tenant that decision has always mapped to,
// in one place, so a future change to that mapping has one place to change.
func Normalize(id string) string {
	if id == "" {
		return System
	}
	return id
}
