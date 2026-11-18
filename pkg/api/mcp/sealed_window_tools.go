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
	"slices"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// NewSealedWindowServer builds the MCP server for a sealed window: the
// barrier, ceremony and credential tools, behind the sealed-window listener
// gate backed by probe, and the typed sealed refusal under every other
// built-in tool name. It is the MCP peer of
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
// sealed allowlist that the given services can serve, each
// handler already wrapped in the sealed window's gate and its error coding.
// They are the tools NewSealedWindowServer serves, so a host never copies a
// sealed-window handler. The result is ordered by tool name.
//
// The gate travels with each handler, so the tools refuse exactly as qrdb's
// listener does: the typed sealed refusal for a first-boot tool once the
// barrier is initialized, CodeUnavailable once the barrier is unsealed.
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

// builtinToolCatalogue returns every built-in tool family, each built over
// nil services: the definitions and permissions are read, the handlers are
// never called. A sealed-window listener registers a refusal under each name
// it does not serve itself (refuseUnserved), so a sealed node answers every
// tool a regular server can serve with the typed sealed refusal rather than
// "tool not found". TestBuiltinToolCatalogue_CoversFullyWiredServer holds it
// to the tools a fully wired server registers.
func builtinToolCatalogue() []serverTool {
	return slices.Concat(
		kvTools(nil),
		adminTools(nil),
		mvccTools(nil),
		transactionTools(nil),
		batchTools(nil),
		watchTools(nil),
		leaseTools(nil),
		lockTools(nil),
		indexTools(nil),
		timeSeriesTools(nil),
		vectorTools(nil),
		embeddingTools(nil),
		ragTools(nil),
		userTools(nil, nil, nil, nil),
		roleTools(nil, nil, nil),
		roleReconcileAllTools(nil, nil, nil),
		certTools(nil, nil),
		enrollmentTools(nil, nil),
		invitationTools(nil, nil, nil),
		collectionTools(nil),
		databaseTools(nil),
		barrierTools(nil, nil, nil, nil),
		ceremonyTools(nil, nil),
		dekTools(nil),
		vlogTools(nil),
		policyTools(nil),
		credentialTools(nil),
		tenantTools(nil, nil, nil),
		tenantBarrierTools(nil, tenantBarrierAuthorizers{}),
		volumePoolTools(nil),
		backupTools(nil),
		rebalanceTools(nil),
		authFactorTools(nil),
		userAuthFactorTools(nil, nil),
		auditTools(nil, nil),
		// auditTools registers the replicated-chain verifier only when a
		// replicated logger is wired; the catalogue names it regardless.
		[]serverTool{{Tool: toolAuditVerifyReplicated(), Perm: permission.AuditVerifyReplicated}},
		sqlTools(nil),
	)
}

// servedTools returns every tool registered on s with the permission it was
// registered under, for a sealed path to refuse by name.
func (s *Server) servedTools() []serverTool {
	registered := s.mcpSrv.ListTools()
	tools := make([]serverTool, 0, len(registered))
	for _, t := range registered {
		perm, _ := s.toolPerms.Lookup(t.Tool.Name)
		tools = append(tools, serverTool{Tool: t.Tool, Perm: perm})
	}
	return tools
}

// refuseUnserved registers, under the name of every tool in tools that s does
// not serve itself, a handler answering with the typed sealed refusal. It is
// installed only on a sealed-window listener, whose gate refuses each such
// call before the handler runs; the handler refuses too, so the name can
// never serve anything. A tool's permission is registered with it when it
// has one, so authorization treats the name exactly as on a regular server.
func (s *Server) refuseUnserved(tools []serverTool) {
	refusal := sealedWindowRefusals[sealedwindow.RefuseSealed]
	refuse := func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		return refusal.result(req.Params.Name), nil
	}
	for _, t := range tools {
		if s.mcpSrv.GetTool(t.Tool.Name) != nil {
			continue
		}
		if _, ok := s.toolPerms.Lookup(t.Tool.Name); !ok && t.Perm != "" {
			s.toolPerms.Register(t.Tool.Name, t.Perm)
		}
		s.mcpSrv.AddTool(t.Tool, refuse)
	}
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
