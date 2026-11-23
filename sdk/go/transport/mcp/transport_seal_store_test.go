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
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// sealStoreStubTools answers the qrdb_seal_store_* tools with canned
// results and records the tool name and arguments of every call.
type sealStoreStubTools struct {
	results map[string]string
	isError bool

	mu    sync.Mutex
	calls []sealStoreStubCall
}

type sealStoreStubCall struct {
	tool string
	args map[string]any
}

func (s *sealStoreStubTools) last(t *testing.T) sealStoreStubCall {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		t.Fatal("no seal store tool was called")
	}
	return s.calls[len(s.calls)-1]
}

func connectSealStoreStub(t *testing.T, s *sealStoreStubTools) *Transport {
	t.Helper()
	srv := mcpserver.NewMCPServer("seal-store-stub", "0")
	for _, name := range []string{
		"qrdb_seal_store_put", "qrdb_seal_store_get", "qrdb_seal_store_delete",
		"qrdb_seal_store_list", "qrdb_seal_store_reseal", "qrdb_seal_store_status",
	} {
		srv.AddTool(mcp.NewTool(name), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			s.mu.Lock()
			s.calls = append(s.calls, sealStoreStubCall{tool: name, args: req.GetArguments()})
			s.mu.Unlock()
			if s.isError {
				return mcp.NewToolResultError("permission denied: seal:store_manage"), nil
			}
			text, ok := s.results[name]
			if !ok {
				text = "ok"
			}
			return mcp.NewToolResultText(text), nil
		})
	}
	tr, err := NewInProcess(srv)
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

func wantSealStoreCall(t *testing.T, got sealStoreStubCall, tool string, args map[string]any) {
	t.Helper()
	if got.tool != tool {
		t.Fatalf("tool = %q, want %q", got.tool, tool)
	}
	if len(args) == 0 && len(got.args) == 0 {
		return
	}
	if !reflect.DeepEqual(got.args, args) {
		t.Fatalf("%s args = %v, want %v", tool, got.args, args)
	}
}

// TestMCPSealStore_Calls drives the six SealStore methods against a stub
// MCP server and asserts the tool name and arguments each sends and the
// result each decodes.
func TestMCPSealStore_Calls(t *testing.T) {
	secret := []byte("wrapped\x00key")
	enc := base64.StdEncoding.EncodeToString(secret)
	stub := &sealStoreStubTools{results: map[string]string{
		"qrdb_seal_store_get":    `{"secret":"` + enc + `"}`,
		"qrdb_seal_store_list":   `{"names":["a","b"]}`,
		"qrdb_seal_store_status": `{"available":true,"sealer_id":"tpm2","secret_count":2,"secret_names":["a","b"]}`,
	}}
	tr := connectSealStoreStub(t, stub)
	ctx := context.Background()

	if err := tr.SealStorePut(ctx, &transport.SealStorePutRequest{Name: "a", Secret: secret}); err != nil {
		t.Fatalf("SealStorePut: %v", err)
	}
	wantSealStoreCall(t, stub.last(t), "qrdb_seal_store_put", map[string]any{"name": "a", "secret": enc})

	got, err := tr.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "a"})
	if err != nil {
		t.Fatalf("SealStoreGet: %v", err)
	}
	if !bytes.Equal(got.Secret, secret) {
		t.Fatalf("SealStoreGet secret = %q, want %q", got.Secret, secret)
	}
	wantSealStoreCall(t, stub.last(t), "qrdb_seal_store_get", map[string]any{"name": "a"})

	if err := tr.SealStoreDelete(ctx, &transport.SealStoreDeleteRequest{Name: "a"}); err != nil {
		t.Fatalf("SealStoreDelete: %v", err)
	}
	wantSealStoreCall(t, stub.last(t), "qrdb_seal_store_delete", map[string]any{"name": "a"})

	list, err := tr.SealStoreList(ctx)
	if err != nil {
		t.Fatalf("SealStoreList: %v", err)
	}
	if !reflect.DeepEqual(list.Names, []string{"a", "b"}) {
		t.Fatalf("SealStoreList names = %v", list.Names)
	}
	wantSealStoreCall(t, stub.last(t), "qrdb_seal_store_list", nil)

	if err := tr.SealStoreReseal(ctx, &transport.SealStoreResealRequest{Name: "b"}); err != nil {
		t.Fatalf("SealStoreReseal: %v", err)
	}
	wantSealStoreCall(t, stub.last(t), "qrdb_seal_store_reseal", map[string]any{"name": "b"})

	st, err := tr.SealStoreStatus(ctx)
	if err != nil {
		t.Fatalf("SealStoreStatus: %v", err)
	}
	want := &transport.SealStoreStatusResponse{Available: true, SealerID: "tpm2", SecretCount: 2, SecretNames: []string{"a", "b"}}
	if !reflect.DeepEqual(st, want) {
		t.Fatalf("SealStoreStatus = %+v, want %+v", st, want)
	}
	wantSealStoreCall(t, stub.last(t), "qrdb_seal_store_status", nil)
}

