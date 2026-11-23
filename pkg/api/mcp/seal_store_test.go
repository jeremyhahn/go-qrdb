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
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// stubSealStore is an in-memory service.SealStoreServicer that counts every
// call it serves.
type stubSealStore struct {
	mu      sync.Mutex
	secrets map[string][]byte
	calls   map[string]int
}

var _ service.SealStoreServicer = (*stubSealStore)(nil)

var errStubSealStoreNotFound = errors.New("seal store: secret not found")

func newStubSealStore() *stubSealStore {
	return &stubSealStore{secrets: map[string][]byte{}, calls: map[string]int{}}
}

func (s *stubSealStore) record(op string) {
	s.calls[op]++
}

func (s *stubSealStore) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.calls {
		n += c
	}
	return n
}

func (s *stubSealStore) count(op string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[op]
}

func (s *stubSealStore) Put(_ context.Context, name string, secret []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("put")
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return &seal.SealStoreError{Op: "put", Key: name, Err: seal.ErrInvalidSecretName}
	}
	s.secrets[name] = append([]byte(nil), secret...)
	return nil
}

func (s *stubSealStore) Get(_ context.Context, name string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("get")
	v, ok := s.secrets[name]
	if !ok {
		return nil, errStubSealStoreNotFound
	}
	return append([]byte(nil), v...), nil
}

func (s *stubSealStore) Delete(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("delete")
	delete(s.secrets, name)
	return nil
}

func (s *stubSealStore) List(_ context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("list")
	names := make([]string, 0, len(s.secrets))
	for n := range s.secrets {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

func (s *stubSealStore) Reseal(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("reseal")
	if _, ok := s.secrets[name]; !ok {
		return errStubSealStoreNotFound
	}
	return nil
}

func (s *stubSealStore) Status(_ context.Context) (*seal.SealStoreStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("status")
	names := make([]string, 0, len(s.secrets))
	for n := range s.secrets {
		names = append(names, n)
	}
	sort.Strings(names)
	return &seal.SealStoreStatus{
		Available:   true,
		Strategy:    seal.StrategyID("stub-sealer"),
		SecretCount: len(names),
		SecretNames: names,
	}, nil
}

func startSealStoreMCP(t *testing.T, store *stubSealStore, tenant string, id *auth.Identity) *mcpclient.Client {
	t.Helper()
	roles := authtest.BuiltInRoles(t, tenantid.System, tenant)
	caller := authtest.NewFixedAuthenticator(id)
	return startMCP(t, &mockAdminService{}, &atomic.Pointer[string]{},
		WithAuthMiddleware(authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles), authmw.WithAuthenticator(caller))),
		WithSealStoreService(store))
}

func callSealStoreTool(t *testing.T, client *mcpclient.Client, tool string, args map[string]any) *mcpgo.CallToolResult {
	t.Helper()
	req := mcpgo.CallToolRequest{}
	req.Params.Name = tool
	req.Params.Arguments = args
	res, err := client.CallTool(callCtx(t), req)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return res
}

func sealStoreResultText(t *testing.T, tool string, res *mcpgo.CallToolResult) string {
	t.Helper()
	if res.IsError {
		t.Fatalf("%s: IsError result: %+v", tool, res.Content)
	}
	if len(res.Content) == 0 {
		t.Fatalf("%s: empty result", tool)
	}
	text, ok := res.Content[0].(mcpgo.TextContent)
	if !ok {
		t.Fatalf("%s: content %T, want text", tool, res.Content[0])
	}
	return text.Text
}

