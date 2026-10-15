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
	"net/http"
	"net/http/httptest"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
)

// fakeMCPSealedProbe is a deterministic sealedWindowProbe test double.
type fakeMCPSealedProbe struct {
	sealed, enrollment bool
	sealedwindow.Admission
}

func (f *fakeMCPSealedProbe) EnrollmentOpen(context.Context) bool { return f.enrollment }

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

// TestSealedWindowListener_RefusesEverythingOnceUnsealed pins the boot-time
// listener's MCP gate: once the barrier is unsealed a tool call is refused,
// allowlisted or not, and the HTTP endpoint answers 503 before mcp-go reads
// anything, so not even initialize or tools/list is served anonymously.
func TestSealedWindowListener_RefusesEverythingOnceUnsealed(t *testing.T) {
	probe := &fakeMCPSealedProbe{sealed: true}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithSealedWindowListener(probe))
	called := false
	next := func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		called = true
		return mcpgo.NewToolResultText("ok"), nil
	}
	mw := srv.sealedWindowMiddleware()
	httpCalled := false
	endpoint := srv.sealedWindowClosedHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	if _, err := mw(next)(context.Background(), buildCallToolRequest(t, "qrdb_barrier_unseal", map[string]any{})); err != nil || !called {
		t.Fatalf("allowlisted tool while sealed: called=%v err=%v", called, err)
	}
	rec := httptest.NewRecorder()
	endpoint.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if rec.Code != http.StatusOK || !httpCalled {
		t.Fatalf("endpoint while sealed: status %d, reached %v", rec.Code, httpCalled)
	}

	probe.sealed = false
	called, httpCalled = false, false
	result, err := mw(next)(context.Background(), buildCallToolRequest(t, "qrdb_barrier_unseal", map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called || !result.IsError {
		t.Fatal("allowlisted tool was served after unseal")
	}
	if payload, ok := wire.DecodeErrorPayload(result.StructuredContent); !ok || payload.Code != wire.CodeUnavailable {
		t.Errorf("refusal payload = %+v (%v), want UNAVAILABLE", payload, ok)
	}
	rec = httptest.NewRecorder()
	endpoint.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if rec.Code != http.StatusServiceUnavailable || httpCalled {
		t.Fatalf("endpoint after unseal: status %d, reached %v; want 503 before mcp-go", rec.Code, httpCalled)
	}
}

// TestWithSealedWindowListener_MCP_Configures verifies the option selects
// the listener's gate and that WithSealedWindowProbe applied after it
// restores the regular one.
func TestWithSealedWindowListener_MCP_Configures(t *testing.T) {
	probe := &fakeMCPSealedProbe{}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithSealedWindowListener(probe))
	if srv.sealedWindowProbe == nil || srv.sealedWindowListener == nil {
		t.Fatal("WithSealedWindowListener did not install the listener gate")
	}
	WithSealedWindowProbe(probe)(srv)
	if srv.sealedWindowListener != nil {
		t.Fatal("WithSealedWindowProbe applied last must install the regular gate")
	}
}

