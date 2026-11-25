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
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
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

// racingKV rewrites the record under key once, right before the first
// CompareAndSwap on it, standing in for a writer that commits between an
// update's read and its commit.
type racingKV struct {
	*kvstore.MemoryKVStore
	raced bool
}

func (r *racingKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if !r.raced {
		r.raced = true
		data, err := r.Get(ctx, key)
		if err != nil {
			return false, err
		}
		var g Group
		if err := json.Unmarshal(data, &g); err != nil {
			return false, err
		}
		g.Description = "raced"
		data, _ = json.Marshal(&g)
		if err := r.Put(ctx, key, data); err != nil {
			return false, err
		}
	}
	return r.MemoryKVStore.CompareAndSwap(ctx, key, expected, next)
}

// TestGroupStore_UpdateBoundToCommittedVersion pins that an update's Guard
// sees the record the write commits over, that a Guard error writes
// nothing, and that a record changed between read and commit fails with
// *GroupConflictError instead of being overwritten.
func TestGroupStore_UpdateBoundToCommittedVersion(t *testing.T) {
	ctx := context.Background()
	t.Run("guard error writes nothing", func(t *testing.T) {
		f := newFixture(t)
		g, err := f.groups.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "eng"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		refused := errors.New("refused")
		var seen [2][]string
		_, err = f.groups.UpdateGroup(ctx, &UpdateGroupRequest{
			ID: g.ID, Name: "ops", RoleIDs: []string{f.acmeRole},
			Guard: func(prior, next *Group) error {
				seen = [2][]string{prior.RoleIDs, next.RoleIDs}
				return refused
			},
		})
		if !errors.Is(err, refused) {
			t.Fatalf("err = %v, want the guard's error", err)
		}
		if len(seen[0]) != 0 || len(seen[1]) != 1 || seen[1][0] != f.acmeRole {
			t.Fatalf("guard saw prior=%v next=%v", seen[0], seen[1])
		}
		got, err := f.groups.GetGroup(ctx, g.ID)
		if err != nil || got.Name != "eng" || len(got.RoleIDs) != 0 {
			t.Fatalf("after refused update: %+v, %v", got, err)
		}
		if _, err := f.groups.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "ops"}); err != nil {
			t.Fatalf("refused rename kept the name claimed: %v", err)
		}
	})
	t.Run("lost race conflicts", func(t *testing.T) {
		kv := &racingKV{MemoryKVStore: kvstore.NewMemoryKVStore()}
		s, err := NewStore(kv)
		if err != nil {
			t.Fatalf("group store: %v", err)
		}
		g, err := s.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "eng"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		var conflict *GroupConflictError
		if _, err := s.UpdateGroup(ctx, &UpdateGroupRequest{ID: g.ID, Name: "ops"}); !errors.As(err, &conflict) {
			t.Fatalf("err = %v, want *GroupConflictError", err)
		}
		got, err := s.GetGroup(ctx, g.ID)
		if err != nil || got.Name != "eng" || got.Description != "raced" {
			t.Fatalf("racing write overwritten: %+v, %v", got, err)
		}
		if _, err := s.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "ops"}); err != nil {
			t.Fatalf("conflicted rename kept the name claimed: %v", err)
		}
		// A retry over the committed version succeeds.
		if _, err := s.UpdateGroup(ctx, &UpdateGroupRequest{ID: g.ID, Description: ptr("again")}); err != nil {
			t.Fatalf("retry: %v", err)
		}
	})
}

// TestGroupStore_DeleteClaimFreezesTheRecord pins that a delete claim
// freezes the group: no update and no member claim commits over it, the
// claim never shows on a read, and DeleteGroup completes the claimed delete.
func TestGroupStore_DeleteClaimFreezesTheRecord(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	g, err := f.groups.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "eng", RoleIDs: []string{f.acmeRole}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var judged []string
	claimed, token, err := f.groups.ClaimGroup(ctx, g.ID, ClaimDelete, func(c *Group) error {
		judged = c.RoleIDs
		return nil
	})
	if err != nil || token == "" || claimed.Claim != nil {
		t.Fatalf("claim = %+v, %q, %v", claimed, token, err)
	}
	if !slices.Equal(judged, []string{f.acmeRole}) {
		t.Fatalf("guard judged roles %v", judged)
	}
	var conflict *GroupConflictError
	if _, err := f.groups.UpdateGroup(ctx, &UpdateGroupRequest{ID: g.ID, RoleIDs: []string{}}); !errors.As(err, &conflict) {
		t.Fatalf("update of a delete-claimed group: %v, want *GroupConflictError", err)
	}
	if _, _, err := f.groups.ClaimGroup(ctx, g.ID, ClaimMember, nil); !errors.As(err, &conflict) {
		t.Fatalf("member claim of a delete-claimed group: %v, want *GroupConflictError", err)
	}
	got, err := f.groups.GetGroup(ctx, g.ID)
	if err != nil || got.Claim != nil || !slices.Equal(got.RoleIDs, []string{f.acmeRole}) {
		t.Fatalf("get of a claimed group = %+v, %v", got, err)
	}
	if got, err := f.groups.GetGroupByName(ctx, "acme", "eng"); err != nil || got.Claim != nil {
		t.Fatalf("get by name of a claimed group = %+v, %v", got, err)
	}
	list, err := f.groups.ListGroups(ctx, &ListGroupsRequest{TenantID: "acme"})
	if err != nil || len(list.Groups) != 1 || list.Groups[0].Claim != nil {
		t.Fatalf("list of a claimed group = %+v, %v", list, err)
	}
	if err := f.groups.DeleteGroup(ctx, g.ID); err != nil {
		t.Fatalf("delete of the claimed group: %v", err)
	}
	var notFound *GroupNotFoundError
	if _, err := f.groups.GetGroup(ctx, g.ID); !errors.As(err, &notFound) {
		t.Fatalf("get after delete: %v, want *GroupNotFoundError", err)
	}
	if err := f.groups.ReleaseClaim(ctx, g.ID, token); err != nil {
		t.Fatalf("release after delete: %v, want a no-op", err)
	}
}

