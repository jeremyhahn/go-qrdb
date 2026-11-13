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

package mcphttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// TestTransport_RepliesCarryTheRequestID: a reply is the request's only
// when its wire id is the request's id. A mismatched, missing or null id,
// or a string where the request sent a number, is refused; an event
// stream skips replies that are not the request's.
func TestTransport_RepliesCarryTheRequestID(t *testing.T) {
	jsonBody := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body)
		}
	}
	eventStream := func(bodies ...string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, body := range bodies {
				_, _ = io.WriteString(w, "data: "+body+"\n\n")
			}
		}
	}
	send := func(t *testing.T, handler http.HandlerFunc, req mcptransport.JSONRPCRequest) (*mcptransport.JSONRPCResponse, error) {
		t.Helper()
		srv := httptest.NewServer(handler)
		defer srv.Close()
		return New(srv.URL, srv.Client()).SendRequest(context.Background(), req)
	}

	for name, handler := range map[string]http.HandlerFunc{
		"json mismatched id":   jsonBody(`{"jsonrpc":"2.0","id":2,"result":{"which":"other"}}`),
		"json missing id":      jsonBody(`{"jsonrpc":"2.0","result":{"which":"other"}}`),
		"json null id":         jsonBody(`{"jsonrpc":"2.0","id":null,"result":{"which":"other"}}`),
		"json string for int":  jsonBody(`{"jsonrpc":"2.0","id":"1","result":{"which":"other"}}`),
		"json no result":       jsonBody(`{"jsonrpc":"2.0","id":1}`),
		"stream mismatched id": eventStream(`{"jsonrpc":"2.0","id":2,"result":{"which":"other"}}`),
		"stream missing id":    eventStream(`{"jsonrpc":"2.0","result":{"which":"other"}}`),
		"stream null id":       eventStream(`{"jsonrpc":"2.0","id":null,"result":{"which":"other"}}`),
		"stream string id":     eventStream(`{"jsonrpc":"2.0","id":"1","result":{"which":"other"}}`),
	} {
		t.Run(name, func(t *testing.T) {
			if resp, err := send(t, handler, request(mcp.MethodToolsCall)); err == nil || resp != nil {
				t.Fatalf("reply %+v, err %v; want the reply refused", resp, err)
			}
		})
	}

	for name, handler := range map[string]http.HandlerFunc{
		"json matching id": jsonBody(`{"jsonrpc":"2.0","id":1,"result":{"which":"right"}}`),
		"stream skips replies that are not the request's": eventStream(
			`{"jsonrpc":"2.0","id":2,"result":{"which":"other"}}`,
			`{"jsonrpc":"2.0","result":{"which":"other"}}`,
			`{"jsonrpc":"2.0","id":null,"result":{"which":"other"}}`,
			`{"jsonrpc":"2.0","id":"1","result":{"which":"other"}}`,
			`{"jsonrpc":"2.0","id":1,"result":{"which":"right"}}`,
		),
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := send(t, handler, request(mcp.MethodToolsCall))
			if err != nil {
				t.Fatalf("SendRequest: %v", err)
			}
			if got := string(resp.Result); got != `{"which":"right"}` {
				t.Fatalf("result %s; want the request's reply", got)
			}
			if id, _ := resp.ID.MarshalJSON(); string(id) != "1" {
				t.Fatalf("reply id %s; want 1", id)
			}
		})
	}

	t.Run("a string id matches the same string", func(t *testing.T) {
		req := request(mcp.MethodToolsCall)
		req.ID = mcp.NewRequestId("req-7")
		resp, err := send(t, jsonBody(`{"jsonrpc":"2.0","id":"req-7","result":{"which":"right"}}`), req)
		if err != nil {
			t.Fatalf("SendRequest: %v", err)
		}
		if got := string(resp.Result); got != `{"which":"right"}` {
			t.Fatalf("result %s; want the request's reply", got)
		}
	})
}

// newEchoServer serves an mcp-go MCP server with one tool, echo, that
// answers its "text" argument.
func newEchoServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := server.NewMCPServer("echo", "1")
	s.AddTool(mcp.NewTool("echo", mcp.WithString("text")), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText(req.GetString("text", "")), nil
	})
	srv := httptest.NewServer(server.NewStreamableHTTPServer(s))
	t.Cleanup(srv.Close)
	return srv
}

func initialize(ctx context.Context, c *Client) error {
	_, err := c.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{
		ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
		ClientInfo:      mcp.Implementation{Name: "mcphttp-test", Version: "1"},
	}})
	return err
}

