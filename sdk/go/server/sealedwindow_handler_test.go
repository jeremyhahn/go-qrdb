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

package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	sdkserver "github.com/jeremyhahn/go-qrdb/sdk/go/server"
)

// TestNewSealedWindowHandler_NilProbeRefusesClosed holds the handler to the
// fail-closed answer: with no probe to say the window is open, every route
// answers the typed SEALED_WINDOW_CLOSED refusal.
func TestNewSealedWindowHandler_NilProbeRefusesClosed(t *testing.T) {
	h := sdkserver.NewSealedWindowHandler(nil, nil, nil, nil)
	if h == nil {
		t.Fatal("NewSealedWindowHandler returned nil")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/barrier/status", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "SEALED_WINDOW_CLOSED") {
		t.Fatalf("nil probe: %d %s, want 503 SEALED_WINDOW_CLOSED", rec.Code, rec.Body.String())
	}
}

// TestNewSealedWindowHandler_SealedServesOnlyAllowlist checks that a sealed,
// enrollment-open window refuses the routes outside its allowlist with the
// sealed answer and reports an allowlisted route whose service is absent as
// not implemented.
func TestNewSealedWindowHandler_SealedServesOnlyAllowlist(t *testing.T) {
	h := sdkserver.NewSealedWindowHandler(nil, nil, nil, &fakeProbe{sealed: true, enrollment: true})
	for _, c := range []struct {
		method, path string
		code         int
		body         string
	}{
		{http.MethodPost, "/v1/barrier/officers", http.StatusNotImplemented, "barrier"},
		{http.MethodGet, "/health", http.StatusServiceUnavailable, `"code":"SEALED"`},
		{http.MethodGet, "/kv/key", http.StatusServiceUnavailable, `"code":"SEALED"`},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, strings.NewReader("{}")))
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.body) {
			t.Errorf("%s %s: %d %s, want %d containing %q", c.method, c.path, rec.Code, rec.Body.String(), c.code, c.body)
		}
	}
}

// sealedWindowTool returns the named tool from tools, or nil.
func sealedWindowTool(tools []mcpserver.ServerTool, name string) *mcpserver.ServerTool {
	for i := range tools {
		if tools[i].Tool.Name == name {
			return &tools[i]
		}
	}
	return nil
}

// TestSealedWindowMCPTools_ExportsOnlyAllowlist checks that the sealed window
// exports the barrier status tool and none of the tools a sealed node must
// not serve, and that every exported name is on the MCP allowlist.
func TestSealedWindowMCPTools_ExportsOnlyAllowlist(t *testing.T) {
	tools := sdkserver.SealedWindowMCPTools(nil, nil, nil, &fakeProbe{sealed: true})
	if sealedWindowTool(tools, "qrdb_barrier_status") == nil {
		t.Fatal("a sealed window does not export qrdb_barrier_status")
	}
	for _, name := range []string{"qrdb_health", "qrdb_status", "qrdb_ceremony_status"} {
		if sealedWindowTool(tools, name) != nil {
			t.Errorf("a sealed window exports %s", name)
		}
	}
	for _, tl := range tools {
		if _, ok := sdkserver.MCPSealedWindowAllowlist[tl.Tool.Name]; !ok {
			t.Errorf("exported tool %s is not on the MCP sealed-window allowlist", tl.Tool.Name)
		}
	}
}

// TestSealedWindowMCPTools_UnsealedRefuses checks that once the node is
// unsealed the exported tools refuse rather than reach a service.
func TestSealedWindowMCPTools_UnsealedRefuses(t *testing.T) {
	tools := sdkserver.SealedWindowMCPTools(nil, nil, nil, &fakeProbe{sealed: false})
	status := sealedWindowTool(tools, "qrdb_barrier_status")
	if status == nil {
		t.Fatal("qrdb_barrier_status not exported")
	}
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "qrdb_barrier_status", Arguments: map[string]any{}}}
	res, err := status.Handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("an unsealed window answered %+v, want an error result", res)
	}
}
