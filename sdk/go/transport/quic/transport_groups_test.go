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
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestE2E_Groups_OpsAndPayloads(t *testing.T) {
	srv, tr := startE2EServer(t)
	ctx := context.Background()
	group := &transport.Group{ID: "g1", TenantID: "t1", Name: "ops", RoleIDs: []string{"r1"}}
	for _, op := range []string{"group.Create", "group.Get", "group.GetByName", "group.Update"} {
		srv.SetResponder(op, respondJSON(group))
	}
	for _, op := range []string{"group.Delete", "group.AddMember", "group.RemoveMember"} {
		srv.SetResponder(op, respondJSON(map[string]any{}))
	}
	srv.SetResponder("group.List", respondJSON(&transport.ListGroupsResponse{Groups: []*transport.Group{group}, Total: 4, HasMore: true}))
	srv.SetResponder("group.ListMembers", func([]byte) (uint16, []byte) {
		return wire.StatusOK, []byte(`{"members":[{"userID":"18446744073709551615","username":"bob","tenantID":"t1"}]}`)
	})
	payload := func(op string) string {
		t.Helper()
		rec := srv.LastRequest(op)
		if rec == nil {
			t.Fatalf("no recorded %s request", op)
		}
		return string(rec.Payload)
	}

	g, err := tr.CreateGroup(ctx, &transport.CreateGroupRequest{TenantID: "t1", Name: "ops", RoleIDs: []string{"r1", "r2"}})
	if err != nil || g.ID != "g1" || !slices.Equal(g.RoleIDs, []string{"r1"}) {
		t.Fatalf("CreateGroup = %+v, %v", g, err)
	}
	var created transport.CreateGroupRequest
	if err := json.Unmarshal([]byte(payload("group.Create")), &created); err != nil || created.Name != "ops" || created.TenantID != "t1" || !slices.Equal(created.RoleIDs, []string{"r1", "r2"}) {
		t.Fatalf("group.Create payload = %+v, %v", created, err)
	}

	if _, err := tr.GetGroup(ctx, "g1"); err != nil {
		t.Fatalf("GetGroup = %v", err)
	}
	if p := payload("group.Get"); p != `{"id":"g1"}` {
		t.Fatalf("group.Get payload = %s", p)
	}

	if _, err := tr.GetGroupByName(ctx, &transport.GetGroupByNameRequest{TenantID: "t1", Name: "ops"}); err != nil {
		t.Fatalf("GetGroupByName = %v", err)
	}
	if p := payload("group.GetByName"); p != `{"tenantID":"t1","name":"ops"}` {
		t.Fatalf("group.GetByName payload = %s", p)
	}

	if _, err := tr.UpdateGroup(ctx, &transport.UpdateGroupRequest{ID: "g1", RoleIDs: &[]string{}}); err != nil {
		t.Fatalf("UpdateGroup = %v", err)
	}
	if p := payload("group.Update"); !strings.Contains(p, `"roleIDs":[]`) || !strings.Contains(p, `"id":"g1"`) {
		t.Fatalf("group.Update payload = %s", p)
	}

	if err := tr.DeleteGroup(ctx, "g1"); err != nil {
		t.Fatalf("DeleteGroup = %v", err)
	}
	if p := payload("group.Delete"); p != `{"id":"g1"}` {
		t.Fatalf("group.Delete payload = %s", p)
	}

	list, err := tr.ListGroups(ctx, &transport.ListGroupsRequest{TenantID: "t1", Page: 2, PageSize: 5})
	if err != nil || len(list.Groups) != 1 || list.Groups[0].ID != "g1" || list.Total != 4 || !list.HasMore {
		t.Fatalf("ListGroups = %+v, %v", list, err)
	}
	if p := payload("group.List"); p != `{"tenantID":"t1","page":2,"pageSize":5}` {
		t.Fatalf("group.List payload = %s", p)
	}

	if err := tr.AddGroupMember(ctx, math.MaxUint64, "g1"); err != nil {
		t.Fatalf("AddGroupMember = %v", err)
	}
	if err := tr.RemoveGroupMember(ctx, math.MaxUint64, "g1"); err != nil {
		t.Fatalf("RemoveGroupMember = %v", err)
	}
	for _, op := range []string{"group.AddMember", "group.RemoveMember"} {
		if p := payload(op); p != `{"userID":"18446744073709551615","groupID":"g1"}` {
			t.Fatalf("%s payload = %s", op, p)
		}
	}

	members, err := tr.ListGroupMembers(ctx, "g1")
	if err != nil || len(members.Members) != 1 || members.Members[0] != (transport.GroupMember{UserID: math.MaxUint64, Username: "bob", TenantID: "t1"}) {
		t.Fatalf("ListGroupMembers = %+v, %v", members, err)
	}
	var asked transport.GroupMemberRequest
	if err := json.Unmarshal([]byte(payload("group.ListMembers")), &asked); err != nil || asked.GroupID != "g1" {
		t.Fatalf("group.ListMembers payload = %+v, %v", asked, err)
	}
}

