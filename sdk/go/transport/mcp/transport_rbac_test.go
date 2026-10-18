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
	"slices"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

var rbacBob = &transport.Identity{Subject: "bob", TenantID: "t1", Roles: []string{"reader", "auditor"}}

// rbacStubTools serves the two permission tools with canned answers and
// records the arguments qrdb_permissions_authorize was called with.
type rbacStubTools struct {
	own       string
	authorize string
	isError   bool

	mu   sync.Mutex
	args map[string]any
}

func (s *rbacStubTools) answer(text string) *mcp.CallToolResult {
	if s.isError {
		return mcp.NewToolResultError(text)
	}
	return mcp.NewToolResultText(text)
}

func (s *rbacStubTools) asked() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.args
}

// connectRBACStub serves s in process and returns a connected Transport.
func connectRBACStub(t *testing.T, s *rbacStubTools) *Transport {
	t.Helper()
	srv := mcpserver.NewMCPServer("rbac-stub", "0")
	srv.AddTool(mcp.NewTool("qrdb_permissions_own"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return s.answer(s.own), nil
	})
	srv.AddTool(mcp.NewTool("qrdb_permissions_authorize"), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		s.mu.Lock()
		s.args = req.GetArguments()
		s.mu.Unlock()
		return s.answer(s.authorize), nil
	})
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

func TestMCPRBAC_ListOwnPermissions(t *testing.T) {
	tr := connectRBACStub(t, &rbacStubTools{own: `{"permissions":[{"name":"kv:get","resource":"kv","action":"get","scope":"self"}]}`})
	got, err := tr.RBACClient().ListOwnPermissions(context.Background())
	want := []transport.PermissionInfo{{Name: "kv:get", Resource: "kv", Action: "get", Scope: transport.PermissionScopeSelf}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("ListOwnPermissions = %+v, %v; want %+v", got, err, want)
	}
}

func TestMCPRBAC_ListOwnPermissionsErrors(t *testing.T) {
	if _, err := connectRBACStub(t, &rbacStubTools{own: "unauthenticated", isError: true}).RBACClient().ListOwnPermissions(context.Background()); err == nil {
		t.Fatal("a tool error was not returned")
	}
	var de *jsonDecodeError
	if _, err := connectRBACStub(t, &rbacStubTools{own: "not json"}).RBACClient().ListOwnPermissions(context.Background()); !errors.As(err, &de) {
		t.Fatalf("ListOwnPermissions = %v, want a jsonDecodeError", err)
	}
}

func TestMCPRBAC_AuthorizeSendsTheIdentity(t *testing.T) {
	s := &rbacStubTools{authorize: `{"allowed":true}`}
	if err := connectRBACStub(t, s).RBACClient().Authorize(context.Background(), rbacBob, "kv", "get"); err != nil {
		t.Fatalf("Authorize = %v, want nil", err)
	}
	args := s.asked()
	want := map[string]any{"resource": "kv", "action": "get", "subject": "bob", "tenant_id": "t1", "roles": "reader,auditor"}
	for k, v := range want {
		if args[k] != v {
			t.Fatalf("argument %s = %v, want %v (all: %v)", k, args[k], v, args)
		}
	}
}

func TestMCPRBAC_AuthorizeDeniedIsUnauthorized(t *testing.T) {
	err := connectRBACStub(t, &rbacStubTools{authorize: `{"allowed":false,"reason":"no grant"}`}).RBACClient().Authorize(context.Background(), rbacBob, "kv", "put")
	var denied *transport.AuthorizationDeniedError
	if !errors.Is(err, auth.ErrUnauthorized) || !errors.As(err, &denied) || denied.Reason != "no grant" {
		t.Fatalf("Authorize = %v, want an AuthorizationDeniedError carrying the reason", err)
	}
}

func TestMCPRBAC_AuthorizeErrors(t *testing.T) {
	if err := connectRBACStub(t, &rbacStubTools{authorize: "permissions:resolve", isError: true}).RBACClient().Authorize(context.Background(), rbacBob, "kv", "get"); err == nil {
		t.Fatal("a tool error was not returned")
	}
	var de *jsonDecodeError
	if err := connectRBACStub(t, &rbacStubTools{authorize: "not json"}).RBACClient().Authorize(context.Background(), rbacBob, "kv", "get"); !errors.As(err, &de) {
		t.Fatalf("Authorize = %v, want a jsonDecodeError", err)
	}
}

func TestMCPRBAC_ClosedTransport(t *testing.T) {
	c := newClosedTransport().RBACClient()
	_, err := c.ListOwnPermissions(context.Background())
	assertClosed(t, err)
	assertClosed(t, c.Authorize(context.Background(), rbacBob, "kv", "get"))
	_, err = c.ListPermissions(context.Background(), rbacBob)
	assertClosed(t, err)
}
