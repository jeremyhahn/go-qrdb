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

package permission

// ---------------------------------------------------------------------------
// Store-global admin permissions
//
// A handful of data-plane verbs act on state that is shared by every
// prefix-isolated tenant on a store rather than on a single tenant's
// namespace. They are tagged ScopeCross so the RBAC authorizer admits them
// only for system-tenant identities and rejects them for tenant-local
// identities — even when a tenant-assignable role (e.g. operator) carries the
// permission.
//
// MVCCCompact is the canonical example: MVCC revisions are a store-global
// monotonic counter, so compacting to revision N discards the historical
// revisions below N for EVERY tenant's keys, destroying other tenants'
// time-travel history. Compaction is therefore reserved for the system tenant.
// The service layer independently enforces this at the namespaced MVCC view
// (see server.namespacedMVCCService.Compact / service.CompactSystemOnlyError);
// this ScopeCross tag is the authz-layer half of the same defense.
//
// The permission constant itself is declared in constants.go (and registered
// in the global permission registry there); this file only refines its scope.
func init() {
	RegisterScope(ScopeCross, MVCCCompact)
}
