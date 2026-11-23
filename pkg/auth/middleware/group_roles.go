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

package middleware

import (
	"context"
	"slices"

	"github.com/jeremyhahn/go-qrdb/pkg/group"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// GroupResolver resolves an IAM group by ID. group.GroupServicer satisfies it.
type GroupResolver interface {
	GetGroup(ctx context.Context, id string) (*group.Group, error)
}

// WithGroupResolver sets the group store identity binding reads the roles of
// a user's IAM groups from. Without it a record's GroupIDs contribute no
// roles (fail closed).
func WithGroupResolver(g GroupResolver) Option {
	return func(m *AuthMiddleware) {
		m.groups = g
	}
}

// effectiveRoles returns the record's RoleIDs union the RoleIDs of each group
// in its GroupIDs, de-duplicated and sorted. A group that does not resolve,
// or resolves to another tenant's group, contributes nothing: a lookup
// failure narrows the identity's roles, never widens them.
func (m *AuthMiddleware) effectiveRoles(ctx context.Context, u *user.User) []string {
	if len(u.GroupIDs) == 0 || m.groups == nil {
		return u.RoleIDs
	}
	roles := slices.Clone(u.RoleIDs)
	tenantID := tenantid.Normalize(u.TenantID)
	gctx := tenant.WithTenantID(ctx, tenantID)
	for _, gid := range u.GroupIDs {
		g, err := m.groups.GetGroup(gctx, gid)
		if err != nil || g == nil || g.TenantID != tenantID {
			continue
		}
		roles = append(roles, g.RoleIDs...)
	}
	slices.Sort(roles)
	return slices.Compact(roles)
}
