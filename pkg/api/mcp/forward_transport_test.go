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
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// startTLSPeer serves h over TLS with cfg and returns its host:port.
func startTLSPeer(t *testing.T, cfg *tls.Config, h http.Handler) string {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(h)
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return ln.Addr().String()
}

// streamPeer is a minimal streamable HTTP MCP server: it refuses
// server/discover, answers initialize as application/json with a session id, tools/call as an event
// stream that carries a notification before the reply, and records the
// session header of every later request.
type streamPeer struct {
	mu       sync.Mutex
	sessions []string
	deleted  atomic.Int32
}

func (p *streamPeer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		p.deleted.Add(1)
		return
	}
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &msg)
	switch msg.Method {
	case "server/discover":
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":`+string(msg.ID)+`,"error":{"code":-32601,"message":"Method not found"}}`)
		return
	case string(mcpgo.MethodInitialize):
	default:
		p.mu.Lock()
		p.sessions = append(p.sessions, r.Header.Get(headerMCPSessionID))
		p.mu.Unlock()
	}
	switch msg.Method {
	case string(mcpgo.MethodInitialize):
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(headerMCPSessionID, "s-1")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":`+string(msg.ID)+`,"result":{"protocolVersion":"`+mcpgo.LATEST_PROTOCOL_VERSION+`","capabilities":{"tools":{}},"serverInfo":{"name":"peer","version":"1"}}}`)
	case string(mcpgo.MethodToolsCall):
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":1,\"progress\":1}}\n\n")
		_, _ = io.WriteString(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"other\",\"result\":{}}\n\n")
		_, _ = io.WriteString(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":"+string(msg.ID)+",\n")
		_, _ = io.WriteString(w, "data: \"result\":{\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}\n\n")
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

func TestForwardTransport_KeepsTheSessionAndReadsAnEventStream(t *testing.T) {
	peer := &streamPeer{}
	srv := httptest.NewServer(peer)
	defer srv.Close()

	cli := mcpclient.NewClient(newForwardTransport(srv.URL, http.DefaultTransport))
	var notified atomic.Int32
	cli.OnNotification(func(n mcpgo.JSONRPCNotification) {
		if n.Method == "notifications/progress" {
			notified.Add(1)
		}
	})
	ctx := context.Background()
	if err := cli.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Initialize(ctx, mcpgo.InitializeRequest{Params: mcpgo.InitializeParams{
		ProtocolVersion: mcpgo.LATEST_PROTOCOL_VERSION,
		ClientInfo:      mcpgo.Implementation{Name: mcpForwardClientName, Version: "1"},
	}}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	req := mcpgo.CallToolRequest{}
	req.Params.Name = "qrdb_put"
	res, err := cli.CallTool(ctx, req)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("result %+v; want the streamed reply", res)
	}
	if text, ok := res.Content[0].(mcpgo.TextContent); !ok || text.Text != "ok" {
		t.Fatalf("content %+v; want the streamed reply", res.Content[0])
	}
	if notified.Load() != 1 {
		t.Fatalf("%d notifications handled; want the one streamed before the reply", notified.Load())
	}
	if err := cli.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if peer.deleted.Load() != 1 {
		t.Fatal("Close did not end the session")
	}
	peer.mu.Lock()
	defer peer.mu.Unlock()
	if len(peer.sessions) < 2 {
		t.Fatalf("requests after initialize: %v", peer.sessions)
	}
	for _, s := range peer.sessions {
		if s != "s-1" {
			t.Fatalf("a request after initialize carried session %q; want s-1", s)
		}
	}
}

func TestForwardTransport_AnyRedirectIsAFailure(t *testing.T) {
	var followed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Add(1) }))
	defer target.Close()
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"307 with a Location", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
		}},
		{"302 with a Location", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, http.StatusFound)
		}},
		{"300 without a Location", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusMultipleChoices)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			tr := newForwardTransport(srv.URL, http.DefaultTransport)
			_, err := tr.SendRequest(context.Background(), mcpRequest(mcpgo.MethodInitialize))
			if err == nil {
				t.Fatal("SendRequest accepted a redirect")
			}
			if err := tr.SendNotification(context.Background(), mcpgo.JSONRPCNotification{JSONRPC: mcpgo.JSONRPC_VERSION}); err == nil {
				t.Fatal("SendNotification accepted a redirect")
			}
		})
	}
	if followed.Load() != 0 {
		t.Fatalf("the redirect's target received %d requests", followed.Load())
	}
}

func TestForwardTransport_UnusableRepliesAreFailures(t *testing.T) {
	for _, tc := range []struct {
		name        string
		contentType string
		body        string
		want        string
	}{
		{"an unknown content type", "text/plain", "hello", "content type"},
		{"a stream without the reply", "text/event-stream", "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\ndata: not json\n\n", "without a reply"},
		{"undecodable JSON", "application/json", "{", "decode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			_, err := newForwardTransport(srv.URL, http.DefaultTransport).SendRequest(context.Background(), mcpRequest(mcpgo.MethodToolsCall))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v; want one naming %q", err, tc.want)
			}
		})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusBadGateway)
	}))
	defer srv.Close()
	if _, err := newForwardTransport(srv.URL, http.DefaultTransport).SendRequest(context.Background(), mcpRequest(mcpgo.MethodToolsCall)); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("err %v; want the peer's status", err)
	}
	if err := newForwardTransport(srv.URL, http.DefaultTransport).Close(); err != nil {
		t.Fatalf("Close without a session: %v", err)
	}
}

func TestForwardTransport_TheClientRefusesRedirects(t *testing.T) {
	tr := newForwardTransport("https://peer.invalid/mcp", http.DefaultTransport)
	if err := tr.client.CheckRedirect(nil, nil); !errors.Is(err, errForwardRedirect) {
		t.Fatalf("CheckRedirect returned %v; want the redirect refusal", err)
	}
}

func mcpRequest(method mcpgo.MCPMethod) mcptransport.JSONRPCRequest {
	return mcptransport.JSONRPCRequest{JSONRPC: mcpgo.JSONRPC_VERSION, ID: mcpgo.NewRequestId(int64(1)), Method: string(method)}
}