// TestMCPSealStore_ToolErrorIsError asserts an IsError tool result reaches
// the caller as an error.
func TestMCPSealStore_ToolErrorIsError(t *testing.T) {
	tr := connectSealStoreStub(t, &sealStoreStubTools{isError: true})
	if _, err := tr.SealStoreList(context.Background()); err == nil {
		t.Fatal("an IsError result was not returned as an error")
	}
	if err := tr.SealStorePut(context.Background(), &transport.SealStorePutRequest{Name: "a", Secret: []byte("x")}); err == nil {
		t.Fatal("an IsError put result was not returned as an error")
	}
}

// TestMCPSealStore_MissingSecretIsSealStoreNotFound proves a missing secret,
// an invalid name and a sealed seal-store barrier reach an MCP caller as the
// seal sentinel the embedded transport returns (#1647), not as a
// KeyNotFoundError. The payload is the one the server's classifiedToolError
// writes: the code, the message and the structured refusal reason.
func TestMCPSealStore_MissingSecretIsSealStoreNotFound(t *testing.T) {
	cases := []struct {
		tool     string
		code     wire.Code
		sentinel error
	}{
		{"qrdb_seal_store_get", wire.CodeNotFound, seal.ErrSealStoreSecretNotFound},
		{"qrdb_seal_store_delete", wire.CodeNotFound, seal.ErrSealStoreSecretNotFound},
		{"qrdb_seal_store_put", wire.CodeInvalidArgument, seal.ErrInvalidSecretName},
		{"qrdb_seal_store_get", wire.CodeUnavailable, seal.ErrSealStoreBarrierSealed},
	}
	for _, tc := range cases {
		reason, ok := transport.BarrierRefusal(tc.sentinel)
		if !ok {
			t.Fatalf("BarrierRefusal(%v) has no reason", tc.sentinel)
		}
		err := mapToolError(tc.tool, &wire.ErrorPayload{Code: tc.code, Message: tc.sentinel.Error(), Reason: reason})
		if !errors.Is(err, tc.sentinel) {
			t.Fatalf("%s %v = %v, want errors.Is %v", tc.tool, tc.code, err, tc.sentinel)
		}
		if _, ok := errors.AsType[*transport.KeyNotFoundError](err); ok {
			t.Fatalf("%s %v = %v, want no KeyNotFoundError", tc.tool, tc.code, err)
		}
	}
}

// TestMCPSealStore_BadJSON asserts an undecodable result is a jsonDecodeError.
func TestMCPSealStore_BadJSON(t *testing.T) {
	tr := connectSealStoreStub(t, &sealStoreStubTools{results: map[string]string{
		"qrdb_seal_store_get":    "not json",
		"qrdb_seal_store_list":   "not json",
		"qrdb_seal_store_status": "not json",
	}})
	ctx := context.Background()
	var de *jsonDecodeError
	if _, err := tr.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "a"}); !errors.As(err, &de) {
		t.Fatalf("SealStoreGet = %v, want a jsonDecodeError", err)
	}
	if _, err := tr.SealStoreList(ctx); !errors.As(err, &de) {
		t.Fatalf("SealStoreList = %v, want a jsonDecodeError", err)
	}
	if _, err := tr.SealStoreStatus(ctx); !errors.As(err, &de) {
		t.Fatalf("SealStoreStatus = %v, want a jsonDecodeError", err)
	}
}

// TestMCPSealStore_ClosedTransport asserts a closed transport refuses every
// call before it reaches the server.
func TestMCPSealStore_ClosedTransport(t *testing.T) {
	stub := &sealStoreStubTools{}
	tr := connectSealStoreStub(t, stub)
	_ = tr.Close()
	ctx := context.Background()
	var ce *transport.ClosedError
	if err := tr.SealStorePut(ctx, &transport.SealStorePutRequest{Name: "a", Secret: []byte("x")}); !errors.As(err, &ce) {
		t.Fatalf("SealStorePut = %v, want ClosedError", err)
	}
	if _, err := tr.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "a"}); !errors.As(err, &ce) {
		t.Fatalf("SealStoreGet = %v, want ClosedError", err)
	}
	if err := tr.SealStoreDelete(ctx, &transport.SealStoreDeleteRequest{Name: "a"}); !errors.As(err, &ce) {
		t.Fatalf("SealStoreDelete = %v, want ClosedError", err)
	}
	if _, err := tr.SealStoreList(ctx); !errors.As(err, &ce) {
		t.Fatalf("SealStoreList = %v, want ClosedError", err)
	}
	if err := tr.SealStoreReseal(ctx, &transport.SealStoreResealRequest{Name: "a"}); !errors.As(err, &ce) {
		t.Fatalf("SealStoreReseal = %v, want ClosedError", err)
	}
	if _, err := tr.SealStoreStatus(ctx); !errors.As(err, &ce) {
		t.Fatalf("SealStoreStatus = %v, want ClosedError", err)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.calls) != 0 {
		t.Fatalf("a closed transport reached the server %d times", len(stub.calls))
	}
}

