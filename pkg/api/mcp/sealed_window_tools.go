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
	"slices"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// NewSealedWindowServer builds the MCP server for a sealed window: the
// barrier, ceremony and credential tools only, behind the sealed-window
// listener gate backed by probe. It is the MCP peer of
// rest.NewSealedWindowServer and is what qrdb's own boot-time listener
// serves. Any of the three services may be nil. opts add composition and are
// applied before the services and the gate. Build probe with
// server.NewSealedWindowListenerProbe and share it with every other transport
// serving the same window.
func NewSealedWindowServer(addr string, barrier service.BarrierServicer, ceremonySvc service.CeremonyServicer, credential service.CredentialServicer, probe sealedwindow.ListenerProbe, opts ...ServerOption) *Server {
	all := make([]ServerOption, 0, len(opts)+4)
	all = append(all, opts...)
	if barrier != nil {
		all = append(all, WithBarrierService(barrier))
	}
	if ceremonySvc != nil {
		all = append(all, WithCeremonyService(ceremonySvc))
	}
	if credential != nil {
		all = append(all, WithCredentialService(credential))
	}
	all = append(all, WithSealedWindowListener(probe))
	return NewServer(nil, nil, addr, all...)
}

// SealedWindowTools returns the sealed window's MCP tools for a host to add
// to its own MCP server (mcpserver.MCPServer.AddTools): every tool on the
// sealed allowlist that the given services can serve, health included, each
// handler already wrapped in the sealed window's gate and its error coding.
// They are the tools NewSealedWindowServer serves, so a host never copies a
// sealed-window handler. The result is ordered by tool name.
//
// The gate travels with each handler, so the tools refuse exactly as qrdb's
// listener does: anonymous while sealed, officer enrollment closed once the
// ceremony is operational, CodeUnavailable once the barrier is unsealed.
func SealedWindowTools(barrier service.BarrierServicer, ceremonySvc service.CeremonyServicer, credential service.CredentialServicer, probe sealedwindow.ListenerProbe, opts ...ServerOption) []mcpserver.ServerTool {
	s := NewSealedWindowServer("", barrier, ceremonySvc, credential, probe, opts...)
	registered := s.mcpSrv.ListTools()
	names := make([]string, 0, len(SealedWindowAllowlist))
	for name := range SealedWindowAllowlist {
		if _, ok := registered[name]; ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	tools := make([]mcpserver.ServerTool, 0, len(names))
	for _, name := range names {
		t := registered[name]
		tools = append(tools, mcpserver.ServerTool{Tool: t.Tool, Handler: s.wrapTool(t.Handler)})
	}
	return tools
}

// wrapTool applies the server's tool middleware chain to h exactly as mcp-go
// applies it to a registered tool: innermost last, so toolMiddlewares[0] is
// the outermost.
func (s *Server) wrapTool(h mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
	for i := len(s.toolMiddlewares) - 1; i >= 0; i-- {
		h = s.toolMiddlewares[i](h)
	}
	return h
}