// TestGroupStore_DeleteClaimIsResumable pins that a delete claim left by an
// interrupted delete is resumed by a later delete claim (same token, guard
// judged again) while a member claim on it still conflicts.
func TestGroupStore_DeleteClaimIsResumable(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	g, err := f.groups.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "eng", RoleIDs: []string{f.acmeRole}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, token, err := f.groups.ClaimGroup(ctx, g.ID, ClaimDelete, nil)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	var judged []string
	resumed, again, err := f.groups.ClaimGroup(ctx, g.ID, ClaimDelete, func(c *Group) error {
		judged = c.RoleIDs
		return nil
	})
	if err != nil || again != token || resumed.Claim != nil {
		t.Fatalf("resumed claim = %+v, %q, %v; want token %q", resumed, again, err, token)
	}
	if !slices.Equal(judged, []string{f.acmeRole}) {
		t.Fatalf("guard judged roles %v", judged)
	}
	refused := errors.New("refused")
	if _, _, err := f.groups.ClaimGroup(ctx, g.ID, ClaimDelete, func(*Group) error { return refused }); !errors.Is(err, refused) {
		t.Fatalf("resumed claim with a refusing guard: %v, want the guard error", err)
	}
	var conflict *GroupConflictError
	if _, _, err := f.groups.ClaimGroup(ctx, g.ID, ClaimMember, nil); !errors.As(err, &conflict) {
		t.Fatalf("member claim of a delete-claimed group: %v, want *GroupConflictError", err)
	}
	h, err := f.groups.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "ops"})
	if err != nil {
		t.Fatalf("create ops: %v", err)
	}
	if _, _, err := f.groups.ClaimGroup(ctx, h.ID, ClaimMember, nil); err != nil {
		t.Fatalf("member claim: %v", err)
	}
	if _, _, err := f.groups.ClaimGroup(ctx, h.ID, ClaimDelete, nil); !errors.As(err, &conflict) {
		t.Fatalf("delete claim of a member-claimed group: %v, want *GroupConflictError", err)
	}
}

// TestGroupStore_ClaimBoundToReadVersion pins that a claim commits only over
// the record its guard judged: a write landing while the guard runs fails
// the claim with *GroupConflictError and leaves the group unclaimed.
func TestGroupStore_ClaimBoundToReadVersion(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	g, err := f.groups.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "eng"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var conflict *GroupConflictError
	_, _, err = f.groups.ClaimGroup(ctx, g.ID, ClaimMember, func(*Group) error {
		_, uerr := f.groups.UpdateGroup(ctx, &UpdateGroupRequest{ID: g.ID, RoleIDs: []string{f.acmeRole}})
		return uerr
	})
	if !errors.As(err, &conflict) {
		t.Fatalf("claim over a record changed under its guard: %v, want *GroupConflictError", err)
	}
	refused := errors.New("refused")
	if _, _, err := f.groups.ClaimGroup(ctx, g.ID, ClaimMember, func(*Group) error { return refused }); !errors.Is(err, refused) {
		t.Fatalf("claim with a refusing guard: %v, want the guard's error", err)
	}
	_, token, err := f.groups.ClaimGroup(ctx, g.ID, ClaimMember, nil)
	if err != nil {
		t.Fatalf("claim of the unclaimed group: %v", err)
	}
	if _, err := f.groups.UpdateGroup(ctx, &UpdateGroupRequest{ID: g.ID, RoleIDs: []string{}}); !errors.As(err, &conflict) {
		t.Fatalf("update of a member-claimed group: %v, want *GroupConflictError", err)
	}
	if err := f.groups.DeleteGroup(ctx, g.ID); !errors.As(err, &conflict) {
		t.Fatalf("delete of a member-claimed group: %v, want *GroupConflictError", err)
	}
	var lapsed *ClaimLapsedError
	if err := f.groups.ReleaseClaim(ctx, g.ID, "not-the-token"); !errors.As(err, &lapsed) {
		t.Fatalf("release with a foreign token: %v, want *ClaimLapsedError", err)
	}
	if err := f.groups.ReleaseClaim(ctx, g.ID, token); err != nil {
		t.Fatalf("release: %v", err)
	}
	got, err := f.groups.UpdateGroup(ctx, &UpdateGroupRequest{ID: g.ID, RoleIDs: []string{}})
	if err != nil || !slices.Equal(got.RoleIDs, []string{}) {
		t.Fatalf("update after release = %+v, %v", got, err)
	}
}

