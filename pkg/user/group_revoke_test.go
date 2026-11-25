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
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

func TestGroupMembership_AddRemoveRevoke(t *testing.T) {
	ctx := context.Background()
	svc := newTestStore(t)
	gm, err := NewGroupMembership(svc)
	if err != nil {
		t.Fatalf("NewGroupMembership: %v", err)
	}
	mk := func(name, tenantID string) *User {
		u, err := svc.CreateUser(ctx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: name, TenantID: tenantID})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return u
	}
	alice, bob, eve := mk("alice", "acme"), mk("bob", "acme"), mk("eve", "other")

	for _, u := range []*User{alice, bob} {
		if changed, err := gm.AddMember(ctx, u.ID, "g-eng"); err != nil || !changed {
			t.Fatalf("add %s: changed=%v err=%v", u.Username, changed, err)
		}
	}
	if changed, err := gm.AddMember(ctx, alice.ID, "g-eng"); err != nil || changed {
		t.Fatalf("re-add: changed=%v err=%v, want unchanged", changed, err)
	}
	if _, err := gm.AddMember(ctx, alice.ID, "g-ops"); err != nil {
		t.Fatalf("add second group: %v", err)
	}
	if _, err := gm.AddMember(ctx, eve.ID, "g-eng"); err != nil {
		t.Fatalf("add eve: %v", err)
	}
	got, err := svc.GetUser(ctx, alice.ID)
	if err != nil || !slices.Equal(got.GroupIDs, []string{"g-eng", "g-ops"}) {
		t.Fatalf("alice GroupIDs = %v, %v", got.GroupIDs, err)
	}

	members, err := gm.Members(ctx, "acme", "g-eng")
	if err != nil || len(members) != 2 || members[0].ID != alice.ID || members[1].ID != bob.ID {
		t.Fatalf("members = %v, %v", members, err)
	}

	if changed, err := gm.RemoveMember(ctx, bob.ID, "g-eng"); err != nil || !changed {
		t.Fatalf("remove bob: changed=%v err=%v", changed, err)
	}
	if changed, err := gm.RemoveMember(ctx, bob.ID, "g-eng"); err != nil || changed {
		t.Fatalf("remove bob again: changed=%v err=%v, want unchanged", changed, err)
	}

	n, err := gm.RevokeGroup(ctx, "acme", "g-eng")
	if err != nil || n != 1 {
		t.Fatalf("revoke = %d, %v; want 1 (alice only)", n, err)
	}
	got, _ = svc.GetUser(ctx, alice.ID)
	if !slices.Equal(got.GroupIDs, []string{"g-ops"}) {
		t.Fatalf("alice GroupIDs after revoke = %v", got.GroupIDs)
	}
	// Revocation is per tenant: the other tenant's member keeps the ID.
	got, _ = svc.GetUser(ctx, eve.ID)
	if !slices.Equal(got.GroupIDs, []string{"g-eng"}) {
		t.Fatalf("eve GroupIDs after acme revoke = %v", got.GroupIDs)
	}

	var nf *UserNotFoundError
	if _, err := gm.AddMember(ctx, 999999, "g-eng"); !errors.As(err, &nf) {
		t.Fatalf("add unknown user: %v, want *UserNotFoundError", err)
	}
}

