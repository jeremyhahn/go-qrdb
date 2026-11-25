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

package mcp

import (
	"context"
	"errors"
	"math"
	"slices"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

var groupTools = []string{
	"qrdb_group_create", "qrdb_group_get", "qrdb_group_get_by_name", "qrdb_group_update",
	"qrdb_group_delete", "qrdb_group_list", "qrdb_group_add_member", "qrdb_group_remove_member",
	"qrdb_group_list_members",
}

// groupStubTools records the arguments of each group tool and answers text, or an error result when isError is set.
type groupStubTools struct {
	text    string
	isError bool

	mu   sync.Mutex
	args map[string]map[string]any
}

func (s *groupStubTools) asked(tool string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.args[tool]
}

// connectGroupStub serves s in process and returns a connected Transport.
func connectGroupStub(t *testing.T, s *groupStubTools) *Transport {
	t.Helper()
	srv := mcpserver.NewMCPServer("group-stub", "0")
	for _, name := range groupTools {
		srv.AddTool(mcp.NewTool(name), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			s.mu.Lock()
			if s.args == nil {
				s.args = map[string]map[string]any{}
			}
			s.args[name] = req.GetArguments()
			text, isError := s.text, s.isError
			s.mu.Unlock()
			if isError {
				return mcp.NewToolResultError(text), nil
			}
			return mcp.NewToolResultText(text), nil
		})
	}
	tr, err := NewInProcess(srv)
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

func assertArgs(t *testing.T, tool string, got, want map[string]any) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s arguments = %v, want %v", tool, got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s argument %s = %#v, want %#v (all: %v)", tool, k, got[k], v, got)
		}
	}
}

func TestMCPGroups_ToolArguments(t *testing.T) {
	s := &groupStubTools{text: `{"id":"g1","name":"ops"}`}
	tr := connectGroupStub(t, s)
	ctx := context.Background()

	if _, err := tr.CreateGroup(ctx, &transport.CreateGroupRequest{TenantID: "t1", Name: "ops", Description: "d", RoleIDs: []string{"r1", "r2"}}); err != nil {
		t.Fatalf("CreateGroup = %v", err)
	}
	assertArgs(t, "qrdb_group_create", s.asked("qrdb_group_create"), map[string]any{"name": "ops", "tenant_id": "t1", "description": "d", "role_ids": "r1,r2"})

	if _, err := tr.GetGroup(ctx, "g1"); err != nil {
		t.Fatalf("GetGroup = %v", err)
	}
	assertArgs(t, "qrdb_group_get", s.asked("qrdb_group_get"), map[string]any{"id": "g1"})

	if _, err := tr.GetGroupByName(ctx, &transport.GetGroupByNameRequest{TenantID: "t1", Name: "ops"}); err != nil {
		t.Fatalf("GetGroupByName = %v", err)
	}
	assertArgs(t, "qrdb_group_get_by_name", s.asked("qrdb_group_get_by_name"), map[string]any{"name": "ops", "tenant_id": "t1"})

	if _, err := tr.UpdateGroup(ctx, &transport.UpdateGroupRequest{ID: "g1", RoleIDs: &[]string{}}); err != nil {
		t.Fatalf("UpdateGroup clear = %v", err)
	}
	assertArgs(t, "qrdb_group_update", s.asked("qrdb_group_update"), map[string]any{"id": "g1", "role_ids": ""})
	if _, err := tr.UpdateGroup(ctx, &transport.UpdateGroupRequest{ID: "g1", Name: "n"}); err != nil {
		t.Fatalf("UpdateGroup keep = %v", err)
	}
	assertArgs(t, "qrdb_group_update", s.asked("qrdb_group_update"), map[string]any{"id": "g1", "name": "n"})
	desc := ""
	if _, err := tr.UpdateGroup(ctx, &transport.UpdateGroupRequest{ID: "g1", Description: &desc, RoleIDs: &[]string{"r1", "r2"}}); err != nil {
		t.Fatalf("UpdateGroup set = %v", err)
	}
	assertArgs(t, "qrdb_group_update", s.asked("qrdb_group_update"), map[string]any{"id": "g1", "description": "", "role_ids": "r1,r2"})

	if err := tr.DeleteGroup(ctx, "g1"); err != nil {
		t.Fatalf("DeleteGroup = %v", err)
	}
	assertArgs(t, "qrdb_group_delete", s.asked("qrdb_group_delete"), map[string]any{"id": "g1"})

	if _, err := tr.ListGroups(ctx, &transport.ListGroupsRequest{}); err != nil {
		t.Fatalf("ListGroups = %v", err)
	}
	assertArgs(t, "qrdb_group_list", s.asked("qrdb_group_list"), map[string]any{})
	if _, err := tr.ListGroups(ctx, &transport.ListGroupsRequest{TenantID: "t1", Page: 2, PageSize: 5}); err != nil {
		t.Fatalf("ListGroups paged = %v", err)
	}
	assertArgs(t, "qrdb_group_list", s.asked("qrdb_group_list"), map[string]any{"tenant_id": "t1", "page": float64(2), "page_size": float64(5)})

	if err := tr.AddGroupMember(ctx, math.MaxUint64, "g1"); err != nil {
		t.Fatalf("AddGroupMember = %v", err)
	}
	assertArgs(t, "qrdb_group_add_member", s.asked("qrdb_group_add_member"), map[string]any{"user_id": "18446744073709551615", "group_id": "g1"})
	if err := tr.RemoveGroupMember(ctx, math.MaxUint64, "g1"); err != nil {
		t.Fatalf("RemoveGroupMember = %v", err)
	}
	assertArgs(t, "qrdb_group_remove_member", s.asked("qrdb_group_remove_member"), map[string]any{"user_id": "18446744073709551615", "group_id": "g1"})
}