func TestGroupStore_MemberClaimLapses(t *testing.T) {
	ctx := context.Background()
	fake := clocktest.New()
	s, err := NewStore(kvstore.NewMemoryKVStore(), WithClock(fake))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	g, err := s.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "eng"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, held, err := s.ClaimGroup(ctx, g.ID, ClaimMember, nil)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := s.ReleaseClaim(ctx, g.ID, held); err != nil {
		t.Fatalf("release of a live claim: %v", err)
	}
	_, token, err := s.ClaimGroup(ctx, g.ID, ClaimMember, nil)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	fake.Advance(claimLease + time.Second)
	if _, err := s.UpdateGroup(ctx, &UpdateGroupRequest{ID: g.ID, RoleIDs: []string{}}); err != nil {
		t.Fatalf("update over a lapsed claim: %v", err)
	}
	var lapsed *ClaimLapsedError
	if err := s.ReleaseClaim(ctx, g.ID, token); !errors.As(err, &lapsed) || lapsed.ID != g.ID {
		t.Fatalf("release of a lapsed claim: %v, want *ClaimLapsedError", err)
	}
}

// TestGroupStore_RevokeRoleStripsGroups pins the role-delete cascade into
// groups: the role leaves every group of its tenant, other roles and other
// tenants' groups are left alone, and a second call changes nothing.
func TestGroupStore_RevokeRoleStripsGroups(t *testing.T) {
	ctx := context.Background()
	kv := kvstore.NewMemoryKVStore()
	roles, err := role.NewStore(kvstore.NewMemoryKVStore())
	if err != nil {
		t.Fatalf("role store: %v", err)
	}
	mk := func(tenantID, name string) string {
		r, err := roles.CreateRole(ctx, &role.CreateRoleRequest{
			Name: name, TenantID: tenantID, Permissions: []permission.Permission{permission.RolesRead},
		})
		if err != nil {
			t.Fatalf("create role: %v", err)
		}
		return r.ID
	}
	doomed, kept, foreign := mk("acme", "doomed"), mk("acme", "kept"), mk("other", "doomed")
	s, err := NewStore(kv, WithRoleLookup(roles))
	if err != nil {
		t.Fatalf("group store: %v", err)
	}
	g1, err := s.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "a", RoleIDs: []string{doomed, kept}})
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	g2, err := s.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "b", RoleIDs: []string{doomed}})
	if err != nil {
		t.Fatalf("create b: %v", err)
	}
	g3, err := s.CreateGroup(ctx, &CreateGroupRequest{TenantID: "other", Name: "c", RoleIDs: []string{foreign}})
	if err != nil {
		t.Fatalf("create c: %v", err)
	}
	rv, err := NewRoleRevoker(kv)
	if err != nil {
		t.Fatalf("NewRoleRevoker: %v", err)
	}
	if n, err := rv.RevokeRole(ctx, "acme", doomed); err != nil || n != 2 {
		t.Fatalf("RevokeRole = %d, %v; want 2, nil", n, err)
	}
	if n, err := rv.RevokeRole(ctx, "acme", doomed); err != nil || n != 0 {
		t.Fatalf("second RevokeRole = %d, %v; want 0, nil", n, err)
	}
	// The other tenant's group holds its own role; revoking acme's doomed
	// role in "other" (a role id it does not carry) changes nothing.
	if n, err := rv.RevokeRole(ctx, "other", doomed); err != nil || n != 0 {
		t.Fatalf("RevokeRole in other = %d, %v; want 0, nil", n, err)
	}
	want := map[string][]string{g1.ID: {kept}, g2.ID: {}, g3.ID: {foreign}}
	for id, roleIDs := range want {
		got, err := s.GetGroup(ctx, id)
		if err != nil {
			t.Fatalf("GetGroup(%s): %v", id, err)
		}
		if fmt.Sprint(got.RoleIDs) != fmt.Sprint(roleIDs) {
			t.Fatalf("group %s RoleIDs = %v, want %v", got.Name, got.RoleIDs, roleIDs)
		}
	}
}