// TestGroupMembership_PendingJoins pins the pending-join lifecycle: a pending
// entry stays out of GroupIDs, is listed by Members, is cleared by
// RevokeGroup and DropPendingMember, and PromoteMember moves it to GroupIDs.
func TestGroupMembership_PendingJoins(t *testing.T) {
	ctx := context.Background()
	svc := newTestStore(t)
	gm, err := NewGroupMembership(svc)
	if err != nil {
		t.Fatalf("NewGroupMembership: %v", err)
	}
	mk := func(name string) *User {
		u, err := svc.CreateUser(ctx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: name, TenantID: "acme"})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return u
	}
	alice, bob, carol := mk("alice"), mk("bob"), mk("carol")
	get := func(u *User) *User {
		got, err := svc.GetUser(ctx, u.ID)
		if err != nil {
			t.Fatalf("get %s: %v", u.Username, err)
		}
		return got
	}

	if changed, err := gm.AddPendingMember(ctx, alice.ID, "g-eng"); err != nil || !changed {
		t.Fatalf("pending alice: changed=%v err=%v", changed, err)
	}
	if changed, err := gm.AddPendingMember(ctx, alice.ID, "g-eng"); err != nil || changed {
		t.Fatalf("pending alice again: changed=%v err=%v, want unchanged", changed, err)
	}
	if got := get(alice); len(got.GroupIDs) != 0 || !slices.Equal(got.PendingGroupIDs, []string{"g-eng"}) {
		t.Fatalf("alice = GroupIDs %v pending %v; want pending only", got.GroupIDs, got.PendingGroupIDs)
	}
	members, err := gm.Members(ctx, "acme", "g-eng")
	if err != nil || len(members) != 1 || members[0].ID != alice.ID {
		t.Fatalf("members with a pending join = %v, %v; want alice", members, err)
	}
	if n, err := gm.RevokeGroup(ctx, "acme", "g-eng"); err != nil || n != 1 {
		t.Fatalf("revoke = %d, %v; want 1", n, err)
	}
	if got := get(alice); len(got.GroupIDs) != 0 || len(got.PendingGroupIDs) != 0 {
		t.Fatalf("alice after revoke = GroupIDs %v pending %v; want neither", got.GroupIDs, got.PendingGroupIDs)
	}
	if changed, err := gm.PromoteMember(ctx, alice.ID, "g-eng"); err != nil || changed {
		t.Fatalf("promote a revoked join: changed=%v err=%v, want unchanged", changed, err)
	}

	if _, err := gm.AddPendingMember(ctx, bob.ID, "g-eng"); err != nil {
		t.Fatalf("pending bob: %v", err)
	}
	if changed, err := gm.PromoteMember(ctx, bob.ID, "g-eng"); err != nil || !changed {
		t.Fatalf("promote bob: changed=%v err=%v", changed, err)
	}
	if got := get(bob); !slices.Equal(got.GroupIDs, []string{"g-eng"}) || len(got.PendingGroupIDs) != 0 {
		t.Fatalf("bob after promote = GroupIDs %v pending %v", got.GroupIDs, got.PendingGroupIDs)
	}
	if changed, err := gm.AddPendingMember(ctx, bob.ID, "g-eng"); err != nil || changed {
		t.Fatalf("pending join of a member: changed=%v err=%v, want unchanged", changed, err)
	}

	if _, err := gm.AddPendingMember(ctx, carol.ID, "g-eng"); err != nil {
		t.Fatalf("pending carol: %v", err)
	}
	if changed, err := gm.DropPendingMember(ctx, carol.ID, "g-eng"); err != nil || !changed {
		t.Fatalf("drop carol: changed=%v err=%v", changed, err)
	}
	if got := get(carol); len(got.GroupIDs) != 0 || len(got.PendingGroupIDs) != 0 {
		t.Fatalf("carol after drop = GroupIDs %v pending %v; want neither", got.GroupIDs, got.PendingGroupIDs)
	}
}

// foreignServicer is a UserServicer that is not one of this package's stores.
type foreignServicer struct{ UserServicer }

func TestNewGroupMembership_RefusesForeignServicer(t *testing.T) {
	gm, err := NewGroupMembership(foreignServicer{})
	var unsupported *GroupMembershipUnsupportedError
	if gm != nil || !errors.As(err, &unsupported) {
		t.Fatalf("NewGroupMembership = %v, %v; want *GroupMembershipUnsupportedError", gm, err)
	}
	if unsupported.Error() == "" {
		t.Fatal("GroupMembershipUnsupportedError has an empty message")
	}
}

