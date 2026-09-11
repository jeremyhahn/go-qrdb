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
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
)

// fakeMCPSealedProbe is a deterministic sealedWindowProbe test double.
type fakeMCPSealedProbe struct{ sealed bool }

func (f *fakeMCPSealedProbe) IsSealed() bool { return f.sealed }

func TestSealedWindowMiddleware_PassthroughWhenUnsealed(t *testing.T) {
	srv := &Server{sealedWindowProbe: &fakeMCPSealedProbe{sealed: false}}
	called := false
	next := func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		called = true
		return mcpgo.NewToolResultText("ok"), nil
	}

	mw := srv.sealedWindowMiddleware()
	req := buildCallToolRequest(t, "qrdb_put", map[string]any{})
	result, err := mw(next)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected next handler to be invoked when unsealed")
	}
	if result.IsError {
		t.Fatal("unexpected error result")
	}
}

func TestSealedWindowMiddleware_RejectsNonAllowlistedWhenSealed(t *testing.T) {
	srv := &Server{sealedWindowProbe: &fakeMCPSealedProbe{sealed: true}}
	called := false
	next := func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		called = true
		return mcpgo.NewToolResultText("ok"), nil
	}

	mw := srv.sealedWindowMiddleware()
	req := buildCallToolRequest(t, "qrdb_put", map[string]any{})
	result, err := mw(next)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Fatal("expected next handler NOT to be invoked while sealed")
	}
	if !result.IsError {
		t.Fatal("expected error result while sealed")
	}

	// The sealed window is the same condition REST answers 503 for, gRPC
	// codes.Unavailable and QUIC wire.StatusUnavailable. Since go-qrdb#438 MCP
	// says it too, so a client retries a node that will serve this tool later
	// instead of reporting a failure its operator has to read prose to
	// diagnose.
	payload, ok := wire.DecodeErrorPayload(result.StructuredContent)
	if !ok {
		t.Fatal("the sealed-window refusal carries no code")
	}
	if payload.Code != wire.CodeUnavailable {
		t.Errorf("code = %s, want UNAVAILABLE", payload.Code)
	}
	if payload.Tool != "qrdb_put" {
		t.Errorf("tool = %q, want qrdb_put", payload.Tool)
	}
}

func TestSealedWindowMiddleware_AllowsAllowlistedWhenSealed(t *testing.T) {
	srv := &Server{sealedWindowProbe: &fakeMCPSealedProbe{sealed: true}}
	called := false
	next := func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		called = true
		return mcpgo.NewToolResultText("ok"), nil
	}

	mw := srv.sealedWindowMiddleware()
	req := buildCallToolRequest(t, "qrdb_barrier_status", map[string]any{})
	result, err := mw(next)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected allowlisted tool to reach next handler while sealed")
	}
	if result.IsError {
		t.Fatal("unexpected error result for allowlisted tool")
	}
}

func TestSealedWindowMiddleware_NilProbePassesThrough(t *testing.T) {
	srv := &Server{}
	called := false
	next := func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		called = true
		return mcpgo.NewToolResultText("ok"), nil
	}

	mw := srv.sealedWindowMiddleware()
	req := buildCallToolRequest(t, "qrdb_put", map[string]any{})
	if _, err := mw(next)(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected next handler to be invoked when no probe is configured")
	}
}

func TestWithSealedWindowProbe_MCP_Configures(t *testing.T) {
	probe := &fakeMCPSealedProbe{sealed: true}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithSealedWindowProbe(probe))
	if srv.sealedWindowProbe == nil {
		t.Fatal("expected sealedWindowProbe to be set")
	}
}
