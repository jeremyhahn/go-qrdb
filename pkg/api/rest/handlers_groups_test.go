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

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/group"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

func newGroupTestServer(t *testing.T) (*Server, user.UserServicer) {
	t.Helper()
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
	return NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithUserService(users), WithGroupService(groups)), users
}

func doGroupRequest(t *testing.T, h http.HandlerFunc, method, path string, body any, want int) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(tenant.WithTenantID(context.Background(), "acme"))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != want {
		t.Fatalf("%s %s = %d (%s), want %d", method, path, rec.Code, rec.Body.String(), want)
	}
	return rec
}

func TestGroupHandlers_LifecycleAndMembership(t *testing.T) {
	s, users := newGroupTestServer(t)

	var g groupResponse
	rec := doGroupRequest(t, s.handleCreateGroup, http.MethodPost, "/groups", createGroupRequest{TenantID: "acme", Name: "eng"}, http.StatusCreated)
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil || g.ID == "" {
		t.Fatalf("create = %+v, %v", g, err)
	}
	doGroupRequest(t, s.handleCreateGroup, http.MethodPost, "/groups", createGroupRequest{TenantID: "acme", Name: "eng"}, http.StatusConflict)
	doGroupRequest(t, s.handleGroup, http.MethodGet, "/groups/"+g.ID, nil, http.StatusOK)
	doGroupRequest(t, s.handleGetGroupByName, http.MethodGet, "/groups/by-name?tenantID=acme&name=eng", nil, http.StatusOK)
	desc := "engineering"
	doGroupRequest(t, s.handleGroup, http.MethodPut, "/groups/"+g.ID, updateGroupRequest{Description: &desc}, http.StatusOK)

	var list listGroupsResponse
	rec = doGroupRequest(t, s.handleListGroups, http.MethodGet, "/groups/list?tenantID=acme", nil, http.StatusOK)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || list.Total != 1 || list.Groups[0].Description != desc {
		t.Fatalf("list = %+v, %v", list, err)
	}

	alice, err := users.CreateUser(context.Background(), &user.CreateUserRequest{WithoutEmailLoginKey: true, Username: "alice", TenantID: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	doGroupRequest(t, s.handleAddGroupMember, http.MethodPost, "/groups/members/add", groupMemberRequest{UserID: alice.ID, GroupID: g.ID}, http.StatusNoContent)
	var ms listGroupMembersResponse
	rec = doGroupRequest(t, s.handleListGroupMembers, http.MethodGet, fmt.Sprintf("/groups/members?groupID=%s", g.ID), nil, http.StatusOK)
	if err := json.Unmarshal(rec.Body.Bytes(), &ms); err != nil || len(ms.Members) != 1 || ms.Members[0].UserID != alice.ID {
		t.Fatalf("members = %+v, %v", ms, err)
	}
	doGroupRequest(t, s.handleRemoveGroupMember, http.MethodPost, "/groups/members/remove", groupMemberRequest{UserID: alice.ID, GroupID: g.ID}, http.StatusNoContent)
	doGroupRequest(t, s.handleGroup, http.MethodDelete, "/groups/"+g.ID, nil, http.StatusNoContent)
	doGroupRequest(t, s.handleGroup, http.MethodGet, "/groups/"+g.ID, nil, http.StatusNotFound)
}

func TestGroupHandlers_NotConfigured(t *testing.T) {
	s := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	doGroupRequest(t, s.handleCreateGroup, http.MethodPost, "/groups", createGroupRequest{Name: "eng"}, http.StatusNotImplemented)
}
