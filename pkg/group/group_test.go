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

package group

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

type recordingRevoker struct {
	calls []string
	err   error
}

func (r *recordingRevoker) RevokeGroup(_ context.Context, tenantID, groupID string) (int, error) {
	r.calls = append(r.calls, tenantID+"/"+groupID)
	return 1, r.err
}

type fixture struct {
	groups    GroupServicer
	revoker   *recordingRevoker
	acmeRole  string
	otherRole string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	roles, err := role.NewStore(kvstore.NewMemoryKVStore())
	if err != nil {
		t.Fatalf("role store: %v", err)
	}
	mk := func(tenantID, name string) string {
		r, err := roles.CreateRole(ctx, &role.CreateRoleRequest{
			Name: name, TenantID: tenantID,
			Permissions: []permission.Permission{permission.RolesRead},
		})
		if err != nil {
			t.Fatalf("create role: %v", err)
		}
		return r.ID
	}
	rv := &recordingRevoker{}
	g, err := NewStore(kvstore.NewMemoryKVStore(), WithRoleLookup(roles), WithMemberRevoker(rv))
	if err != nil {
		t.Fatalf("group store: %v", err)
	}
	return &fixture{groups: g, revoker: rv, acmeRole: mk("acme", "reader"), otherRole: mk("other", "reader")}
}

func TestGroupStore_Lifecycle(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	g, err := f.groups.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "eng", Description: "d", RoleIDs: []string{f.acmeRole, f.acmeRole}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if g.ID == "" || g.TenantID != "acme" || len(g.RoleIDs) != 1 || g.RoleIDs[0] != f.acmeRole {
		t.Fatalf("created group = %+v", g)
	}
	got, err := f.groups.GetGroup(ctx, g.ID)
	if err != nil || got.Name != "eng" || got.Description != "d" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	byName, err := f.groups.GetGroupByName(ctx, "acme", "eng")
	if err != nil || byName.ID != g.ID {
		t.Fatalf("get by name = %+v, %v", byName, err)
	}
	desc := "new"
	up, err := f.groups.UpdateGroup(ctx, &UpdateGroupRequest{ID: g.ID, Name: "platform", Description: &desc, RoleIDs: []string{}})
	if err != nil || up.Name != "platform" || up.Description != "new" || len(up.RoleIDs) != 0 {
		t.Fatalf("update = %+v, %v", up, err)
	}
	var nf *GroupNotFoundError
	if _, err := f.groups.GetGroupByName(ctx, "acme", "eng"); !errors.As(err, &nf) {
		t.Fatalf("old name still resolves: %v", err)
	}
	// The released old name can be claimed again.
	if _, err := f.groups.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "eng"}); err != nil {
		t.Fatalf("reclaim old name: %v", err)
	}
	if err := f.groups.DeleteGroup(ctx, g.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(f.revoker.calls) != 1 || f.revoker.calls[0] != "acme/"+g.ID {
		t.Fatalf("revoker calls = %v", f.revoker.calls)
	}
	if _, err := f.groups.GetGroup(ctx, g.ID); !errors.As(err, &nf) {
		t.Fatalf("get after delete: %v", err)
	}
	if _, err := f.groups.GetGroupByName(ctx, "acme", "platform"); !errors.As(err, &nf) {
		t.Fatalf("name after delete: %v", err)
	}
	if err := f.groups.DeleteGroup(ctx, g.ID); !errors.As(err, &nf) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestGroupStore_DuplicateNameRefused(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.groups.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "eng"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	var taken *GroupNameTakenError
	if _, err := f.groups.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "eng"}); !errors.As(err, &taken) {
		t.Fatalf("duplicate create: %v", err)
	}
	other, err := f.groups.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "ops"})
	if err != nil {
		t.Fatalf("create ops: %v", err)
	}
	if _, err := f.groups.UpdateGroup(ctx, &UpdateGroupRequest{ID: other.ID, Name: "eng"}); !errors.As(err, &taken) {
		t.Fatalf("rename onto taken name: %v", err)
	}
	// The same name in another tenant is a different group.
	if _, err := f.groups.CreateGroup(ctx, &CreateGroupRequest{TenantID: "other", Name: "eng"}); err != nil {
		t.Fatalf("same name other tenant: %v", err)
	}
	var invalid *ValidationError
	if _, err := f.groups.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme"}); !errors.As(err, &invalid) {
		t.Fatalf("empty name: %v", err)
	}
}

func TestGroupStore_CrossTenantRoleRefused(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var wrong *RoleNotInTenantError
	if _, err := f.groups.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "eng", RoleIDs: []string{f.otherRole}}); !errors.As(err, &wrong) {
		t.Fatalf("create with other tenant's role: %v", err)
	}
	if _, err := f.groups.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "eng", RoleIDs: []string{"no-such-role"}}); !errors.As(err, &wrong) {
		t.Fatalf("create with unknown role: %v", err)
	}
	g, err := f.groups.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "eng"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.groups.UpdateGroup(ctx, &UpdateGroupRequest{ID: g.ID, RoleIDs: []string{f.otherRole}}); !errors.As(err, &wrong) {
		t.Fatalf("update with other tenant's role: %v", err)
	}
}

func TestGroupStore_TenantIsolationAndPagination(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for i := range 5 {
		if _, err := f.groups.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: fmt.Sprintf("g%d", i)}); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	foreign, err := f.groups.CreateGroup(ctx, &CreateGroupRequest{TenantID: "other", Name: "x"})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	seen := map[string]bool{}
	for page := 1; page <= 3; page++ {
		resp, err := f.groups.ListGroups(ctx, &ListGroupsRequest{TenantID: "acme", Page: page, PageSize: 2})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if resp.Total != 5 || resp.HasMore != (page < 3) {
			t.Fatalf("page %d = total %d hasMore %v", page, resp.Total, resp.HasMore)
		}
		for _, g := range resp.Groups {
			seen[g.ID] = true
		}
	}
	if len(seen) != 5 {
		t.Fatalf("pages covered %d groups, want 5", len(seen))
	}
	var nf *GroupNotFoundError
	if _, err := f.groups.GetGroup(tenant.WithTenantID(ctx, "acme"), foreign.ID); !errors.As(err, &nf) {
		t.Fatalf("acme read other tenant's group: %v", err)
	}
}

func TestGroupStore_DeleteKeepsRecordWhenRevokeFails(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	g, err := f.groups.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "eng"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	f.revoker.err = errors.New("boom")
	if err := f.groups.DeleteGroup(ctx, g.ID); err == nil {
		t.Fatal("delete succeeded with a failing revoker")
	}
	if _, err := f.groups.GetGroup(ctx, g.ID); err != nil {
		t.Fatalf("record gone after a failed revoke: %v", err)
	}
}
