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

// TestNodeBarrierPermissions_AreCrossScope pins, by name, every permission
// that changes the node barrier or decides who holds a share of its key. Each
// was ScopeSelf, which for a route naming no tenant compares the caller's
// tenant with itself; a tenant's SO could therefore plant officers in the node
// barrier's unseal and recovery domains.
func TestNodeBarrierPermissions_AreCrossScope(t *testing.T) {
	for _, p := range []Permission{
		SealBootstrap, SealEnableEncryption, SealGenerateRecovery,
		SealInitializeShamir, SealRekey,
		SealRotateRootKey, SealSeal, SealUnseal,
		SealOfficerRegister, SealOfficerList, SealOfficerRevoke,
		CeremonyInitialize, CeremonySignCSR,
	} {
		if got, err := LookupScope(p); err != nil || got != ScopeCross {
			t.Errorf("%s: scope = %v (err %v), want ScopeCross", p, got, err)
		}
	}
}

// TestNodeBarrierPermissions_ReadAndPossessionVerbsStaySelf pins the node
// verbs deliberately left ScopeSelf: seal:status and ceremony:status name no
// share holder. A holder's claim and quorum steps carry no permission at all:
// they are identity-gated by the holder's certificate and factor proof.
func TestNodeBarrierPermissions_ReadAndPossessionVerbsStaySelf(t *testing.T) {
	for _, p := range []Permission{
		SealStatus, CeremonyStatus,
	} {
		if got, err := LookupScope(p); err != nil || got != ScopeSelf {
			t.Errorf("%s: scope = %v (err %v), want ScopeSelf", p, got, err)
		}
	}
}

// TestNodeBarrierPermissions_DeclaredOnce: a permission in either node-barrier
// list must not also be declared by the self-scope list, or its scope would be
// whichever init ran last.
func TestNodeBarrierPermissions_DeclaredOnce(t *testing.T) {
	self := make(map[Permission]struct{})
	for _, p := range selfScopedPermissions() {
		self[p] = struct{}{}
	}
	node := append(nodeBarrierSOPermissions(), nodeBarrierShareHolderPermissions()...)
	node = append(node, clusterAdminPermissions()...)
	node = append(node, clusterBackupPermissions()...)
	if len(node) < 31 {
		t.Fatalf("node-barrier and cluster lists hold %d permissions, want at least 31", len(node))
	}
	for _, p := range node {
		if _, dup := self[p]; dup {
			t.Errorf("%s is declared both ScopeSelf and ScopeCross", p)
		}
	}
}