func ptr[T any](v T) *T { return &v }

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

func TestListGroups_HugePageAndPageSizeReturnEmptyPage(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for i := range 3 {
		if _, err := f.groups.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: fmt.Sprintf("g%d", i)}); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	for _, tc := range []struct {
		name           string
		page, pageSize int
	}{
		{"huge page", math.MaxInt, 2},
		{"huge page size", 2, math.MaxInt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := f.groups.ListGroups(ctx, &ListGroupsRequest{TenantID: "acme", Page: tc.page, PageSize: tc.pageSize})
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(resp.Groups) != 0 || resp.Total != 3 || resp.HasMore {
				t.Fatalf("got %d groups total %d hasMore %v, want empty page of 3", len(resp.Groups), resp.Total, resp.HasMore)
			}
		})
	}
	resp, err := f.groups.ListGroups(ctx, &ListGroupsRequest{TenantID: "acme", Page: 1, PageSize: math.MaxInt})
	if err != nil || len(resp.Groups) != 3 || resp.HasMore {
		t.Fatalf("first page with huge page size = %+v, %v", resp, err)
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

// nonAtomicKV hides the AtomicKVStore methods of the store it wraps, so a
// kvGroupStore built over it directly (NewStore refuses one) reaches the
// write paths with a store that cannot claim a name.
type nonAtomicKV struct {
	kvstore.KVStore
}

func TestGroupStore_WritesRefuseNonAtomicStore(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	existing, err := f.groups.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "eng"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	base, ok := f.groups.(*kvGroupStore)
	if !ok {
		t.Fatalf("fixture store is %T, want *kvGroupStore", f.groups)
	}
	s := &kvGroupStore{store: nonAtomicKV{KVStore: base.store}, roles: base.roles, revoker: base.revoker, identity: base.identity}
	if _, ok := s.store.(kvstore.AtomicKVStore); ok {
		t.Fatal("double still satisfies AtomicKVStore")
	}
	t.Run("CreateGroup", func(t *testing.T) {
		if _, err := s.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "ops"}); !errors.Is(err, ErrStoreNotAtomic) {
			t.Fatalf("create = %v, want ErrStoreNotAtomic", err)
		}
	})
	t.Run("UpdateGroup", func(t *testing.T) {
		if _, err := s.UpdateGroup(ctx, &UpdateGroupRequest{ID: existing.ID, Name: "platform"}); !errors.Is(err, ErrStoreNotAtomic) {
			t.Fatalf("update = %v, want ErrStoreNotAtomic", err)
		}
	})
	t.Run("DeleteGroup", func(t *testing.T) {
		if err := s.DeleteGroup(ctx, existing.ID); !errors.Is(err, ErrStoreNotAtomic) {
			t.Fatalf("delete = %v, want ErrStoreNotAtomic", err)
		}
		if len(f.revoker.calls) != 0 {
			t.Fatalf("members revoked before the refusal: %v", f.revoker.calls)
		}
	})
}

var errFault = errors.New("injected store fault")

// faultKV is a memory store whose operations fail with errFault when the
// key starts with the prefix registered for the operation in fail; with
// casFalse every CompareAndSwap reports a lost race.
type faultKV struct {
	*kvstore.MemoryKVStore
	fail     map[string]string
	casFalse bool
}

func newFaultKV() *faultKV {
	return &faultKV{MemoryKVStore: kvstore.NewMemoryKVStore(), fail: map[string]string{}}
}

func (f *faultKV) faulted(op, key string) bool {
	p, ok := f.fail[op]
	return ok && strings.HasPrefix(key, p)
}

func (f *faultKV) Get(ctx context.Context, key string) ([]byte, error) {
	if f.faulted("get", key) {
		return nil, errFault
	}
	return f.MemoryKVStore.Get(ctx, key)
}

func (f *faultKV) Put(ctx context.Context, key string, value []byte) error {
	if f.faulted("put", key) {
		return errFault
	}
	return f.MemoryKVStore.Put(ctx, key, value)
}

func (f *faultKV) Delete(ctx context.Context, key string) error {
	if f.faulted("delete", key) {
		return errFault
	}
	return f.MemoryKVStore.Delete(ctx, key)
}

func (f *faultKV) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	if f.faulted("scan", prefix) {
		return errFault
	}
	return f.MemoryKVStore.Scan(ctx, prefix, fn)
}

func (f *faultKV) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if f.faulted("pia", key) {
		return false, errFault
	}
	return f.MemoryKVStore.PutIfAbsent(ctx, key, value)
}

func (f *faultKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if f.faulted("cas", key) {
		return false, errFault
	}
	if f.casFalse {
		return false, nil
	}
	return f.MemoryKVStore.CompareAndSwap(ctx, key, expected, next)
}

func (f *faultKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if f.faulted("cad", key) {
		return false, errFault
	}
	return f.MemoryKVStore.CompareAndDelete(ctx, key, expected)
}

