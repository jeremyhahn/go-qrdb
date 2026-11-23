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

package role

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// iamExposurePermissions are the device pairing and sharing permissions and
// the shared-password ownership override (row iam-exposure, #1651).
var iamExposurePermissions = []permission.Permission{
	permission.DevicePair,
	permission.DeviceRevoke,
	permission.DeviceShare,
	permission.DeviceSync,
	permission.PasswordsOverrideOwnership,
}

func permissionSet(perms []permission.Permission) map[permission.Permission]struct{} {
	set := make(map[permission.Permission]struct{}, len(perms))
	for _, p := range perms {
		set[p] = struct{}{}
	}
	return set
}

// TestOwnerRole_HoldsIAMExposurePermissions pins that the Owner role, which
// holds every registered permission, reaches the new ones with no grant line.
func TestOwnerRole_HoldsIAMExposurePermissions(t *testing.T) {
	held := permissionSet(OwnerRole("").Permissions)
	for _, p := range iamExposurePermissions {
		if _, ok := held[p]; !ok {
			t.Errorf("OwnerRole: missing %q", p)
		}
	}
}

// TestBuiltInRoles_IAMExposureGrants pins that the built-in Admin holds the
// device pairing and sharing permissions and the ownership override, while
// the User and Auditor hold none of them.
func TestBuiltInRoles_IAMExposureGrants(t *testing.T) {
	byName := make(map[string]*Role)
	for _, r := range BuiltInRoles("") {
		byName[r.Name] = r
	}
	for _, tc := range []struct {
		name string
		want bool
	}{
		{RoleNameAdmin, true},
		{RoleNameUser, false},
		{RoleNameAuditor, false},
	} {
		r, ok := byName[tc.name]
		if !ok {
			t.Fatalf("BuiltInRoles: no %q role", tc.name)
		}
		held := permissionSet(r.Permissions)
		for _, p := range iamExposurePermissions {
			if _, got := held[p]; got != tc.want {
				t.Errorf("%s holds %q: got %t, want %t", tc.name, p, got, tc.want)
			}
		}
	}
}