func TestE2E_Groups_Errors(t *testing.T) {
	srv, tr := startE2EServer(t)
	ctx := context.Background()
	refuse := func([]byte) (uint16, []byte) { return wire.StatusError, []byte("group g1 changed") }
	for _, op := range []string{"group.Get", "group.AddMember", "group.Delete", "group.List"} {
		srv.SetResponder(op, refuse)
	}
	_, err := tr.GetGroup(ctx, "g1")
	assertQuicRequestError(t, err)
	assertQuicRequestError(t, tr.AddGroupMember(ctx, 1, "g1"))
	assertQuicRequestError(t, tr.DeleteGroup(ctx, "g1"))
	_, err = tr.ListGroups(ctx, &transport.ListGroupsRequest{})
	assertQuicRequestError(t, err)

	garbled := func([]byte) (uint16, []byte) { return wire.StatusOK, []byte("not json") }
	srv.SetResponder("group.Get", garbled)
	srv.SetResponder("group.List", garbled)
	var ue *UnmarshalError
	if _, err := tr.GetGroup(ctx, "g1"); !errors.As(err, &ue) {
		t.Fatalf("GetGroup = %v, want an UnmarshalError", err)
	}
	if _, err := tr.ListGroups(ctx, &transport.ListGroupsRequest{}); !errors.As(err, &ue) {
		t.Fatalf("ListGroups = %v, want an UnmarshalError", err)
	}
}

func TestE2E_Groups_ClosedTransport(t *testing.T) {
	tr := newClosedTransport()
	ctx := context.Background()
	var ce *transport.ClosedError
	check := func(name string, err error) {
		t.Helper()
		if !errors.As(err, &ce) {
			t.Fatalf("%s = %T, want ClosedError", name, err)
		}
	}
	_, err := tr.CreateGroup(ctx, &transport.CreateGroupRequest{Name: "n"})
	check("CreateGroup", err)
	_, err = tr.GetGroup(ctx, "g1")
	check("GetGroup", err)
	_, err = tr.GetGroupByName(ctx, &transport.GetGroupByNameRequest{Name: "n"})
	check("GetGroupByName", err)
	_, err = tr.UpdateGroup(ctx, &transport.UpdateGroupRequest{ID: "g1"})
	check("UpdateGroup", err)
	check("DeleteGroup", tr.DeleteGroup(ctx, "g1"))
	_, err = tr.ListGroups(ctx, &transport.ListGroupsRequest{})
	check("ListGroups", err)
	check("AddGroupMember", tr.AddGroupMember(ctx, 1, "g1"))
	check("RemoveGroupMember", tr.RemoveGroupMember(ctx, 1, "g1"))
	_, err = tr.ListGroupMembers(ctx, "g1")
	check("ListGroupMembers", err)
}