// fakeIdentity is an identity index whose answers are set by its fields.
type fakeIdentity struct {
	tenant.IdentityIndex
	token     string
	recordErr error
	lookupErr error
	claim     tenant.Claim
	held      bool
	forgetErr error
	forgot    []string
}

func (f *fakeIdentity) Record(context.Context, tenant.IdentityKind, string, string) (string, error) {
	return f.token, f.recordErr
}

func (f *fakeIdentity) Forget(_ context.Context, _ tenant.IdentityKind, _, token string) error {
	f.forgot = append(f.forgot, token)
	return f.forgetErr
}

func (f *fakeIdentity) Lookup(context.Context, tenant.IdentityKind, string) (tenant.Claim, bool, error) {
	return f.claim, f.held, f.lookupErr
}

// stubRoles answers every role lookup with r and err.
type stubRoles struct {
	r   *role.Role
	err error
}

func (s stubRoles) GetRole(context.Context, string) (*role.Role, error) { return s.r, s.err }

func wantErr[T error](t *testing.T, what string, err error) {
	t.Helper()
	var target T
	if !errors.As(err, &target) {
		t.Fatalf("%s = %v, want %T", what, err, target)
	}
}

func TestGroupErrors_Messages(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&GroupNotFoundError{ID: "g1"}, `group "g1" not found`},
		{&GroupNotFoundError{Name: "eng", TenantID: "acme"}, `group "eng" not found in tenant "acme"`},
		{&GroupConflictError{ID: "g1"}, "group: g1 changed concurrently; retry"},
		{&ClaimLapsedError{ID: "g1"}, "group: g1: the member claim lapsed before the change it guarded committed; retry"},
		{&GroupNameTakenError{Name: "eng", TenantID: "acme"}, `group name "eng" already exists in tenant "acme"`},
		{&RoleNotInTenantError{RoleID: "r1", TenantID: "acme"}, `role "r1" is not a role of tenant "acme"`},
		{&ValidationError{Field: "name", Reason: "bad"}, "group: invalid name: bad"},
		{&StoreError{Op: "get", Detail: "lookup", Err: errFault}, "group store get: lookup: injected store fault"},
		{&StoreError{Op: "new", Detail: "nil"}, "group store new: nil"},
	} {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("%T.Error() = %q, want %q", tc.err, got, tc.want)
		}
	}
	if !errors.Is(&StoreError{Op: "get", Err: errFault}, errFault) {
		t.Error("StoreError does not unwrap to its cause")
	}
	if (&StoreError{}).Unwrap() != nil {
		t.Error("StoreError without a cause unwraps to non-nil")
	}
}

func TestNewStore_Refusals(t *testing.T) {
	_, err := NewStore(nil)
	wantErr[*StoreError](t, "NewStore(nil)", err)
	if _, err := NewStore(nonAtomicKV{KVStore: kvstore.NewMemoryKVStore()}); !errors.Is(err, ErrStoreNotAtomic) {
		t.Fatalf("NewStore(non-atomic) = %v, want ErrStoreNotAtomic", err)
	}
	svc, err := NewStore(kvstore.NewMemoryKVStore(), WithClock(nil))
	if err != nil || svc.(*kvGroupStore).clock == nil {
		t.Fatalf("NewStore(WithClock(nil)) = %v, %v; want a clock kept", svc, err)
	}
	_, err = NewRoleRevoker(nil)
	wantErr[*StoreError](t, "NewRoleRevoker(nil)", err)
	_, err = NewRoutingRoleRevoker(nil)
	wantErr[*StoreError](t, "NewRoutingRoleRevoker(nil)", err)
}

