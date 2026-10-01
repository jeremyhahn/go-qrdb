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
	"context"
	"encoding/json"
	"errors"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
)

// erroringToolModule registers one MCP tool per error it is given; each tool
// fails by RETURNING that error, the way a module's handler reports a failure.
// It builds no envelope — that is the server's job, and what this file pins.
type erroringToolModule struct {
	apimodule.BaseModule
	errs map[string]error
}

func (m *erroringToolModule) Name() string { return "erroring-tool-module" }

func (m *erroringToolModule) RegisterMCP(s *mcpserver.MCPServer) {
	for name, err := range m.errs {
		s.AddTool(mcpgo.NewTool(name), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return nil, err
		})
	}
}

// callToolThroughServer dispatches a tools/call JSON-RPC message through the
// real mcp-go server — so the server's whole tool middleware chain runs —
// and returns the decoded tool result.
func callToolThroughServer(t *testing.T, s *Server, tool string) *mcpgo.CallToolResult {
	t.Helper()
	msg, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": tool, "arguments": map[string]any{}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	reply := s.MCPServer().HandleMessage(context.Background(), msg)
	raw, err := json.Marshal(reply)
	if err != nil {
		t.Fatalf("marshal reply: %v", err)
	}
	var envelope struct {
		Result *mcpgo.CallToolResult `json:"result"`
		Error  json.RawMessage       `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("unmarshal reply %s: %v", raw, err)
	}
	if envelope.Result == nil {
		t.Fatalf("tools/call %s answered a JSON-RPC error, not a tool result: %s", tool, raw)
	}
	return envelope.Result
}

// TestModuleTool_ErrorsCarryTheClassifiedEnvelope pins the half of #561 that
// needs no new export: a module-registered tool that fails by returning an
// error gets the SAME classified envelope qrdb's own tools get, because the
// error-code middleware is server-wide and outermost. A gRPC status is taken
// verbatim; an unclassified error is CodeInternal, never CodeOK.
func TestModuleTool_ErrorsCarryTheClassifiedEnvelope(t *testing.T) {
	mod := &erroringToolModule{errs: map[string]error{
		"mod_not_found":    status.Error(codes.NotFound, "no such key"),
		"mod_already":      status.Error(codes.AlreadyExists, "serial collision"),
		"mod_denied":       status.Error(codes.PermissionDenied, "not yours"),
		"mod_unclassified": errors.New("boom"),
	}}
	s := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithServiceModules([]apimodule.ServiceModule{mod}))

	want := map[string]wire.Code{
		"mod_not_found":    wire.CodeNotFound,
		"mod_already":      wire.CodeAlreadyExists,
		"mod_denied":       wire.CodePermissionDenied,
		"mod_unclassified": wire.CodeInternal,
	}
	for tool, code := range want {
		t.Run(tool, func(t *testing.T) {
			res := callToolThroughServer(t, s, tool)
			if !res.IsError {
				t.Fatalf("%s: a failed module tool reported success", tool)
			}
			payload, ok := wire.DecodeErrorPayload(res.StructuredContent)
			if !ok {
				t.Fatalf("%s: no error envelope in StructuredContent", tool)
			}
			if payload.Code != code {
				t.Errorf("%s: code = %s, want %s", tool, payload.Code, code)
			}
			if payload.Tool != tool {
				t.Errorf("%s: envelope names tool %q", tool, payload.Tool)
			}
			if payload.Status != code.String() {
				t.Errorf("%s: status = %q, want %q", tool, payload.Status, code.String())
			}
		})
	}
}
