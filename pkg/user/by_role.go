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

package user

import (
	"context"
	"slices"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// usersByRolePageSize is the page UsersHoldingRole reads per ListUsers call.
const usersByRolePageSize = 100

// HeldRoleIDs returns the role IDs u holds in tenantID: its own RoleIDs when
// tenantID is u's tenant, plus every role binding scoped to tenantID.
func HeldRoleIDs(u *User, tenantID string) []string {
	if u == nil {
		return nil
	}
	target := tenantid.Normalize(tenantID)
	out := make([]string, 0, len(u.RoleIDs)+len(u.RoleBindings))
	if tenantid.Normalize(u.TenantID) == target {
		out = append(out, u.RoleIDs...)
	}
	for _, b := range u.RoleBindings {
		if tenantid.Normalize(b.TenantID) == target {
			out = append(out, b.RoleID)
		}
	}
	return out
}

// UsersHoldingRole pages through tenantID's accounts and returns every one
// that holds roleID there (HeldRoleIDs). A store fault aborts the walk.
func UsersHoldingRole(ctx context.Context, svc UserServicer, tenantID, roleID string) ([]*User, error) {
	if svc == nil {
		return nil, &ValidationError{Field: "users", Reason: "must not be nil"}
	}
	if roleID == "" {
		return nil, &ValidationError{Field: "roleID", Reason: "must not be empty"}
	}
	var out []*User
	for page := 1; ; page++ {
		resp, err := svc.ListUsers(ctx, &ListUsersRequest{TenantID: tenantID, Page: page, PageSize: usersByRolePageSize})
		if err != nil {
			return nil, err
		}
		if resp == nil {
			return out, nil
		}
		for _, u := range resp.Users {
			if slices.Contains(HeldRoleIDs(u, tenantID), roleID) {
				out = append(out, u)
			}
		}
		if !resp.HasMore || len(resp.Users) == 0 {
			return out, nil
		}
	}
}