func TestGroupStore_Validation(t *testing.T) {
	ctx := context.Background()
	kv := kvstore.NewMemoryKVStore()
	acmeRole := stubRoles{r: &role.Role{ID: "r1", TenantID: "acme"}}
	s := &kvGroupStore{store: kv, roles: acmeRole, revoker: &recordingRevoker{}, clock: clocktest.New()}
	_, err := s.CreateGroup(ctx, nil)
	wantErr[*ValidationError](t, "CreateGroup(nil)", err)
	_, err = s.UpdateGroup(ctx, nil)
	wantErr[*ValidationError](t, "UpdateGroup(nil)", err)
	_, err = s.ListGroups(ctx, nil)
	wantErr[*ValidationError](t, "ListGroups(nil)", err)
	_, err = s.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: strings.Repeat("n", 257)})
	wantErr[*ValidationError](t, "257-byte name", err)
	_, err = s.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "eng", RoleIDs: []string{""}})
	wantErr[*ValidationError](t, "empty role id", err)
	g, err := s.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "eng", RoleIDs: []string{"r1", "r1"}})
	if err != nil || !slices.Equal(g.RoleIDs, []string{"r1"}) {
		t.Fatalf("duplicate role ids = %+v, %v; want one r1", g, err)
	}
	for name, roles := range map[string]RoleLookup{
		"no role lookup": nil,
		"deleting role":  stubRoles{r: &role.Role{ID: "r1", TenantID: "acme", Deleting: true}},
	} {
		bare := &kvGroupStore{store: kv, roles: roles, clock: clocktest.New()}
		_, err := bare.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "x", RoleIDs: []string{"r1"}})
		wantErr[*RoleNotInTenantError](t, name, err)
	}
	failing := &kvGroupStore{store: kv, roles: stubRoles{err: errFault}, clock: clocktest.New()}
	_, err = failing.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "x", RoleIDs: []string{"r1"}})
	wantErr[*StoreError](t, "role lookup error", err)
	_, _, err = s.ClaimGroup(ctx, g.ID, "bogus", nil)
	wantErr[*ValidationError](t, "ClaimGroup op bogus", err)
	_, err = s.GetGroup(ctx, "")
	wantErr[*ValidationError](t, "GetGroup empty id", err)
	_, err = s.GetGroupByName(ctx, "acme", "")
	wantErr[*ValidationError](t, "GetGroupByName empty name", err)
	_, err = s.GetGroupByName(ctx, "acme", "nobody")
	wantErr[*GroupNotFoundError](t, "GetGroupByName unknown", err)
	if err := kv.Put(ctx, nameIndexKey("acme", "dangling"), []byte("missing-id")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err = s.GetGroupByName(ctx, "acme", "dangling")
	var nf *GroupNotFoundError
	if !errors.As(err, &nf) || nf.Name != "dangling" {
		t.Fatalf("name index to a missing record = %v, want *GroupNotFoundError by name", err)
	}
	if err := s.ReleaseClaim(ctx, g.ID, ""); err != nil {
		t.Fatalf("ReleaseClaim empty token = %v", err)
	}
	if err := s.ReleaseClaim(ctx, "missing-id", "tok"); err != nil {
		t.Fatalf("ReleaseClaim of a missing group = %v", err)
	}
	resp, err := s.ListGroups(ctx, &ListGroupsRequest{TenantID: "acme"})
	if err != nil || resp.Total != 1 || len(resp.Groups) != 1 {
		t.Fatalf("ListGroups default page size = %+v, %v", resp, err)
	}
	na := &kvGroupStore{store: nonAtomicKV{KVStore: kv}, clock: clocktest.New()}
	if _, _, err := na.ClaimGroup(ctx, g.ID, ClaimMember, nil); !errors.Is(err, ErrStoreNotAtomic) {
		t.Fatalf("ClaimGroup non-atomic = %v", err)
	}
	if err := na.ReleaseClaim(ctx, g.ID, "tok"); !errors.Is(err, ErrStoreNotAtomic) {
		t.Fatalf("ReleaseClaim non-atomic = %v", err)
	}
	if _, err := na.RevokeRole(ctx, "acme", "r1"); !errors.Is(err, ErrStoreNotAtomic) {
		t.Fatalf("RevokeRole non-atomic = %v", err)
	}
}

