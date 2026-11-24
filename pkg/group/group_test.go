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
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
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
