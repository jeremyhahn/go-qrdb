// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mcp

import (
	"testing"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// permsModule is a ServiceModule that declares MCP tool permissions via the
// MCPToolPermissioner extension.
type permsModule struct {
	apimodule.BaseModule
	registered bool
}

func (m *permsModule) Name() string { return "perms-module" }

func (m *permsModule) RegisterMCP(*mcpserver.MCPServer) { m.registered = true }

func (m *permsModule) MCPToolPermissions() map[string]permission.Permission {
	return map[string]permission.Permission{
		"xkms_generateKey": permission.Permission("keys:create"),
		"xkms_health":      permission.IdentityGated,
	}
}

// bareModule registers tools but declares no permissions (no
// MCPToolPermissioner implementation).
type bareModule struct {
	apimodule.BaseModule
}

func (m *bareModule) Name() string { return "bare-module" }

// TestServiceModule_MCPToolPermissionsFeedRegistry proves a module's
// declared tool permissions land in the server's tool registry — the single
// MCP authorization path — while an undeclared module tool stays uncovered
// (fail-closed under auth middleware).
func TestServiceModule_MCPToolPermissionsFeedRegistry(t *testing.T) {
	mod := &permsModule{}
	s := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithServiceModules([]apimodule.ServiceModule{mod, &bareModule{}}))

	if !mod.registered {
		t.Fatal("module RegisterMCP was not invoked")
	}

	perm, ok := s.toolPerms.Lookup("xkms_generateKey")
	if !ok || perm != permission.Permission("keys:create") {
		t.Fatalf("xkms_generateKey registry entry = %q, %v; want keys:create, true", perm, ok)
	}
	perm, ok = s.toolPerms.Lookup("xkms_health")
	if !ok || perm != permission.IdentityGated {
		t.Fatalf("xkms_health registry entry = %q, %v; want IdentityGated, true", perm, ok)
	}
	if s.toolPerms.Covered("bare-module-tool") {
		t.Fatal("undeclared module tool must stay uncovered (fail-closed)")
	}
}