// newFaultStore returns a store over a fresh faultKV holding one acme group
// "eng" carrying role r1.
func newFaultStore(t *testing.T) (*kvGroupStore, *faultKV, *Group) {
	t.Helper()
	fk := newFaultKV()
	s := &kvGroupStore{store: fk, roles: stubRoles{r: &role.Role{ID: "r1", TenantID: "acme"}}, revoker: &recordingRevoker{}, clock: clocktest.New()}
	g, err := s.CreateGroup(context.Background(), &CreateGroupRequest{TenantID: "acme", Name: "eng", RoleIDs: []string{"r1"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return s, fk, g
}

func TestGroupStore_StoreFaults(t *testing.T) {
	ctx := context.Background()
	create := func(s *kvGroupStore, name string) error {
		_, err := s.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: name})
		return err
	}
	for _, tc := range []struct{ op, prefix string }{
		{"pia", keyPrefixName}, {"put", keyPrefixPrimary}, {"put", keyPrefixTenant},
	} {
		t.Run("CreateGroup "+tc.op+" "+tc.prefix, func(t *testing.T) {
			s, fk, _ := newFaultStore(t)
			fk.fail[tc.op] = tc.prefix
			wantErr[*StoreError](t, "create", create(s, "ops"))
			delete(fk.fail, tc.op)
			if err := create(s, "ops"); err != nil {
				t.Fatalf("create after the fault = %v; the name claim was not released", err)
			}
		})
	}
	t.Run("readGroup", func(t *testing.T) {
		s, fk, g := newFaultStore(t)
		fk.fail["get"] = keyPrefixPrimary
		_, err := s.GetGroup(ctx, g.ID)
		wantErr[*StoreError](t, "get fault", err)
		delete(fk.fail, "get")
		if err := fk.MemoryKVStore.Put(ctx, primaryKey("junk"), []byte("{")); err != nil {
			t.Fatalf("seed: %v", err)
		}
		_, err = s.GetGroup(ctx, "junk")
		wantErr[*StoreError](t, "unmarshal", err)
		fk.fail["get"] = keyPrefixName
		_, err = s.GetGroupByName(ctx, "acme", "eng")
		wantErr[*StoreError](t, "GetGroupByName get fault", err)
	})
	t.Run("ClaimGroup", func(t *testing.T) {
		s, fk, g := newFaultStore(t)
		fk.fail["cas"] = keyPrefixPrimary
		_, _, err := s.ClaimGroup(ctx, g.ID, ClaimMember, nil)
		wantErr[*StoreError](t, "cas fault", err)
		delete(fk.fail, "cas")
		fk.casFalse = true
		_, _, err = s.ClaimGroup(ctx, g.ID, ClaimMember, nil)
		wantErr[*GroupConflictError](t, "lost cas", err)
	})
	t.Run("ReleaseClaim", func(t *testing.T) {
		s, fk, g := newFaultStore(t)
		_, token, err := s.ClaimGroup(ctx, g.ID, ClaimMember, nil)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		fk.fail["cas"] = keyPrefixPrimary
		wantErr[*StoreError](t, "cas fault", s.ReleaseClaim(ctx, g.ID, token))
		delete(fk.fail, "cas")
		fk.casFalse = true
		wantErr[*GroupConflictError](t, "lost cas", s.ReleaseClaim(ctx, g.ID, token))
		fk.casFalse = false
		fk.fail["get"] = keyPrefixPrimary
		wantErr[*StoreError](t, "get fault", s.ReleaseClaim(ctx, g.ID, token))
	})
	t.Run("UpdateGroup", func(t *testing.T) {
		s, fk, g := newFaultStore(t)
		if err := create(s, "taken"); err != nil {
			t.Fatalf("create: %v", err)
		}
		update := func(name string) error {
			_, err := s.UpdateGroup(ctx, &UpdateGroupRequest{ID: g.ID, Name: name})
			return err
		}
		fk.fail["pia"] = keyPrefixName
		wantErr[*StoreError](t, "rename claim fault", update("ops"))
		delete(fk.fail, "pia")
		wantErr[*GroupNameTakenError](t, "rename to a taken name", update("taken"))
		fk.fail["cas"] = keyPrefixPrimary
		wantErr[*StoreError](t, "cas fault", update("ops"))
		delete(fk.fail, "cas")
		if id, _ := fk.MemoryKVStore.Get(ctx, nameIndexKey("acme", "ops")); id != nil {
			t.Fatal("the new name stayed claimed after the failed update")
		}
		fk.fail["cad"] = nameIndexKey("acme", "eng")
		wantErr[*StoreError](t, "old name release fault", update("ops"))
		delete(fk.fail, "cad")
		desc := "platform team"
		got, err := s.UpdateGroup(ctx, &UpdateGroupRequest{ID: g.ID, Description: &desc})
		if err != nil || got.Description != desc || got.Name != "ops" {
			t.Fatalf("description update = %+v, %v", got, err)
		}
	})
	t.Run("DeleteGroup", func(t *testing.T) {
		s, fk, g := newFaultStore(t)
		bare := &kvGroupStore{store: fk, clock: clocktest.New()}
		wantErr[*StoreError](t, "no revoker", bare.DeleteGroup(ctx, g.ID))
		for _, f := range []struct{ op, prefix string }{
			{"cad", keyPrefixName}, {"delete", keyPrefixTenant}, {"delete", keyPrefixPrimary},
		} {
			fk.fail[f.op] = f.prefix
			wantErr[*StoreError](t, "delete "+f.op+" "+f.prefix, s.DeleteGroup(ctx, g.ID))
			delete(fk.fail, f.op)
		}
		if err := s.DeleteGroup(ctx, g.ID); err != nil {
			t.Fatalf("delete after the faults = %v", err)
		}
	})
	t.Run("ListGroups", func(t *testing.T) {
		s, fk, _ := newFaultStore(t)
		fk.fail["scan"] = keyPrefixTenant
		_, err := s.ListGroups(ctx, &ListGroupsRequest{TenantID: "acme"})
		wantErr[*StoreError](t, "scan fault", err)
		delete(fk.fail, "scan")
		if err := fk.MemoryKVStore.Put(ctx, tenantIndexKey("acme", "ghost"), []byte("ghost")); err != nil {
			t.Fatalf("seed: %v", err)
		}
		_, err = s.ListGroups(ctx, &ListGroupsRequest{TenantID: "acme"})
		wantErr[*GroupNotFoundError](t, "index entry for a missing record", err)
	})
}

func TestGroupStore_IdentityIndex(t *testing.T) {
	ctx := context.Background()
	t.Run("record error releases the name", func(t *testing.T) {
		idx := &fakeIdentity{recordErr: errFault}
		s := &kvGroupStore{store: kvstore.NewMemoryKVStore(), identity: idx, clock: clocktest.New()}
		_, err := s.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "eng"})
		wantErr[*StoreError](t, "record error", err)
		idx.recordErr = nil
		if _, err := s.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "eng"}); err != nil {
			t.Fatalf("create after the record error = %v", err)
		}
	})
	t.Run("failed create forgets its claim", func(t *testing.T) {
		idx := &fakeIdentity{token: "tok"}
		fk := newFaultKV()
		fk.fail["put"] = keyPrefixPrimary
		s := &kvGroupStore{store: fk, identity: idx, clock: clocktest.New()}
		_, err := s.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "eng"})
		wantErr[*StoreError](t, "put fault", err)
		if !slices.Equal(idx.forgot, []string{"tok"}) {
			t.Fatalf("forgotten tokens = %v, want [tok]", idx.forgot)
		}
	})
	t.Run("forgetIdentity", func(t *testing.T) {
		stale := &tenant.IdentityClaimStaleError{Kind: tenant.IdentityGroup, ID: "g1"}
		for _, tc := range []struct {
			name     string
			idx      *fakeIdentity
			tenantID string
			check    func(error) bool
		}{
			{"lookup error", &fakeIdentity{lookupErr: errFault}, "acme", func(err error) bool { return errors.Is(err, errFault) }},
			{"not held", &fakeIdentity{}, "acme", func(err error) bool { return err == nil }},
			{"system tenant", &fakeIdentity{held: true, claim: tenant.Claim{Tenant: tenantid.System, Token: "t"}}, "", func(err error) bool { return err == nil }},
			{"other tenant", &fakeIdentity{held: true, claim: tenant.Claim{Tenant: "other", Token: "t"}}, "acme", func(err error) bool {
				var c *tenant.IdentityClaimConflictError
				return errors.As(err, &c)
			}},
			{"stale forget", &fakeIdentity{held: true, claim: tenant.Claim{Tenant: "acme", Token: "t"}, forgetErr: stale}, "acme", func(err error) bool { return err == nil }},
			{"forget error", &fakeIdentity{held: true, claim: tenant.Claim{Tenant: "acme", Token: "t"}, forgetErr: errFault}, "acme", func(err error) bool { return errors.Is(err, errFault) }},
		} {
			s := &kvGroupStore{identity: tc.idx}
			if err := s.forgetIdentity(ctx, "g1", tc.tenantID); !tc.check(err) {
				t.Errorf("%s: forgetIdentity = %v", tc.name, err)
			}
		}
	})
	t.Run("delete reports an identity failure", func(t *testing.T) {
		idx := &fakeIdentity{}
		s := &kvGroupStore{store: kvstore.NewMemoryKVStore(), identity: idx, revoker: &recordingRevoker{}, clock: clocktest.New()}
		g, err := s.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "eng"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		idx.lookupErr = errFault
		wantErr[*StoreError](t, "delete", s.DeleteGroup(ctx, g.ID))
	})
}