// mcpSealStoreSecretCalls are the two seal-store calls that carry a secret:
// put sends one, get returns one.
var mcpSealStoreSecretCalls = []struct {
	name string
	call func(*Transport) error
}{
	{"put", func(tr *Transport) error {
		return tr.SealStorePut(context.Background(), &transport.SealStorePutRequest{Name: "alpha", Secret: []byte("s3cret")})
	}},
	{"get", func(tr *Transport) error {
		_, err := tr.SealStoreGet(context.Background(), &transport.SealStoreGetRequest{Name: "alpha"})
		return err
	}},
}

// TestMCPSealStore_SecretRequiresTLS proves a seal-store put or get is
// refused over http:// before any tool call reaches the server.
func TestMCPSealStore_SecretRequiresTLS(t *testing.T) {
	for _, c := range mcpSealStoreSecretCalls {
		t.Run(c.name, func(t *testing.T) {
			var reached atomic.Int64
			plain := httptest.NewServer(toolCallCounter(&reached))
			t.Cleanup(plain.Close)
			tr := connectMCP(t, plain.URL+"/mcp", nil)

			err := c.call(tr)
			var tlsErr *transport.BarrierSecretRequiresTLSError
			if !errors.As(err, &tlsErr) || tlsErr.Kind != transport.BarrierSecretSealStore {
				t.Fatalf("err = %v, want *transport.BarrierSecretRequiresTLSError for the seal-store secret", err)
			}
			if n := reached.Load(); n != 0 {
				t.Fatalf("the server saw %d seal-store tool calls over http://, want none", n)
			}
		})
	}
}

// TestMCPSealStore_PlaintextRedirectIsRefused proves a seal-store put or get
// never follows a redirect from https:// to http://.
func TestMCPSealStore_PlaintextRedirectIsRefused(t *testing.T) {
	for _, c := range mcpSealStoreSecretCalls {
		t.Run(c.name, func(t *testing.T) {
			var plainReached atomic.Int64
			plain := httptest.NewServer(toolCallCounter(&plainReached))
			t.Cleanup(plain.Close)
			session := mcpserver.NewStreamableHTTPServer(mcpserver.NewMCPServer("redirect-probe", "1"))
			origin := httptest.NewTLSServer(redirectToolCalls(session, http.StatusTemporaryRedirect, func(r *http.Request) string {
				return plain.URL + r.URL.Path
			}))
			t.Cleanup(origin.Close)
			tr := connectMCP(t, origin.URL+"/mcp", trustingMCP(origin))

			err := c.call(tr)
			var tlsErr *transport.BarrierSecretRequiresTLSError
			if !errors.As(err, &tlsErr) || tlsErr.Kind != transport.BarrierSecretSealStore {
				t.Fatalf("err = %v, want *transport.BarrierSecretRequiresTLSError for the seal-store secret", err)
			}
			if n := plainReached.Load(); n != 0 {
				t.Fatalf("the seal-store call followed a redirect to http:// (%d tool calls)", n)
			}
		})
	}
}

// TestMCPSealStore_ForeignHostRedirectIsRefused proves a seal-store put or
// get never follows a 307 or 308 to another host.
func TestMCPSealStore_ForeignHostRedirectIsRefused(t *testing.T) {
	for _, c := range mcpSealStoreSecretCalls {
		for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
			t.Run(c.name+"/"+http.StatusText(status), func(t *testing.T) {
				var foreignRequests, foreignBytes atomic.Int64
				foreign := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					foreignRequests.Add(1)
					foreignBytes.Add(int64(len(buffered(r))))
					w.WriteHeader(http.StatusOK)
				}))
				t.Cleanup(foreign.Close)
				session := mcpserver.NewStreamableHTTPServer(mcpserver.NewMCPServer("redirect-probe", "1"))
				origin := httptest.NewTLSServer(redirectToolCalls(session, status, func(r *http.Request) string {
					return foreign.URL + r.URL.Path
				}))
				t.Cleanup(origin.Close)
				tr := connectMCP(t, origin.URL+"/mcp", trustingMCP(origin))

				err := c.call(tr)
				var hostErr *transport.BarrierSecretForeignHostError
				if !errors.As(err, &hostErr) || hostErr.Kind != transport.BarrierSecretSealStore {
					t.Fatalf("err = %v, want *transport.BarrierSecretForeignHostError for the seal-store secret", err)
				}
				if n, body := foreignRequests.Load(), foreignBytes.Load(); n != 0 || body != 0 {
					t.Fatalf("the foreign host saw %d requests and %d body bytes, want none", n, body)
				}
			})
		}
	}
}
