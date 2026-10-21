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

package bootstrap

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// createOnlyRoles is a firstboot.RoleCreator that cannot resolve a role by
// name, the shape of a role seam that leaves a seeding collision unverifiable.
type createOnlyRoles struct{}

func (createOnlyRoles) CreateRole(context.Context, *role.CreateRoleRequest) (*role.Role, error) {
	return nil, nil
}

func TestTenantRoleResolver_ExplicitResolverWins(t *testing.T) {
	explicit, roles := &fakeRoles{}, &fakeRoles{}
	if got := tenantRoleResolver(Dependencies{Roles: roles, RoleResolver: explicit}); got != role.NameResolver(explicit) {
		t.Fatalf("tenantRoleResolver = %p, want the explicit RoleResolver %p over Roles %p", got, explicit, roles)
	}
}

func TestTenantRoleResolver_FallsBackToRoles(t *testing.T) {
	roles := &fakeRoles{}
	if got := tenantRoleResolver(Dependencies{Roles: roles}); got != role.NameResolver(roles) {
		t.Fatalf("tenantRoleResolver = %v, want Roles, which resolves names", got)
	}
}

func TestTenantRoleResolver_NilWhenRolesCannotResolve(t *testing.T) {
	if got := tenantRoleResolver(Dependencies{Roles: createOnlyRoles{}}); got != nil {
		t.Fatalf("tenantRoleResolver = %v, want nil for a create-only role seam", got)
	}
}

func TestAdminCompletionBounds_ZeroDefaultsEveryBound(t *testing.T) {
	got := AdminCompletionBounds{}.withDefaults()
	want := AdminCompletionBounds{Budget: 2 * time.Minute, AttemptTimeout: 10 * time.Second, Delay: 250 * time.Millisecond, MaxDelay: 5 * time.Second}
	if got != want {
		t.Fatalf("withDefaults() = %+v, want %+v", got, want)
	}
}

func TestInvalidAdminUsernameError_QuotesTheName(t *testing.T) {
	msg := (&InvalidAdminUsernameError{Username: "bad\nname"}).Error()
	if !strings.Contains(msg, `"bad\nname"`) || !strings.HasPrefix(msg, "bootstrap: ") {
		t.Fatalf("Error() = %q, want the bootstrap-prefixed message with the name quoted", msg)
	}
}