func TestGroupStore_RevokeRoleFaults(t *testing.T) {
	ctx := context.Background()
	revoke := func(s *kvGroupStore) (int, error) { return s.RevokeRole(ctx, "acme", "r1") }
	t.Run("scan", func(t *testing.T) {
		s, fk, _ := newFaultStore(t)
		fk.fail["scan"] = keyPrefixTenant
		_, err := revoke(s)
		wantErr[*StoreError](t, "scan fault", err)
	})
	t.Run("get", func(t *testing.T) {
		s, fk, _ := newFaultStore(t)
		fk.fail["get"] = keyPrefixPrimary
		_, err := revoke(s)
		wantErr[*StoreError](t, "get fault", err)
	})
	t.Run("missing and garbage records", func(t *testing.T) {
		s, fk, _ := newFaultStore(t)
		if err := fk.MemoryKVStore.Put(ctx, tenantIndexKey("acme", "0-ghost"), []byte("0-ghost")); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if n, err := revoke(s); err != nil || n != 1 {
			t.Fatalf("revoke past a missing record = %d, %v; want 1, nil", n, err)
		}
		if err := fk.MemoryKVStore.Put(ctx, primaryKey("0-ghost"), []byte("{")); err != nil {
			t.Fatalf("seed: %v", err)
		}
		_, err := revoke(s)
		wantErr[*StoreError](t, "unmarshal", err)
	})
	t.Run("cas", func(t *testing.T) {
		s, fk, _ := newFaultStore(t)
		fk.fail["cas"] = keyPrefixPrimary
		_, err := revoke(s)
		wantErr[*StoreError](t, "cas fault", err)
		delete(fk.fail, "cas")
		fk.casFalse = true
		_, err = revoke(s)
		wantErr[*GroupConflictError](t, "lost cas", err)
	})
}
