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
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func escapedPath(method, path string) func(*http.Request) bool {
	return func(r *http.Request) bool {
		return r.Method == method && r.URL.EscapedPath() == path
	}
}

func TestRESTGroups_CRUDPathsAndBodies(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	ctx := context.Background()
	canned := &transport.Group{ID: "g1", TenantID: "t1", Name: "ops", RoleIDs: []string{"r1"}}

	var created transport.CreateGroupRequest
	rs.handle(exactPath(http.MethodPost, "/groups"), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&created)
		writeJSON(w, canned)
	})
	want := transport.CreateGroupRequest{TenantID: "t1", Name: "ops", Description: "d", RoleIDs: []string{"r1", "r2"}}
	g, err := rt.CreateGroup(ctx, &want)
	if err != nil || g.ID != "g1" || !slices.Equal(g.RoleIDs, []string{"r1"}) {
		t.Fatalf("CreateGroup = %+v, %v", g, err)
	}
	if created.TenantID != want.TenantID || created.Name != want.Name || created.Description != want.Description || !slices.Equal(created.RoleIDs, want.RoleIDs) {
		t.Fatalf("CreateGroup body = %+v, want %+v", created, want)
	}

	rs.handle(escapedPath(http.MethodGet, "/groups/a%2Fb"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, canned)
	})
	if g, err := rt.GetGroup(ctx, "a/b"); err != nil || g.Name != "ops" {
		t.Fatalf("GetGroup = %+v, %v", g, err)
	}

	var byName map[string]string
	rs.handle(exactPath(http.MethodGet, "/groups/by-name"), func(w http.ResponseWriter, r *http.Request) {
		byName = map[string]string{"name": r.URL.Query().Get("name"), "tenantID": r.URL.Query().Get("tenantID")}
		writeJSON(w, canned)
	})
	if g, err := rt.GetGroupByName(ctx, &transport.GetGroupByNameRequest{TenantID: "t1", Name: "ops team"}); err != nil || g.ID != "g1" {
		t.Fatalf("GetGroupByName = %+v, %v", g, err)
	}
	if byName["name"] != "ops team" || byName["tenantID"] != "t1" {
		t.Fatalf("GetGroupByName query = %v", byName)
	}

	var updateBody string
	rs.handle(escapedPath(http.MethodPut, "/groups/g1"), func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		updateBody = string(b)
		writeJSON(w, canned)
	})
	if _, err := rt.UpdateGroup(ctx, &transport.UpdateGroupRequest{ID: "g1", RoleIDs: &[]string{}}); err != nil {
		t.Fatalf("UpdateGroup clear = %v", err)
	}
	if !strings.Contains(updateBody, `"roleIDs":[]`) {
		t.Fatalf("UpdateGroup clear body = %s, want roleIDs []", updateBody)
	}
	if _, err := rt.UpdateGroup(ctx, &transport.UpdateGroupRequest{ID: "g1", Name: "n"}); err != nil {
		t.Fatalf("UpdateGroup keep = %v", err)
	}
	if strings.Contains(updateBody, "roleIDs") || strings.Contains(updateBody, "description") {
		t.Fatalf("UpdateGroup keep body = %s, want no roleIDs or description", updateBody)
	}

	status := http.StatusNoContent
	rs.handle(escapedPath(http.MethodDelete, "/groups/g1"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	})
	if err := rt.DeleteGroup(ctx, "g1"); err != nil {
		t.Fatalf("DeleteGroup 204 = %v", err)
	}
	status = http.StatusOK
	if err := rt.DeleteGroup(ctx, "g1"); err != nil {
		t.Fatalf("DeleteGroup 200 = %v", err)
	}
}

