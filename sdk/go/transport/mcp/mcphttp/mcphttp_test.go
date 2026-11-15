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
	"sync"
	"sync/atomic"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// streamPeer is a minimal streamable HTTP MCP server: it refuses
// server/discover, answers initialize as application/json with a session
// id, tools/call as an event stream that carries a notification and
// another request's reply before its own, and records the session header
// of every later request.
type streamPeer struct {
	mu       sync.Mutex
	sessions []string
	deleted  atomic.Int32
}

func (p *streamPeer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		if r.Header.Get(HeaderSessionID) == "s-1" {
			p.deleted.Add(1)
		}
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
	case string(mcp.MethodInitialize):
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(HeaderSessionID, "s-1")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":`+string(msg.ID)+`,"result":{"protocolVersion":"`+mcp.LATEST_PROTOCOL_VERSION+`","capabilities":{"tools":{}},"serverInfo":{"name":"peer","version":"1"}}}`)
	case string(mcp.MethodToolsCall):
		p.record(r)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":1,\"progress\":1}}\n\n")
		_, _ = io.WriteString(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"other\",\"result\":{}}\n\n")
		_, _ = io.WriteString(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":"+string(msg.ID)+",\n")
		_, _ = io.WriteString(w, "data: \"result\":{\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}\n\n")
	default:
		p.record(r)
		w.WriteHeader(http.StatusAccepted)
	}
}

func (p *streamPeer) record(r *http.Request) {
	p.mu.Lock()
	p.sessions = append(p.sessions, r.Header.Get(HeaderSessionID))
	p.mu.Unlock()
}

func TestTransport_KeepsTheSessionAndReadsAnEventStream(t *testing.T) {
	peer := &streamPeer{}
	srv := httptest.NewServer(peer)
	defer srv.Close()

	cli := mcpclient.NewClient(New(srv.URL, srv.Client()))
	var notified atomic.Int32
	cli.OnNotification(func(n mcp.JSONRPCNotification) {
		if n.Method == "notifications/progress" {
			notified.Add(1)
		}
	})
	ctx := context.Background()
	if err := cli.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{
		ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
		ClientInfo:      mcp.Implementation{Name: "test", Version: "1"},
	}}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	req := mcp.CallToolRequest{}
	req.Params.Name = "qrdb_put"
	res, err := cli.CallTool(ctx, req)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("result %+v; want the streamed reply", res)
	}
	if text, ok := res.Content[0].(mcp.TextContent); !ok || text.Text != "ok" {
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

func TestTransport_AnUnfollowedRedirectIsAFailure(t *testing.T) {
	var followed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Add(1) }))
	defer target.Close()
	errRefused := errors.New("refused")
	refusing := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return errRefused }}
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		want    error
	}{
		{"307 with a Location", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
		}, errRefused},
		{"302 with a Location", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, http.StatusFound)
		}, errRefused},
		{"300 without a Location", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusMultipleChoices)
		}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			tr := New(srv.URL, refusing)
			_, err := tr.SendRequest(context.Background(), request(mcp.MethodInitialize))
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("SendRequest: %v; want a failure", err)
			}
			err = tr.SendNotification(context.Background(), mcp.JSONRPCNotification{JSONRPC: mcp.JSONRPC_VERSION})
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("SendNotification: %v; want a failure", err)
			}
		})
	}
	if followed.Load() != 0 {
		t.Fatalf("the redirect's target received %d requests", followed.Load())
	}
}

func TestTransport_UnusableRepliesAreFailures(t *testing.T) {
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
			_, err := New(srv.URL, srv.Client()).SendRequest(context.Background(), request(mcp.MethodToolsCall))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v; want one naming %q", err, tc.want)
			}
		})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusBadGateway)
	}))
	defer srv.Close()
	if _, err := New(srv.URL, srv.Client()).SendRequest(context.Background(), request(mcp.MethodToolsCall)); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("err %v; want the server's status", err)
	}
	if err := New(srv.URL, srv.Client()).Close(); err != nil {
		t.Fatalf("Close without a session: %v", err)
	}
}

