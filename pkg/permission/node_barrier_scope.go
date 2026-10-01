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

// nodeBarrierSOPermissions are the Security Officer operations on the NODE
// barrier: rekeying it, rotating its root key, generating (and deleting) its
// recovery keys, and initializing its Shamir sealing.
//
// The node barrier is a platform singleton. There is one per node, it
// protects every tenant's data, and no tenant owns it. These permissions were
// ScopeSelf, and ScopeSelf compares the caller's tenant with the request's
// tenant -- which, for an operation that names no tenant, is the caller's own.
// The check was therefore vacuous: an SO of tenant A, holding the built-in SO
// role of tenant A and presenting tenant A's own valid SO PIN, could rekey the
// barrier that protects every tenant. A tenant's own barrier is governed by
// the tenant_barrier:* permissions, which these are not.
//
// They are ScopeCross: only an identity of the system tenant may exercise
// them, on every transport, with the SO dual-factor gate on or off -- the
// platform-operator scope the tenant-administration rulings assign to
// platform-wide state.
func nodeBarrierSOPermissions() []Permission {
	return []Permission{
		SealGenerateRecovery,
		SealInitializeShamir,
		SealRekey,
		SealRotateRootKey,
	}
}

func init() {
	RegisterScope(ScopeCross, nodeBarrierSOPermissions()...)
}
