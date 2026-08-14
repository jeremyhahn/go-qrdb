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

import "testing"

// TestMVCCCompact_IsCrossScope pins mvcc:compact to ScopeCross so the RBAC
// authorizer admits it only for system-tenant identities and denies it to
// tenant-local identities — even when carried by a tenant-assignable role such
// as operator. MVCC revisions are a store-global counter; a tenant Compact
// would destroy every other tenant's historical revisions, so compaction is
// reserved for the system tenant. This is the authz-layer half of the defense
// paired with server.namespacedMVCCService.Compact.
func TestMVCCCompact_IsCrossScope(t *testing.T) {
	if got := LookupScope(MVCCCompact); got != ScopeCross {
		t.Fatalf("LookupScope(MVCCCompact) = %v, want %v (ScopeCross)", got, ScopeCross)
	}
	if !IsCrossScope(MVCCCompact) {
		t.Fatal("IsCrossScope(MVCCCompact) = false, want true")
	}
	// It must remain registered in the global permission registry.
	if !IsRegistered(MVCCCompact) {
		t.Fatal("IsRegistered(MVCCCompact) = false, want true")
	}
}

// TestMVCCReads_RemainSelfScope is a regression guard: only compaction is
// store-global. The revision-aware read verbs stay ScopeSelf so tenant-local
// identities retain normal MVCC access to their own namespace.
func TestMVCCReads_RemainSelfScope(t *testing.T) {
	selfScoped := []Permission{
		MVCCPutWithLease,
		MVCCGetRevision,
		MVCCGetAtRevision,
		MVCCRange,
		MVCCCurrentRevision,
	}
	for _, p := range selfScoped {
		if got := LookupScope(p); got != ScopeSelf {
			t.Errorf("LookupScope(%q) = %v, want %v (ScopeSelf)", p, got, ScopeSelf)
		}
	}
}