// TestStatusError_DecodesTheJSONErrorBody pins that a refusal the server
// answers as {"error": "..."} reaches the caller as the plain message, with
// the status kept, and that a body that is not that object is kept verbatim.
func TestStatusError_DecodesTheJSONErrorBody(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{
			name: "json error object",
			body: `{"error":"qrdb/auth: invalid_cert: subject OU \"system\" and SPIFFE ID tenant \"acme\" disagree"}` + "\n",
			want: `mcphttp: the server answered 401 Unauthorized: qrdb/auth: invalid_cert: subject OU "system" and SPIFFE ID tenant "acme" disagree`,
		},
		{
			name: "plain text",
			body: "not json\n",
			want: "mcphttp: the server answered 401 Unauthorized: not json",
		},
		{
			name: "json without error",
			body: `{"message":"x"}`,
			want: `mcphttp: the server answered 401 Unauthorized: {"message":"x"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{
				Status:     "401 Unauthorized",
				StatusCode: http.StatusUnauthorized,
				Body:       io.NopCloser(strings.NewReader(tc.body)),
			}
			if got := statusError(resp).Error(); got != tc.want {
				t.Fatalf("statusError = %q, want %q", got, tc.want)
			}
		})
	}
}

func request(method mcp.MCPMethod) mcptransport.JSONRPCRequest {
	return mcptransport.JSONRPCRequest{JSONRPC: mcp.JSONRPC_VERSION, ID: mcp.NewRequestId(int64(1)), Method: string(method)}
}

// TestTransport_EdgeReplies covers the replies and failures the main tests
// do not reach: a JSON body that is not an object, an id of a type no
// request carries, a reply as the stream's last event without a closing
// blank line, a notification whose params do not decode, and a stream that
// fails mid-read.
func TestTransport_EdgeReplies(t *testing.T) {
	serve := func(t *testing.T, contentType, body string) (*mcptransport.JSONRPCResponse, error) {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", contentType)
			_, _ = io.WriteString(w, body)
		}))
		defer srv.Close()
		return New(srv.URL, srv.Client()).SendRequest(context.Background(), request(mcp.MethodToolsCall))
	}
	for _, tc := range []struct {
		name, contentType, body, want string
	}{
		{"a JSON array", "application/json", `[1]`, "decode"},
		{"a boolean id", "application/json", `{"jsonrpc":"2.0","id":true,"result":{}}`, "does not match"},
		{"a stream of a boolean id", "text/event-stream", "data: {\"jsonrpc\":\"2.0\",\"id\":true,\"result\":{}}\n\n", "without a reply"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := serve(t, tc.contentType, tc.body)
			if err == nil || resp != nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("reply %+v, err %v; want a failure naming %q", resp, err, tc.want)
			}
		})
	}

	t.Run("a reply ending the stream without a blank line", func(t *testing.T) {
		resp, err := serve(t, "text/event-stream", `data: {"jsonrpc":"2.0","id":1,"result":{}}`)
		if err != nil || resp == nil {
			t.Fatalf("reply %+v, err %v; want the final event's reply", resp, err)
		}
	})

	t.Run("a notification whose params do not decode is skipped", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":5}\n\n")
			_, _ = io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n")
		}))
		defer srv.Close()
		tr := New(srv.URL, srv.Client())
		var notified atomic.Int32
		tr.SetNotificationHandler(func(mcp.JSONRPCNotification) { notified.Add(1) })
		resp, err := tr.SendRequest(context.Background(), request(mcp.MethodToolsCall))
		if err != nil || resp == nil {
			t.Fatalf("reply %+v, err %v; want the reply after the skipped notification", resp, err)
		}
		if notified.Load() != 0 {
			t.Fatalf("%d notifications handled; want the undecodable one skipped", notified.Load())
		}
	})

	t.Run("a stream that fails mid-read", func(t *testing.T) {
		errRead := errors.New("read failed")
		resp, err := New("http://127.0.0.1", nil).readEventStream(failingReader{errRead}, mcp.NewRequestId(int64(1)))
		if resp != nil || !errors.Is(err, errRead) {
			t.Fatalf("reply %+v, err %v; want the read error", resp, err)
		}
	})
}

// failingReader fails every read with err.
type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

// TestDecodeWireID_RejectsWhatIsNotOneID pins that an empty ID or more
// than one JSON value is no ID.
func TestDecodeWireID_RejectsWhatIsNotOneID(t *testing.T) {
	for _, raw := range []string{"", "1 2", "{", "true", "[1]"} {
		if id, ok := decodeWireID(json.RawMessage(raw)); ok {
			t.Fatalf("decodeWireID(%q) = %+v; want no ID", raw, id)
		}
	}
}

// TestTransport_RequestFailures pins that an unusable URL and an
// unreachable server fail the request, the notification and Close.
func TestTransport_RequestFailures(t *testing.T) {
	bad := New("http://\x7f", http.DefaultClient)
	if err := bad.SendNotification(context.Background(), mcp.JSONRPCNotification{JSONRPC: mcp.JSONRPC_VERSION}); err == nil {
		t.Fatal("SendNotification to an unusable URL succeeded")
	}
	if _, err := bad.SendRequest(context.Background(), request(mcp.MethodToolsCall)); err == nil {
		t.Fatal("SendRequest to an unusable URL succeeded")
	}
	bad.sessionID = "s-1"
	if err := bad.Close(); err == nil {
		t.Fatal("Close on an unusable URL succeeded")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	gone := New(srv.URL, srv.Client())
	srv.Close()
	gone.sessionID = "s-1"
	if err := gone.Close(); err == nil {
		t.Fatal("Close against a closed server succeeded")
	}
	if gone.GetSessionId() != "s-1" {
		t.Fatal("a failed Close dropped the session")
	}
}
