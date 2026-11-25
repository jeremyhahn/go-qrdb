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

// TestPasswordsOverrideOwnership_Vocabulary pins the shared-password
// ownership override: its wire string, resource and action, registration,
// self scope and category.
func TestPasswordsOverrideOwnership_Vocabulary(t *testing.T) {
	const want = "passwords:override_ownership"
	if string(PasswordsOverrideOwnership) != want {
		t.Fatalf("permission string: got %q, want %q", PasswordsOverrideOwnership, want)
	}
	if got := Resource(PasswordsOverrideOwnership); got != "passwords" {
		t.Errorf("Resource: got %q, want %q", got, "passwords")
	}
	if got := Action(PasswordsOverrideOwnership); got != "override_ownership" {
		t.Errorf("Action: got %q, want %q", got, "override_ownership")
	}
	if !IsRegistered(PasswordsOverrideOwnership) {
		t.Errorf("IsRegistered(%q): got false, want true", PasswordsOverrideOwnership)
	}
	if got, err := Parse(want); err != nil || got != PasswordsOverrideOwnership {
		t.Errorf("Parse(%q): got (%q, %v), want (%q, nil)", want, got, err, PasswordsOverrideOwnership)
	}
	scope, err := LookupScope(PasswordsOverrideOwnership)
	if err != nil {
		t.Fatalf("LookupScope: %v", err)
	}
	if scope != ScopeSelf {
		t.Errorf("LookupScope: got %s, want %s", scope, ScopeSelf)
	}
	var cats []string
	for _, cat := range Categories() {
		for _, p := range cat.Permissions {
			if p == PasswordsOverrideOwnership {
				cats = append(cats, cat.Name)
			}
		}
	}
	if len(cats) != 1 || cats[0] != "Password Vault" {
		t.Errorf("categories: got %v, want [Password Vault]", cats)
	}
}

// TestPasswordsOverrideOwnership_BuiltInRoleGrants pins who may override a
// shared password's ownership: the Security Officer and the Admin, and no
// other built-in role. It is a governance duty, which is what lets the SO
// hold it.
func TestPasswordsOverrideOwnership_BuiltInRoleGrants(t *testing.T) {
	for _, tc := range []struct {
		role string
		want bool
	}{
		{RoleSO, true},
		{RoleAdmin, true},
		{RoleOperator, false},
		{RoleUser, false},
		{RoleAuditor, false},
	} {
		if got := HasRoleGrant(tc.role, PasswordsOverrideOwnership); got != tc.want {
			t.Errorf("HasRoleGrant(%q, %q): got %t, want %t", tc.role, PasswordsOverrideOwnership, got, tc.want)
		}
	}
	if got := LookupDuty(PasswordsOverrideOwnership); got != DutyGovernance {
		t.Errorf("LookupDuty(%q): got %v, want %v", PasswordsOverrideOwnership, got, DutyGovernance)
	}
}