func TestMCPGroups_DecodesResults(t *testing.T) {
	ctx := context.Background()
	g, err := connectGroupStub(t, &groupStubTools{text: `{"id":"g1","tenantID":"t1","name":"ops","description":"d","roleIDs":["r1"],"createdAt":"c","updatedAt":"u"}`}).GetGroup(ctx, "g1")
	if err != nil || g.ID != "g1" || g.TenantID != "t1" || g.Name != "ops" || g.Description != "d" || !slices.Equal(g.RoleIDs, []string{"r1"}) || g.CreatedAt != "c" || g.UpdatedAt != "u" {
		t.Fatalf("GetGroup = %+v, %v", g, err)
	}

	list, err := connectGroupStub(t, &groupStubTools{text: `{"groups":[{"id":"g1"},{"id":"g2"}],"total":9,"hasMore":true}`}).ListGroups(ctx, &transport.ListGroupsRequest{})
	if err != nil || len(list.Groups) != 2 || list.Groups[1].ID != "g2" || list.Total != 9 || !list.HasMore {
		t.Fatalf("ListGroups = %+v, %v", list, err)
	}

	members, err := connectGroupStub(t, &groupStubTools{text: `{"members":[{"userID":"18446744073709551615","username":"bob","tenantID":"t1"}]}`}).ListGroupMembers(ctx, "g1")
	if err != nil || len(members.Members) != 1 || members.Members[0] != (transport.GroupMember{UserID: math.MaxUint64, Username: "bob", TenantID: "t1"}) {
		t.Fatalf("ListGroupMembers = %+v, %v", members, err)
	}
}

func TestMCPGroups_ToolErrors(t *testing.T) {
	ctx := context.Background()
	failing := connectGroupStub(t, &groupStubTools{text: "group g1 changed", isError: true})
	if _, err := failing.GetGroup(ctx, "g1"); err == nil {
		t.Fatal("GetGroup: a tool error was not returned")
	}
	if err := failing.AddGroupMember(ctx, 1, "g1"); err == nil {
		t.Fatal("AddGroupMember: a tool error was not returned")
	}
	if err := failing.DeleteGroup(ctx, "g1"); err == nil {
		t.Fatal("DeleteGroup: a tool error was not returned")
	}

	garbled := connectGroupStub(t, &groupStubTools{text: "not json"})
	var de *jsonDecodeError
	if _, err := garbled.GetGroup(ctx, "g1"); !errors.As(err, &de) {
		t.Fatalf("GetGroup = %v, want a jsonDecodeError", err)
	}
	if _, err := garbled.ListGroups(ctx, &transport.ListGroupsRequest{}); !errors.As(err, &de) {
		t.Fatalf("ListGroups = %v, want a jsonDecodeError", err)
	}
	if _, err := garbled.ListGroupMembers(ctx, "g1"); !errors.As(err, &de) {
		t.Fatalf("ListGroupMembers = %v, want a jsonDecodeError", err)
	}
	if err := garbled.RemoveGroupMember(ctx, 1, "g1"); err != nil {
		t.Fatalf("RemoveGroupMember ignores the answer text, got %v", err)
	}
}

func TestMCPGroups_ClosedTransport(t *testing.T) {
	tr := newClosedTransport()
	ctx := context.Background()
	_, err := tr.CreateGroup(ctx, &transport.CreateGroupRequest{Name: "n"})
	assertClosed(t, err)
	_, err = tr.GetGroup(ctx, "g1")
	assertClosed(t, err)
	_, err = tr.GetGroupByName(ctx, &transport.GetGroupByNameRequest{Name: "n"})
	assertClosed(t, err)
	_, err = tr.UpdateGroup(ctx, &transport.UpdateGroupRequest{ID: "g1"})
	assertClosed(t, err)
	assertClosed(t, tr.DeleteGroup(ctx, "g1"))
	_, err = tr.ListGroups(ctx, &transport.ListGroupsRequest{})
	assertClosed(t, err)
	assertClosed(t, tr.AddGroupMember(ctx, 1, "g1"))
	assertClosed(t, tr.RemoveGroupMember(ctx, 1, "g1"))
	_, err = tr.ListGroupMembers(ctx, "g1")
	assertClosed(t, err)
}
