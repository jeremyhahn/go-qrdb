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

package quic

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/group"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

func quicGroupCall(t *testing.T, h func(context.Context, []byte) ([]byte, error), req any, out any) {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := h(tenant.WithTenantID(context.Background(), "acme"), payload)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if out != nil {
		if err := json.Unmarshal(resp, out); err != nil {
			t.Fatalf("decode %s: %v", resp, err)
		}
	}
}

func TestGroupHandlers_LifecycleAndMembership(t *testing.T) {
	gen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatal(err)
	}
	users, err := user.NewStore(kvstore.NewMemoryKVStore(), gen)
	if err != nil {
		t.Fatal(err)
	}
	members, err := user.NewGroupMembership(users)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := group.NewStore(kvstore.NewMemoryKVStore(), group.WithMemberRevoker(members))
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(nil, nil, "localhost:0", WithUserService(users), WithGroupService(groups))

	var g groupWireResponse
	quicGroupCall(t, s.handleCreateGroup, createGroupWireRequest{TenantID: "acme", Name: "eng"}, &g)
	if g.ID == "" || g.TenantID != "acme" {
		t.Fatalf("create = %+v", g)
	}
	quicGroupCall(t, s.handleGetGroup, groupIDWireRequest{ID: g.ID}, nil)
	quicGroupCall(t, s.handleGetGroupByName, getGroupByNameWireRequest{TenantID: "acme", Name: "eng"}, nil)
	desc := "engineering"
	quicGroupCall(t, s.handleUpdateGroup, updateGroupWireRequest{ID: g.ID, Description: &desc}, nil)
	var list listGroupsWireResponse
	quicGroupCall(t, s.handleListGroups, listGroupsWireRequest{TenantID: "acme"}, &list)
	if list.Total != 1 || list.Groups[0].Description != desc {
		t.Fatalf("list = %+v", list)
	}

	alice, err := users.CreateUser(context.Background(), &user.CreateUserRequest{WithoutEmailLoginKey: true, Username: "alice", TenantID: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	quicGroupCall(t, s.handleAddGroupMember, groupMemberWireRequest{UserID: alice.ID, GroupID: g.ID}, nil)
	var ms listGroupMembersWireResponse
	quicGroupCall(t, s.handleListGroupMembers, groupMemberWireRequest{GroupID: g.ID}, &ms)
	if len(ms.Members) != 1 || ms.Members[0].UserID != alice.ID {
		t.Fatalf("members = %+v", ms)
	}
	quicGroupCall(t, s.handleRemoveGroupMember, groupMemberWireRequest{UserID: alice.ID, GroupID: g.ID}, nil)
	var st statusWireResponse
	quicGroupCall(t, s.handleDeleteGroup, groupIDWireRequest{ID: g.ID}, &st)
	if st.Status != "ok" {
		t.Fatalf("delete status = %q", st.Status)
	}
	payload, _ := json.Marshal(groupIDWireRequest{ID: g.ID})
	var nf *group.GroupNotFoundError
	if _, err := s.handleGetGroup(tenant.WithTenantID(context.Background(), "acme"), payload); !errors.As(err, &nf) {
		t.Fatalf("get deleted: %v, want *GroupNotFoundError", err)
	}
}

func TestGroupHandlers_NotConfigured(t *testing.T) {
	s := NewServer(nil, nil, "localhost:0")
	var nc *ServiceNotConfiguredError
	if _, err := s.handleCreateGroup(context.Background(), []byte(`{"name":"eng"}`)); !errors.As(err, &nc) {
		t.Fatalf("create without stores: %v, want *ServiceNotConfiguredError", err)
	}
}
