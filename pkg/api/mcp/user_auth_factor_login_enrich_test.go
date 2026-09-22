// Copyright 2026 Jeremy Hahn
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

//go:build codec_json && raft

package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TestMCPUserAuthFactorLogin_Enriches verifies the MCP login tool enriches
// the result with username, tenant, and roles from the user service.
func TestMCPUserAuthFactorLogin_Enriches(t *testing.T) {
	factorSvc := newCovMockUserAuthFactorSvc() // lookup returns UserID 42
	userSvc := &mockUserService{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			return &user.User{
				ID:        id,
				Username:  "alice",
				TenantID:  "acme",
				RoleIDs:   []string{"admin"},
				Lifecycle: user.LifecycleActive,
			}, nil
		},
	}
	handler := handleUserAuthFactorLoginTool(factorSvc, userSvc)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"type":           "mtls",
		"lookup_key_b64": base64.StdEncoding.EncodeToString([]byte("key")),
	}
	res, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("login tool: %v", err)
	}
	text := textFromResult(t, res)

	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if out["username"] != "alice" {
		t.Errorf("username: got %v, want alice", out["username"])
	}
	if out["tenant_id"] != "acme" {
		t.Errorf("tenant_id: got %v, want acme", out["tenant_id"])
	}
	roles, ok := out["roles"].([]any)
	if !ok || len(roles) != 1 || roles[0] != "admin" {
		t.Errorf("roles: got %v, want [admin]", out["roles"])
	}
	if active, _ := out["active"].(bool); !active {
		t.Errorf("active: got %v, want true", out["active"])
	}
}

// textFromResult extracts the text payload from a tool result.
func textFromResult(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatal("empty tool result")
	}
	tc, ok := mcp.AsTextContent(res.Content[0])
	if !ok {
		t.Fatalf("expected text content, got %T", res.Content[0])
	}
	return tc.Text
}