func TestGroupMembership_Validation(t *testing.T) {
	ctx := context.Background()
	svc := newTestStore(t)
	gm, err := NewGroupMembership(svc)
	if err != nil {
		t.Fatalf("NewGroupMembership: %v", err)
	}
	u, err := svc.CreateUser(ctx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "alice", TenantID: "acme"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var verr *ValidationError
	if _, err := gm.AddMember(ctx, u.ID, ""); !errors.As(err, &verr) {
		t.Fatalf("add with empty group: %v, want *ValidationError", err)
	}
	if _, err := gm.AddMember(ctx, 0, "g-eng"); !errors.As(err, &verr) {
		t.Fatalf("add user 0: %v, want *ValidationError", err)
	}
	if _, err := gm.RevokeGroup(ctx, "acme", ""); !errors.As(err, &verr) {
		t.Fatalf("revoke empty group: %v, want *ValidationError", err)
	}
	if _, err := gm.Members(ctx, "acme", ""); !errors.As(err, &verr) {
		t.Fatalf("members of empty group: %v, want *ValidationError", err)
	}
	var nf *UserNotFoundError
	if _, err := gm.PromoteMember(ctx, 999999, "g-eng"); !errors.As(err, &nf) {
		t.Fatalf("promote unknown user: %v, want *UserNotFoundError", err)
	}
	if changed, err := gm.RemoveMember(ctx, u.ID, "g-eng"); err != nil || changed {
		t.Fatalf("remove non-member: changed=%v err=%v, want unchanged", changed, err)
	}
	if changed, err := gm.DropPendingMember(ctx, u.ID, "g-eng"); err != nil || changed {
		t.Fatalf("drop absent pending join: changed=%v err=%v, want unchanged", changed, err)
	}
}

func TestMembershipRequest_TerminalAndUnknownOp(t *testing.T) {
	deleting, err := json.Marshal(&User{ID: 1, TenantID: "acme", Lifecycle: LifecycleDeleting})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if req, err := membershipRequest(1, deleting, "g-eng", opAdd); req != nil || err != nil {
		t.Fatalf("terminal record: req=%v err=%v, want nil, nil", req, err)
	}
	active, err := json.Marshal(&User{ID: 1, TenantID: "acme", Lifecycle: LifecycleActive})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var verr *ValidationError
	if _, err := membershipRequest(1, active, "g-eng", membershipOp(0)); !errors.As(err, &verr) {
		t.Fatalf("unknown op: %v, want *ValidationError", err)
	}
	var serr *StoreError
	if _, err := membershipRequest(1, []byte("{"), "g-eng", opAdd); !errors.As(err, &serr) {
		t.Fatalf("corrupt record: %v, want *StoreError", err)
	}
}

func TestRoutingUserService_GroupMembership(t *testing.T) {
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"acme": newFakeKVStore()},
		sealed:  map[string]bool{},
	}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	gm, err := NewGroupMembership(svc)
	if err != nil {
		t.Fatalf("NewGroupMembership: %v", err)
	}
	ctx := tenant.WithTenantID(context.Background(), "acme")
	mk := func(name string) *User {
		u, err := svc.CreateUser(ctx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: name, TenantID: "acme"})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return u
	}
	alice, bob := mk("alice"), mk("bob")

	if changed, err := gm.AddMember(ctx, alice.ID, "g-eng"); err != nil || !changed {
		t.Fatalf("add alice: changed=%v err=%v", changed, err)
	}
	if changed, err := gm.AddPendingMember(ctx, bob.ID, "g-eng"); err != nil || !changed {
		t.Fatalf("pending bob: changed=%v err=%v", changed, err)
	}
	if changed, err := gm.PromoteMember(ctx, bob.ID, "g-eng"); err != nil || !changed {
		t.Fatalf("promote bob: changed=%v err=%v", changed, err)
	}
	members, err := gm.Members(ctx, "acme", "g-eng")
	if err != nil || len(members) != 2 {
		t.Fatalf("members = %v, %v; want alice and bob", members, err)
	}
	if changed, err := gm.RemoveMember(ctx, bob.ID, "g-eng"); err != nil || !changed {
		t.Fatalf("remove bob: changed=%v err=%v", changed, err)
	}
	if n, err := gm.RevokeGroup(ctx, "acme", "g-eng"); err != nil || n != 1 {
		t.Fatalf("revoke = %d, %v; want 1", n, err)
	}
	var nf *UserNotFoundError
	if _, err := gm.AddMember(ctx, 999999, "g-eng"); !errors.As(err, &nf) {
		t.Fatalf("add unknown user: %v, want *UserNotFoundError", err)
	}

	resolver.sealed["acme"] = true
	if _, err := gm.AddMember(ctx, alice.ID, "g-ops"); err == nil {
		t.Fatal("add member in a sealed tenant succeeded")
	}
	if _, err := gm.RevokeGroup(ctx, "acme", "g-eng"); err == nil {
		t.Fatal("revoke in a sealed tenant succeeded")
	}
	if _, err := gm.Members(ctx, "acme", "g-eng"); err == nil {
		t.Fatal("members in a sealed tenant succeeded")
	}
}
