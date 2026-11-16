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

package node

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TestNode_FirstBoot_StandardToStrictRefusesAdminHoldingSO drives the
// embedded posture change: a standard node's admin is granted the system SO
// role (standard mode allows it), and a later strict first boot on the same
// store must refuse with *firstboot.StrictSeparationSOAdminError instead of
// keeping an account that holds both roles (SPEC row 124).
func TestNode_FirstBoot_StandardToStrictRefusesAdminHoldingSO(t *testing.T) {
	ctx := context.Background()
	n := newPostureNode(t, false, audit.NewRingBuffer(64))
	runPostureFirstBoot(t, n)

	users := n.UserService()
	admin, err := users.GetUserByUsername(ctx, tenantid.System, "admin")
	if err != nil {
		t.Fatalf("GetUserByUsername(admin): %v", err)
	}
	strictDeps := func() firstboot.Dependencies {
		svcs := n.Services()
		return firstboot.Dependencies{
			Tenants: svcs.Tenant, Users: users, Roles: svcs.Role, RoleResolver: svcs.Role,
			Logger: slog.New(slog.DiscardHandler), SeparationOfDuties: true,
		}
	}
	// Control: an admin without SO passes the strict check.
	if _, err := n.FirstBoot(ctx, strictDeps()); err != nil {
		t.Fatalf("strict FirstBoot with an admin that holds no SO: %v, want success", err)
	}

	soRoleID := role.SystemRoleID(tenantid.System, role.RoleNameSO)
	if _, err := users.UpdateUser(ctx, &user.UpdateUserRequest{
		ID: admin.ID, RoleIDs: append(slices.Clone(admin.RoleIDs), soRoleID),
	}); err != nil {
		t.Fatalf("grant the admin the SO role: %v", err)
	}

	_, err = n.FirstBoot(ctx, strictDeps())
	var conflict *firstboot.StrictSeparationSOAdminError
	if !errors.As(err, &conflict) {
		t.Fatalf("strict FirstBoot with the admin holding SO: %v, want *firstboot.StrictSeparationSOAdminError", err)
	}
	if conflict.UserID != admin.ID || conflict.AdminTenantID != tenantid.System {
		t.Fatalf("conflict = %+v, want user %d with Admin in %q", conflict, admin.ID, tenantid.System)
	}
}
