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
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/group"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// mustMemoryGroupStore is a memory-backed group store for registration tests.
func mustMemoryGroupStore() group.GroupServicer {
	g, err := group.NewStore(kvstore.NewMemoryKVStore())
	if err != nil {
		panic(err)
	}
	return g
}

func callGroupTool(t *testing.T, tools []serverTool, ctx context.Context, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	for _, tl := range tools {
		if tl.Tool.Name != name {
			continue
		}
		req := mcp.CallToolRequest{}
		req.Params.Name = name
		req.Params.Arguments = args
		res, err := tl.Handler(ctx, req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.IsError {
			t.Fatalf("%s: tool error %+v", name, res.Content)
		}
		return res
	}
	t.Fatalf("tool %s not found", name)
	return nil
}

func groupToolText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	tc, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("content = %T", res.Content[0])
	}
	return tc.Text
}

func TestGroupTools_LifecycleAndMembership(t *testing.T) {
	ctx := tenant.WithTenantID(context.Background(), "acme")
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
	tools := groupTools(groups, users, nil, nil)

	var g group.Group
	if err := json.Unmarshal([]byte(groupToolText(t, callGroupTool(t, tools, ctx, "qrdb_group_create", map[string]any{"name": "eng", "tenant_id": "acme"}))), &g); err != nil {
		t.Fatal(err)
	}
	if g.ID == "" || g.Name != "eng" {
		t.Fatalf("created = %+v", g)
	}
	callGroupTool(t, tools, ctx, "qrdb_group_get", map[string]any{"id": g.ID})
	callGroupTool(t, tools, ctx, "qrdb_group_get_by_name", map[string]any{"name": "eng", "tenant_id": "acme"})
	callGroupTool(t, tools, ctx, "qrdb_group_update", map[string]any{"id": g.ID, "description": "engineering"})
	var list group.ListGroupsResponse
	if err := json.Unmarshal([]byte(groupToolText(t, callGroupTool(t, tools, ctx, "qrdb_group_list", map[string]any{"tenant_id": "acme"}))), &list); err != nil || list.Total != 1 || list.Groups[0].Description != "engineering" {
		t.Fatalf("list = %+v, %v", list, err)
	}

	alice, err := users.CreateUser(ctx, &user.CreateUserRequest{WithoutEmailLoginKey: true, Username: "alice", TenantID: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	uid := strconv.FormatUint(alice.ID, 10)
	callGroupTool(t, tools, ctx, "qrdb_group_add_member", map[string]any{"user_id": uid, "group_id": g.ID})
	var ms struct {
		Members []struct {
			UserID uint64 `json:"userID,string"` // the uint64 crosses the wire as a decimal string
		} `json:"members"`
	}
	if err := json.Unmarshal([]byte(groupToolText(t, callGroupTool(t, tools, ctx, "qrdb_group_list_members", map[string]any{"group_id": g.ID}))), &ms); err != nil || len(ms.Members) != 1 || ms.Members[0].UserID != alice.ID {
		t.Fatalf("members = %+v, %v", ms, err)
	}
	callGroupTool(t, tools, ctx, "qrdb_group_remove_member", map[string]any{"user_id": uid, "group_id": g.ID})
	callGroupTool(t, tools, ctx, "qrdb_group_delete", map[string]any{"id": g.ID})

	var nf *group.GroupNotFoundError
	for _, tl := range tools {
		if tl.Tool.Name == "qrdb_group_get" {
			req := mcp.CallToolRequest{}
			req.Params.Arguments = map[string]any{"id": g.ID}
			if _, err := tl.Handler(ctx, req); !errors.As(err, &nf) {
				t.Fatalf("get deleted: %v, want *GroupNotFoundError", err)
			}
		}
	}
}

func TestGroupTools_NotConfigured(t *testing.T) {
	for _, tl := range groupTools(nil, nil, nil, nil) {
		res, err := tl.Handler(context.Background(), mcp.CallToolRequest{})
		if err != nil || !res.IsError {
			t.Fatalf("%s without stores: res=%+v err=%v, want a tool error", tl.Tool.Name, res, err)
		}
	}
}