func TestRESTGroups_ListQuery(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	var raw string
	rs.handle(exactPath(http.MethodGet, "/groups/list"), func(w http.ResponseWriter, r *http.Request) {
		raw = r.URL.RawQuery
		writeJSON(w, map[string]any{"groups": []*transport.Group{{ID: "g1"}, {ID: "g2"}}, "total": 7, "hasMore": true})
	})
	out, err := rt.ListGroups(context.Background(), &transport.ListGroupsRequest{})
	if err != nil || raw != "" {
		t.Fatalf("ListGroups empty = %v, query %q", err, raw)
	}
	if len(out.Groups) != 2 || out.Groups[1].ID != "g2" || out.Total != 7 || !out.HasMore {
		t.Fatalf("ListGroups decoded %+v", out)
	}
	if _, err := rt.ListGroups(context.Background(), &transport.ListGroupsRequest{TenantID: "t1", Page: 2, PageSize: 5}); err != nil {
		t.Fatalf("ListGroups paged = %v", err)
	}
	if raw != "page=2&pageSize=5&tenantID=t1" {
		t.Fatalf("ListGroups query = %q", raw)
	}
}

func TestRESTGroups_MembersUserIDIsADecimalString(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	ctx := context.Background()
	bodies := map[string]string{}
	for _, p := range []string{"/groups/members/add", "/groups/members/remove"} {
		rs.handle(exactPath(http.MethodPost, p), func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			bodies[r.URL.Path] = string(b)
			writeJSON(w, map[string]any{})
		})
	}
	if err := rt.AddGroupMember(ctx, math.MaxUint64, "g1"); err != nil {
		t.Fatalf("AddGroupMember = %v", err)
	}
	if err := rt.RemoveGroupMember(ctx, math.MaxUint64, "g1"); err != nil {
		t.Fatalf("RemoveGroupMember = %v", err)
	}
	for p, b := range bodies {
		if !strings.Contains(b, `"userID":"18446744073709551615"`) || !strings.Contains(b, `"groupID":"g1"`) {
			t.Fatalf("%s body = %s", p, b)
		}
	}
	if len(bodies) != 2 {
		t.Fatalf("member bodies = %v", bodies)
	}

	var groupID string
	rs.handle(exactPath(http.MethodGet, "/groups/members"), func(w http.ResponseWriter, r *http.Request) {
		groupID = r.URL.Query().Get("groupID")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"members":[{"userID":"18446744073709551615","username":"bob","tenantID":"t1"}]}`)
	})
	out, err := rt.ListGroupMembers(ctx, "g&1")
	if err != nil || groupID != "g&1" {
		t.Fatalf("ListGroupMembers = %v, groupID %q", err, groupID)
	}
	if len(out.Members) != 1 || out.Members[0] != (transport.GroupMember{UserID: math.MaxUint64, Username: "bob", TenantID: "t1"}) {
		t.Fatalf("ListGroupMembers decoded %+v", out.Members)
	}
}

func TestRESTGroups_DeleteServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(escapedPath(http.MethodDelete, "/groups/missing"), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"group not found"}`)
	})
	if err := rt.DeleteGroup(context.Background(), "missing"); err == nil {
		t.Fatal("DeleteGroup 404 = nil, want an error")
	}
}

func TestRESTGroups_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	ctx := context.Background()
	var ce *transport.ClosedError
	check := func(name string, err error) {
		t.Helper()
		if !errors.As(err, &ce) {
			t.Fatalf("%s = %T, want ClosedError", name, err)
		}
	}
	_, err := rt.CreateGroup(ctx, &transport.CreateGroupRequest{Name: "n"})
	check("CreateGroup", err)
	_, err = rt.GetGroup(ctx, "g1")
	check("GetGroup", err)
	_, err = rt.GetGroupByName(ctx, &transport.GetGroupByNameRequest{Name: "n"})
	check("GetGroupByName", err)
	_, err = rt.UpdateGroup(ctx, &transport.UpdateGroupRequest{ID: "g1"})
	check("UpdateGroup", err)
	check("DeleteGroup", rt.DeleteGroup(ctx, "g1"))
	_, err = rt.ListGroups(ctx, &transport.ListGroupsRequest{})
	check("ListGroups", err)
	check("AddGroupMember", rt.AddGroupMember(ctx, 1, "g1"))
	check("RemoveGroupMember", rt.RemoveGroupMember(ctx, 1, "g1"))
	_, err = rt.ListGroupMembers(ctx, "g1")
	check("ListGroupMembers", err)
}
