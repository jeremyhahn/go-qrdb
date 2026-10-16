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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// mcpDispatchAuthenticator refuses every caller and counts how often it was
// asked: a request it sees is a request that reached authentication, and so
// the user store behind it.
type mcpDispatchAuthenticator struct{ calls atomic.Int64 }

func (a *mcpDispatchAuthenticator) Authenticate(context.Context) (*auth.Identity, error) {
	a.calls.Add(1)
	return nil, auth.ErrUnauthenticated
}

func (a *mcpDispatchAuthenticator) Name() string { return "counting" }

// mcpDispatchBarrier serves barrier status and cluster unseal, recording
// whether either was reached and whether anonymously; every other barrier
// method is unimplemented.
type mcpDispatchBarrier struct {
	service.BarrierServicer
	served, anonymous atomic.Bool
}

func (b *mcpDispatchBarrier) record(ctx context.Context) {
	b.served.Store(true)
	b.anonymous.Store(sealedwindow.Anonymous(ctx))
}

func (b *mcpDispatchBarrier) BarrierUnseal(ctx context.Context, _ []byte) error {
	b.record(ctx)
	return nil
}

func (b *mcpDispatchBarrier) BarrierClusterUnseal(ctx context.Context) (*clusterseal.Report, error) {
	b.record(ctx)
	return &clusterseal.Report{}, nil
}

func (b *mcpDispatchBarrier) BarrierStatus(ctx context.Context) (*service.BarrierStatusInfo, error) {
	b.record(ctx)
	return &service.BarrierStatusInfo{Sealed: true, Initialized: true, ClusterSeal: &clusterseal.Report{}}, nil
}

// postMCP posts body to h's MCP endpoint, carrying *session and keeping the
// session id the server assigns.
func postMCP(t *testing.T, h http.Handler, session *string, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if *session != "" {
		req.Header.Set("Mcp-Session-Id", *session)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if id := rr.Header().Get("Mcp-Session-Id"); id != "" {
		*session = id
	}
	got, _ := io.ReadAll(rr.Body)
	return rr.Code, string(got)
}

// TestSealedWindowDispatch_MCP: a regular MCP listener serves a sealed node
// exactly as the boot-time listener does -- the sealed-window tools
// anonymously, everything else refused, neither authenticated -- and
// authenticates every request once the node barrier is unsealed, a
// cluster-held node included.
func TestSealedWindowDispatch_MCP(t *testing.T) {
	const unsealBody = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"qrdb_barrier_unseal","arguments":{"secret":"s3cret"}}}`
	tests := []struct {
		name                string
		nodeSealed, barrier bool
		body                string
		served              bool
		unauthorized        bool
		want                string
	}{
		{name: "sealed, unseal", nodeSealed: true, barrier: true, body: unsealBody, served: true},
		{name: "sealed, data tool", nodeSealed: true, barrier: true, body: callToolBody("qrdb_get")},
		{name: "sealed, barrier status", nodeSealed: true, barrier: true, body: callToolBody("qrdb_barrier_status"), served: true, want: `\"initialized\":true`},
		{name: "cluster-held, barrier status", nodeSealed: true, body: callToolBody("qrdb_barrier_status"), unauthorized: true},
		{name: "sealed, health", nodeSealed: true, barrier: true, body: callToolBody("qrdb_health")},
		{name: "sealed, status", nodeSealed: true, barrier: true, body: callToolBody("qrdb_status")},
		{name: "sealed, authenticated-only tool", nodeSealed: true, barrier: true, body: callToolBody("qrdb_barrier_cluster_unseal")},
		{name: "unsealed", body: unsealBody, unauthorized: true},
		{name: "cluster-held", nodeSealed: true, body: callToolBody("qrdb_barrier_cluster_unseal"), unauthorized: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authn := &mcpDispatchAuthenticator{}
			barrier := &mcpDispatchBarrier{}
			srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
				WithAuthMiddleware(authmw.New(denyAllAuthorizer{}, authmw.WithAuthenticator(authn))),
				WithBarrierService(barrier),
				WithSealedWindowProbe(&fakeMCPSealedProbe{sealed: tt.nodeSealed}),
				WithSealedWindowDispatch(&fakeMCPSealedProbe{sealed: tt.barrier}))
			h := srv.endpoint()

			var session string
			if tt.unauthorized {
				if status, body := postMCP(t, h, &session, tt.body); status != http.StatusUnauthorized {
					t.Fatalf("status = %d, want 401 (body %q)", status, body)
				}
				if n := authn.calls.Load(); n != 1 {
					t.Fatalf("authentications = %d, want 1", n)
				}
				return
			}

			if status, body := postMCP(t, h, &session, initializeBody); status != http.StatusOK {
				t.Fatalf("initialize: status %d, body %q", status, body)
			}
			status, body := postMCP(t, h, &session, tt.body)
			if status != http.StatusOK {
				t.Fatalf("tools/call: status %d, body %q", status, body)
			}
			if tt.served {
				if strings.Contains(body, `"isError":true`) || strings.Contains(body, `"error":`) {
					t.Fatalf("refused (body %q)", body)
				}
				if !strings.Contains(body, tt.want) {
					t.Fatalf("body %q, want it to contain %q", body, tt.want)
				}
			} else if !strings.Contains(body, `"status":"UNAVAILABLE"`) || !strings.Contains(body, `"reason":"SEALED"`) {
				t.Fatalf("want the typed sealed refusal, body %q", body)
			}
			if barrier.served.Load() != tt.served || barrier.anonymous.Load() != tt.served {
				t.Fatalf("served %v anonymous %v, want %v", barrier.served.Load(), barrier.anonymous.Load(), tt.served)
			}
			if n := authn.calls.Load(); n != 0 {
				t.Fatalf("authentication ran %d times while the barrier was sealed", n)
			}
		})
	}
}
