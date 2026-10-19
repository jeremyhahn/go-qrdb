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

// deviceOTAVocabulary pins the literal wire strings of the device-onboarding
// and OTA permissions. The layers that onboard devices and ship updates gate
// on exactly these pairs, so a retyped constant here silently changes who may
// onboard a device or roll out an update.
var deviceOTAVocabulary = []struct {
	perm     Permission
	resource string
	action   string
}{
	{DevicesOnboard, "devices", "onboard"},
	{OTAActivate, "ota", "activate"},
	{OTAConfirm, "ota", "confirm"},
}

// TestDeviceOTAPermissions_Vocabulary asserts each permission carries the
// exact wire string, splits into the expected resource and action, and is in
// the global registry (so a role may actually grant it).
func TestDeviceOTAPermissions_Vocabulary(t *testing.T) {
	for _, tc := range deviceOTAVocabulary {
		want := tc.resource + ":" + tc.action
		if string(tc.perm) != want {
			t.Errorf("permission string: got %q, want %q", tc.perm, want)
		}
		if got := Resource(tc.perm); got != tc.resource {
			t.Errorf("Resource(%q): got %q, want %q", tc.perm, got, tc.resource)
		}
		if got := Action(tc.perm); got != tc.action {
			t.Errorf("Action(%q): got %q, want %q", tc.perm, got, tc.action)
		}
		if !IsRegistered(tc.perm) {
			t.Errorf("IsRegistered(%q): got false, want true", tc.perm)
		}
		if got, err := Parse(want); err != nil || got != tc.perm {
			t.Errorf("Parse(%q): got (%q, %v), want (%q, nil)", want, got, err, tc.perm)
		}
	}
}

// TestDeviceOTAPermissions_ByResource proves each family is reachable as a
// whole and that no permission outside the declared verbs claims either
// resource.
func TestDeviceOTAPermissions_ByResource(t *testing.T) {
	for _, resource := range []string{"devices", "ota"} {
		declared := make(map[Permission]struct{})
		for _, tc := range deviceOTAVocabulary {
			if tc.resource == resource {
				declared[tc.perm] = struct{}{}
			}
		}
		got := ByResource(resource)
		if len(got) != len(declared) {
			t.Fatalf("ByResource(%q): got %d permissions %v, want %d", resource, len(got), got, len(declared))
		}
		for _, p := range got {
			if _, ok := declared[p]; !ok {
				t.Errorf("ByResource(%q) returned undeclared permission %q", resource, p)
			}
		}
	}
}

// TestDeviceOTAPermissions_UnknownActionNotRegistered is the negative case:
// an action that was never declared must not resolve, so a typo in a role
// grant is rejected rather than silently accepted.
func TestDeviceOTAPermissions_UnknownActionNotRegistered(t *testing.T) {
	for _, p := range []Permission{
		"devices:enroll", "devices:onboard:cross", "devices:", "devices",
		"ota:rollback", "ota:stage", "ota:", "ota",
	} {
		if IsRegistered(p) {
			t.Errorf("IsRegistered(%q): got true, want false", p)
		}
		if _, err := LookupScope(p); err == nil {
			t.Errorf("LookupScope(%q): got nil error, want one", p)
		}
	}
}

// TestDeviceOTAPermissions_ScopeSelf pins the tenancy reach: each tenant
// onboards, activates and confirms for its own devices only. ScopeSelf has no
// system-tenant lift, so a grant held in one tenant reaches no other.
func TestDeviceOTAPermissions_ScopeSelf(t *testing.T) {
	for _, tc := range deviceOTAVocabulary {
		got, err := LookupScope(tc.perm)
		if err != nil {
			t.Fatalf("LookupScope(%q): %v", tc.perm, err)
		}
		if got != ScopeSelf {
			t.Errorf("LookupScope(%q): got %s, want %s", tc.perm, got, ScopeSelf)
		}
		if IsCrossScope(tc.perm) {
			t.Errorf("IsCrossScope(%q): got true, want false", tc.perm)
		}
	}
}

// TestDeviceOTAPermissions_Categories proves the permissions render under the
// Device Services group, each in exactly one category.
func TestDeviceOTAPermissions_Categories(t *testing.T) {
	seen := make(map[Permission]string)
	for _, cat := range Categories() {
		for _, p := range cat.Permissions {
			for _, tc := range deviceOTAVocabulary {
				if p != tc.perm {
					continue
				}
				if prev, dup := seen[p]; dup {
					t.Errorf("%q is in categories %q and %q", p, prev, cat.Name)
				}
				seen[p] = cat.Name
				if cat.Group != "Device Services" {
					t.Errorf("%q: category %q has group %q, want %q", p, cat.Name, cat.Group, "Device Services")
				}
			}
		}
	}
	for _, tc := range deviceOTAVocabulary {
		if _, ok := seen[tc.perm]; !ok {
			t.Errorf("%q is in no category", tc.perm)
		}
	}
}

// TestDeviceOTAPermissions_BuiltInRoleGrants pins which built-in roles carry
// the three permissions. The tenant Admin does everything in its tenant
// except key custody, so it holds all three; the User role is a crypto
// consumer and the Auditor is read-only, so neither holds any.
func TestDeviceOTAPermissions_BuiltInRoleGrants(t *testing.T) {
	for _, tc := range []struct {
		role string
		want bool
	}{
		{RoleAdmin, true},
		{RoleUser, false},
		{RoleAuditor, false},
	} {
		for _, v := range deviceOTAVocabulary {
			if got := HasRoleGrant(tc.role, v.perm); got != tc.want {
				t.Errorf("HasRoleGrant(%q, %q): got %t, want %t", tc.role, v.perm, got, tc.want)
			}
		}
	}
}
