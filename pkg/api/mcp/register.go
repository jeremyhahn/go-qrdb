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
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// serverTool pairs one mcp-go tool registration with the permission required
// to invoke it, so the permission is declared WITH the tool at the
// registration site (unified-authz cutover; this replaced the old
// hand-maintained mcpPermissions table). Every tool set builder
// (kvTools, adminTools, ...) returns []serverTool, and addTools records the
// declared permission in the server's MCPToolRegistry while registering the
// tool on the underlying mcp-go server.
//
// No MCP tool is public: the MCP surface is a privileged agent interface, so
// every tool MUST carry a permission (or the permission.IdentityGated
// sentinel for handler-enforced share-claim flows). addTools enforces this
// at construction time — registering a tool with an empty Perm panics (the
// default-deny startup assertion for this transport).
type serverTool struct {
	Tool    mcp.Tool
	Perm    permission.Permission
	Handler mcpserver.ToolHandlerFunc
}

// addTools registers each tool on the underlying mcp-go server and declares
// its permission in the toolPerms registry consulted by AuthorizeMCP —
// ONE registration per tool, with authorization declared at the registration
// site. It panics if a tool carries no permission or a duplicate name (both
// construction-time bugs; see the MCPToolRegistry doc).
func (s *Server) addTools(tools ...serverTool) {
	for _, t := range tools {
		s.toolPerms.Register(t.Tool.Name, t.Perm)
		s.mcpSrv.AddTool(t.Tool, t.Handler)
	}
}
