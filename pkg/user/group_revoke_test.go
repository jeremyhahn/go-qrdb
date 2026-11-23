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
	"errors"
	"slices"
	"testing"
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
