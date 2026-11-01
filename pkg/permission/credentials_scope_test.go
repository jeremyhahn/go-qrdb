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

package permission

import "testing"

// Every tenant has its own named-credential store, so the credentials:*
// permissions are self-scope: a tenant identity exercises them in its own
// tenant against its own store, and none of them reaches across tenants.
func TestCredentialsPermissions_AreSelfScope(t *testing.T) {
	perms := []Permission{
		CredentialsSubmit, CredentialsStrategy, CredentialsList, CredentialsDelete,
		CredentialsRotate, CredentialsGCExpired, CredentialsRetrieveVersion,
		CredentialsListVersions, CredentialsStore, CredentialsRetrieve,
	}
	for _, p := range perms {
		t.Run(string(p), func(t *testing.T) {
			if got := mustScope(t, p); got != ScopeSelf {
				t.Errorf("LookupScope(%q) = %v, want %v", p, got, ScopeSelf)
			}
			if IsCrossScope(p) {
				t.Errorf("IsCrossScope(%q) = true, want false", p)
			}
		})
	}
}

// The credentials:* scope is declared in self_scope.go with the rest of the
// self-scoped permissions, not in an init of its own.
func TestCredentialsPermissions_DeclaredInSelfScopedList(t *testing.T) {
	declared := make(map[Permission]bool)
	for _, p := range selfScopedPermissions() {
		declared[p] = true
	}
	for _, p := range []Permission{
		CredentialsSubmit, CredentialsStrategy, CredentialsList, CredentialsDelete,
		CredentialsRotate, CredentialsGCExpired, CredentialsRetrieveVersion,
		CredentialsListVersions, CredentialsStore, CredentialsRetrieve,
	} {
		if !declared[p] {
			t.Errorf("selfScopedPermissions() does not declare %q", p)
		}
	}
}