// TestSealStoreTools_SOServed drives every seal store tool as the system
// Security Officer, who holds seal:store_manage, and asserts each call
// reaches the seal store service with the arguments it sent.
func TestSealStoreTools_SOServed(t *testing.T) {
	store := newStubSealStore()
	client := startSealStoreMCP(t, store, tenantid.System, authtest.SOIdentity(tenantid.System, "system-so"))
	secret := []byte("barrier-wrapped\x00secret")
	enc := base64.StdEncoding.EncodeToString(secret)

	sealStoreResultText(t, "qrdb_seal_store_put", callSealStoreTool(t, client, "qrdb_seal_store_put",
		map[string]any{"name": "db-key", "secret": enc}))
	if store.count("put") != 1 {
		t.Fatalf("put calls = %d, want 1", store.count("put"))
	}

	text := sealStoreResultText(t, "qrdb_seal_store_get", callSealStoreTool(t, client, "qrdb_seal_store_get",
		map[string]any{"name": "db-key"}))
	var got struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("get result %q: %v", text, err)
	}
	if got.Secret != enc {
		t.Fatalf("get secret = %q, want %q", got.Secret, enc)
	}

	text = sealStoreResultText(t, "qrdb_seal_store_list", callSealStoreTool(t, client, "qrdb_seal_store_list", nil))
	var list struct {
		Names []string `json:"names"`
	}
	if err := json.Unmarshal([]byte(text), &list); err != nil {
		t.Fatalf("list result %q: %v", text, err)
	}
	if len(list.Names) != 1 || list.Names[0] != "db-key" {
		t.Fatalf("list names = %v, want [db-key]", list.Names)
	}

	sealStoreResultText(t, "qrdb_seal_store_reseal", callSealStoreTool(t, client, "qrdb_seal_store_reseal",
		map[string]any{"name": "db-key"}))
	if store.count("reseal") != 1 {
		t.Fatalf("reseal calls = %d, want 1", store.count("reseal"))
	}

	text = sealStoreResultText(t, "qrdb_seal_store_status", callSealStoreTool(t, client, "qrdb_seal_store_status", nil))
	var st transport.SealStoreStatusResponse
	if err := json.Unmarshal([]byte(text), &st); err != nil {
		t.Fatalf("status result %q: %v", text, err)
	}
	if !st.Available || st.SealerID != "stub-sealer" || st.SecretCount != 1 || len(st.SecretNames) != 1 || st.SecretNames[0] != "db-key" {
		t.Fatalf("status = %+v", st)
	}

	sealStoreResultText(t, "qrdb_seal_store_delete", callSealStoreTool(t, client, "qrdb_seal_store_delete",
		map[string]any{"name": "db-key"}))
	if store.count("delete") != 1 {
		t.Fatalf("delete calls = %d, want 1", store.count("delete"))
	}
	if store.total() != 6 {
		t.Fatalf("seal store calls = %d, want 6", store.total())
	}
}

// TestSealStoreTools_ServiceErrorIsToolError asserts a seal store error
// reaches the caller as an error result, not a success.
func TestSealStoreTools_ServiceErrorIsToolError(t *testing.T) {
	store := newStubSealStore()
	client := startSealStoreMCP(t, store, tenantid.System, authtest.SOIdentity(tenantid.System, "system-so"))
	res := callSealStoreTool(t, client, "qrdb_seal_store_get", map[string]any{"name": "missing"})
	if !res.IsError {
		t.Fatalf("get of a missing secret: result %+v, want IsError", res)
	}
	if store.count("get") != 1 {
		t.Fatalf("get calls = %d, want 1", store.count("get"))
	}
}

// TestSealStoreTools_InvalidNameIsInvalidArgument: a secret name that is not
// one opaque path segment is the caller's error, so the tool error carries the
// invalid-argument code, the class REST answers 400 for.
func TestSealStoreTools_InvalidNameIsInvalidArgument(t *testing.T) {
	store := newStubSealStore()
	client := startSealStoreMCP(t, store, tenantid.System, authtest.SOIdentity(tenantid.System, "system-so"))
	res := callSealStoreTool(t, client, "qrdb_seal_store_put", map[string]any{
		"name": `a\b`, "secret": base64.StdEncoding.EncodeToString([]byte("s1")),
	})
	if !res.IsError {
		t.Fatalf("put of an invalid name: result %+v, want IsError", res)
	}
	if got := toolErrorCode(t, res); got != wire.CodeInvalidArgument {
		t.Fatalf("code = %s, want %s", got, wire.CodeInvalidArgument)
	}
	if store.count("put") != 1 {
		t.Fatalf("put calls = %d, want 1", store.count("put"))
	}
}

// TestSealStoreTools_BadSecretRefused asserts a put whose secret is not
// base64 is refused before it reaches the seal store.
func TestSealStoreTools_BadSecretRefused(t *testing.T) {
	store := newStubSealStore()
	client := startSealStoreMCP(t, store, tenantid.System, authtest.SOIdentity(tenantid.System, "system-so"))
	res := callSealStoreTool(t, client, "qrdb_seal_store_put", map[string]any{"name": "db-key", "secret": "%%%not-base64"})
	if !res.IsError {
		t.Fatalf("put with a non-base64 secret: result %+v, want IsError", res)
	}
	if store.total() != 0 {
		t.Fatalf("a refused put reached the seal store")
	}
}

// TestSealStoreTools_TenantAdminRefused asserts a tenant Admin, who does not
// hold seal:store_manage, is refused and never reaches the seal store.
func TestSealStoreTools_TenantAdminRefused(t *testing.T) {
	store := newStubSealStore()
	client := startSealStoreMCP(t, store, "acme", authtest.AdminIdentity("acme", "acme-admin"))
	res := callSealStoreTool(t, client, "qrdb_seal_store_list", nil)
	wantToolCode(t, "qrdb_seal_store_list", res, wire.CodePermissionDenied)
	if store.total() != 0 {
		t.Fatalf("a refused qrdb_seal_store_list reached the seal store")
	}
}