// TestSealedWindowListener_OfficerRegistrationFollowsEnrollment: the MCP
// listener gate refuses the registration tools with PERMISSION_DENIED once
// enrollment has closed, and admits them while open.
func TestSealedWindowListener_OfficerRegistrationFollowsEnrollment(t *testing.T) {
	probe := &fakeMCPSealedProbe{sealed: true}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithSealedWindowListener(probe))
	mw := srv.sealedWindowMiddleware()
	called := false
	next := func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		called = true
		return mcpgo.NewToolResultText("ok"), nil
	}
	for _, tool := range []string{"qrdb_barrier_officer_register", "qrdb_barrier_recovery_officer_register"} {
		called = false
		result, err := mw(next)(context.Background(), buildCallToolRequest(t, tool, map[string]any{}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		payload, ok := wire.DecodeErrorPayload(result.StructuredContent)
		if called || !ok || payload.Code != wire.CodePermissionDenied {
			t.Errorf("%s, enrollment closed: called=%v payload=%+v, want PERMISSION_DENIED", tool, called, payload)
		}
	}
	probe.enrollment = true
	called = false
	if _, err := mw(next)(context.Background(), buildCallToolRequest(t, "qrdb_barrier_officer_register", map[string]any{})); err != nil || !called {
		t.Errorf("enrollment open: called=%v err=%v", called, err)
	}
}

// stateMCPProbe reports the two-valued seal state of sealedwindow.StateProbe.
type stateMCPProbe struct{ sealed, unconfirmed bool }

func (p stateMCPProbe) IsSealed() bool                        { return p.sealed }
func (p stateMCPProbe) SealState() (sealed, unconfirmed bool) { return p.sealed, p.unconfirmed }

var _ sealedwindow.StateProbe = stateMCPProbe{}

func TestSealedWindow_UnconfirmedIsNotSealed(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithSealedWindowProbe(stateMCPProbe{sealed: true, unconfirmed: true}))
	called := false
	next := func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		called = true
		return mcpgo.NewToolResultText("ok"), nil
	}
	result, err := srv.sealedWindowMiddleware()(next)(context.Background(), buildCallToolRequest(t, "qrdb_get", map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	payload, ok := wire.DecodeErrorPayload(result.StructuredContent)
	if called || !ok || payload.Code != wire.CodeUnavailable {
		t.Fatalf("called=%v payload=%+v, want UNAVAILABLE", called, payload)
	}
	if payload.Reason != replicaforward.ReasonSealStateUnconfirmed {
		t.Errorf("reason = %q, want %q", payload.Reason, replicaforward.ReasonSealStateUnconfirmed)
	}
	if want := (&clusterseal.SealStateUnconfirmedError{}).Error(); payload.Message != want {
		t.Errorf("message = %q, want %q", payload.Message, want)
	}
	if _, err := srv.sealedWindowMiddleware()(next)(context.Background(), buildCallToolRequest(t, "qrdb_barrier_status", map[string]any{})); err != nil || !called {
		t.Errorf("allowlisted status on an unconfirmed node: called=%v err=%v", called, err)
	}
}

// TestSealedWindowListener_MarksToolCallsAnonymous: tool calls the listener
// admits carry the sealedwindow.Anonymous mark.
func TestSealedWindowListener_MarksToolCallsAnonymous(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithSealedWindowListener(&fakeMCPSealedProbe{sealed: true}))
	var anonymous bool
	next := func(ctx context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		anonymous = sealedwindow.Anonymous(ctx)
		return mcpgo.NewToolResultText("ok"), nil
	}
	if _, err := srv.sealedWindowMiddleware()(next)(context.Background(), buildCallToolRequest(t, "qrdb_barrier_status", map[string]any{})); err != nil || !anonymous {
		t.Errorf("listener tool call: anonymous=%v err=%v", anonymous, err)
	}
}

// TestSealedWindowListener_ClusterUnsealIsNeverAnonymous: the boot-time
// listener refuses the cluster-unseal tool; the regular gate admits it while
// closed, leaving the decision to authentication.
func TestSealedWindowListener_ClusterUnsealIsNeverAnonymous(t *testing.T) {
	probe := &fakeMCPSealedProbe{sealed: true, enrollment: true}
	called := false
	next := func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		called = true
		return mcpgo.NewToolResultText("ok"), nil
	}
	listener := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithSealedWindowListener(probe))
	result, err := listener.sealedWindowMiddleware()(next)(context.Background(), buildCallToolRequest(t, "qrdb_barrier_cluster_unseal", map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	payload, ok := wire.DecodeErrorPayload(result.StructuredContent)
	if called || !ok || payload.Code != wire.CodeUnavailable {
		t.Errorf("boot-time listener: called=%v payload=%+v, want UNAVAILABLE", called, payload)
	}
	regular := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithSealedWindowProbe(probe))
	if _, err := regular.sealedWindowMiddleware()(next)(context.Background(), buildCallToolRequest(t, "qrdb_barrier_cluster_unseal", map[string]any{})); err != nil || !called {
		t.Errorf("regular gate: called=%v err=%v", called, err)
	}
}