// TestClient_InitializesAndCallsTools: the Client opens a session with an
// mcp-go server, keeps its session id, calls a tool, and reports the
// server's JSON-RPC error for an unknown tool as an error.
func TestClient_InitializesAndCallsTools(t *testing.T) {
	srv := newEchoServer(t)
	tr := New(srv.URL+"/mcp", srv.Client())
	c := NewClient(tr)
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := initialize(ctx, c); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if tr.GetSessionId() == "" {
		t.Fatal("no session id kept from initialize")
	}
	for _, want := range []string{"one", "two"} {
		res, err := c.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": want}}})
		if err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		if len(res.Content) != 1 {
			t.Fatalf("content %+v; want one text", res.Content)
		}
		text, ok := mcp.AsTextContent(res.Content[0])
		if !ok || text.Text != want {
			t.Fatalf("content %+v; want text %q", res.Content[0], want)
		}
	}
	if _, err := c.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "absent"}}); err == nil || !strings.Contains(err.Error(), "server error") {
		t.Fatalf("err %v; want the server's error for an unknown tool", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestClient_RefusesAnUnsupportedProtocolVersion: a server that answers
// initialize with a protocol version the client does not speak opens no
// session.
func TestClient_RefusesAnUnsupportedProtocolVersion(t *testing.T) {
	var notified atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg message
		_ = json.NewDecoder(r.Body).Decode(&msg)
		if msg.Method != string(mcp.MethodInitialize) {
			notified.Store(true)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":`+string(msg.ID)+`,"result":{"protocolVersion":"1999-01-01","capabilities":{},"serverInfo":{"name":"old","version":"1"}}}`)
	}))
	defer srv.Close()
	err := initialize(context.Background(), NewClient(New(srv.URL, srv.Client())))
	if err == nil || !strings.Contains(err.Error(), "1999-01-01") {
		t.Fatalf("err %v; want the unsupported version refused", err)
	}
	if notified.Load() {
		t.Fatal("notifications/initialized sent for a refused session")
	}
}

// TestClient_AForwardedRequestsProtocolMetaIsDropped: a tools/call whose
// _meta carries another connection's per-request protocol keys reaches an
// initialized session without them, keeps its other meta, and leaves the
// caller's meta as it was.
func TestClient_AForwardedRequestsProtocolMetaIsDropped(t *testing.T) {
	srv := newEchoServer(t)
	c := NewClient(New(srv.URL+"/mcp", srv.Client()))
	ctx := context.Background()
	if err := initialize(ctx, c); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	meta := &mcp.Meta{ProgressToken: "p-1", AdditionalFields: map[string]any{
		mcp.MetaKeyProtocolVersion:    "2026-07-28",
		mcp.MetaKeyClientInfo:         mcp.Implementation{Name: "caller", Version: "1"},
		mcp.MetaKeyClientCapabilities: mcp.ClientCapabilities{},
		"example.com/trace":           "t-1",
	}}
	res, err := c.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name: "echo", Arguments: map[string]any{"text": "fwd"}, Meta: meta,
	}})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if text, ok := mcp.AsTextContent(res.Content[0]); !ok || text.Text != "fwd" {
		t.Fatalf("content %+v; want text fwd", res.Content)
	}
	if len(meta.AdditionalFields) != 4 {
		t.Fatalf("caller meta changed: %+v", meta.AdditionalFields)
	}
	got := sessionMeta(meta)
	if got.ProgressToken != "p-1" || len(got.AdditionalFields) != 1 || got.AdditionalFields["example.com/trace"] != "t-1" {
		t.Fatalf("session meta %+v; want the progress token and the trace key only", got)
	}
	if sessionMeta(&mcp.Meta{AdditionalFields: map[string]any{mcp.MetaKeyProtocolVersion: "2026-07-28"}}) != nil {
		t.Fatal("meta of protocol keys only should drop to none")
	}
}

// TestClient_ARedirectToAnotherHostIsNeverFollowed: with a client whose
// redirect policy refuses, a peer's Location naming another host fails the
// call, and that host sees no request (GO-2026-6911).
func TestClient_ARedirectToAnotherHostIsNeverFollowed(t *testing.T) {
	var reached atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached.Add(1)
	}))
	defer elsewhere.Close()
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/mcp", http.StatusTemporaryRedirect)
	}))
	defer peer.Close()
	errRefused := errors.New("redirect refused")
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return errRefused }}
	if err := initialize(context.Background(), NewClient(New(peer.URL+"/mcp", hc))); !errors.Is(err, errRefused) {
		t.Fatalf("err %v; want the redirect refused", err)
	}
	if n := reached.Load(); n != 0 {
		t.Fatalf("the redirect target saw %d requests; want none", n)
	}
}
