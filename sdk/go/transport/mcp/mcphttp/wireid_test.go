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
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// TestTransport_RepliesCarryTheRequestID pins response correlation (ported
// from go-xkms 52734e0cde): a JSON-RPC response is the request's reply only
// when its wire ID equals the request's ID. An application/json body with a
// mismatched, missing or null ID is a failure; on an event stream an
// unrelated response is skipped, and a stream with no matching response is
// a failure.
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
	wantRight := func(t *testing.T, resp *mcptransport.JSONRPCResponse, wantID string) {
		t.Helper()
		var got struct{ Which string }
		if err := json.Unmarshal(resp.Result, &got); err != nil || got.Which != "right" {
			t.Fatalf("result %s (%v); want the matching reply", resp.Result, err)
		}
		id, err := json.Marshal(resp.ID)
		if err != nil || string(id) != wantID {
			t.Fatalf("reply id %s (%v); want the wire id %s", id, err, wantID)
		}
	}

	for name, handler := range map[string]http.HandlerFunc{
		"json mismatched id":   jsonBody(`{"jsonrpc":"2.0","id":2,"result":{"which":"other"}}`),
		"json missing id":      jsonBody(`{"jsonrpc":"2.0","result":{"which":"other"}}`),
		"json null id":         jsonBody(`{"jsonrpc":"2.0","id":null,"result":{"which":"other"}}`),
		"json string for int":  jsonBody(`{"jsonrpc":"2.0","id":"1","result":{"which":"other"}}`),
		"stream mismatched id": eventStream(`{"jsonrpc":"2.0","id":2,"result":{"which":"other"}}`),
		"stream missing id":    eventStream(`{"jsonrpc":"2.0","result":{"which":"other"}}`),
		"stream null id":       eventStream(`{"jsonrpc":"2.0","id":null,"result":{"which":"other"}}`),
		"stream string for int": eventStream(
			`{"jsonrpc":"2.0","id":"1","result":{"which":"other"}}`),
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := send(t, handler, request(mcp.MethodInitialize))
			if err == nil || resp != nil {
				t.Fatalf("reply %+v, err %v; want a failure", resp, err)
			}
		})
	}

	for name, handler := range map[string]http.HandlerFunc{
		"json matching id": jsonBody(`{"jsonrpc":"2.0","id":1,"result":{"which":"right"}}`),
		"stream skips unrelated responses": eventStream(
			`{"jsonrpc":"2.0","id":2,"result":{"which":"other"}}`,
			`{"jsonrpc":"2.0","result":{"which":"other"}}`,
			`{"jsonrpc":"2.0","id":null,"result":{"which":"other"}}`,
			`{"jsonrpc":"2.0","id":"1","result":{"which":"other"}}`,
			`{"jsonrpc":"2.0","id":1,"result":{"which":"right"}}`,
		),
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := send(t, handler, request(mcp.MethodInitialize))
			if err != nil {
				t.Fatalf("SendRequest: %v", err)
			}
			wantRight(t, resp, "1")
		})
	}

	t.Run("string id matches", func(t *testing.T) {
		req := request(mcp.MethodInitialize)
		req.ID = mcp.NewRequestId("req-7")
		resp, err := send(t, jsonBody(`{"jsonrpc":"2.0","id":"req-7","result":{"which":"right"}}`), req)
		if err != nil {
			t.Fatalf("SendRequest: %v", err)
		}
		wantRight(t, resp, `"req-7"`)
	})

	t.Run("error reply with the id", func(t *testing.T) {
		resp, err := send(t, jsonBody(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"no"}}`), request(mcp.MethodToolsCall))
		if err != nil || resp.Error == nil || resp.Error.Code != -32601 {
			t.Fatalf("reply %+v, err %v; want the JSON-RPC error", resp, err)
		}
	})
}
