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
	"context"
	"testing"
)

// TestBuiltInRoles_HaveNoCustodianRole proves the build defines no custodian
// RBAC role. A key custodian is a share holder: it authenticates with its
// holder certificate and an enrolled factor, and RBAC refuses a holder
// identity everything, so an empty role in its name granted nothing and only
// suggested otherwise.
func TestBuiltInRoles_HaveNoCustodianRole(t *testing.T) {
	for _, r := range BuiltInRoles("acme") {
		if r.Name == "custodian" {
			t.Fatalf("BuiltInRoles defines a custodian role: %+v", r)
		}
	}
	if IsSystemRoleName("custodian") {
		t.Fatal("custodian is still reserved as a system role name")
	}
}

// TestReconcileBuiltInRoles_SeedsNoCustodianRole proves seeding a tenant
// creates no custodian role.
func TestReconcileBuiltInRoles_SeedsNoCustodianRole(t *testing.T) {
	ctx := context.Background()
	svc := newTestStore(t)
	if _, err := svc.ReconcileBuiltInRoles(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if r, err := svc.GetRoleByName(ctx, "acme", "custodian"); err == nil {
		t.Fatalf("seeding created a custodian role: %+v", r)
	}
}
