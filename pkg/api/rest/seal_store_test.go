// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package rest

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

var errStubSealStoreNotFound = errors.New("seal store stub: secret not found")

// stubSealStore is an in-memory service.SealStoreServicer that records
// every call it receives.
type stubSealStore struct {
	mu       sync.Mutex
	secrets  map[string][]byte
	calls    []string
	resealed []string
}

func newStubSealStore() *stubSealStore {
	return &stubSealStore{secrets: map[string][]byte{}}
}

func (s *stubSealStore) record(call string) {
	s.calls = append(s.calls, call)
}

func (s *stubSealStore) Put(_ context.Context, name string, secret []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("put")
	s.secrets[name] = slices.Clone(secret)
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
	return slices.Clone(v), nil
}

func (s *stubSealStore) Delete(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("delete")
	delete(s.secrets, name)
	return nil
}

func (s *stubSealStore) names() []string {
	out := make([]string, 0, len(s.secrets))
	for n := range s.secrets {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (s *stubSealStore) List(_ context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("list")
	return s.names(), nil
}

func (s *stubSealStore) Reseal(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("reseal")
	s.resealed = append(s.resealed, name)
	return nil
}

func (s *stubSealStore) Status(_ context.Context) (*seal.SealStoreStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("status")
	names := s.names()
	return &seal.SealStoreStatus{Available: true, Strategy: "passphrase", SecretCount: len(names), SecretNames: names}, nil
}

func (s *stubSealStore) callList() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

func newSealStoreTestServer(t *testing.T, caller *auth.Identity, store *stubSealStore) *Server {
	t.Helper()
	roles := authtest.BuiltInRoles(t, tenantid.System, caller.TenantID)
	return NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithAuthMiddleware(authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles),
			authmw.WithAuthenticator(authtest.NewFixedAuthenticator(caller)))),
		WithSealStoreService(store))
}

func serveSealStore(t *testing.T, srv *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, r)
	req.TLS = &tls.ConnectionState{}
	rec := httptest.NewRecorder()
	srv.server.Handler.ServeHTTP(rec, req)
	return rec
}

// TestSealStoreRoutes_SystemSO drives every seal store route as a system
// Security Officer (seal:store_manage) and checks each reaches the store.
func TestSealStoreRoutes_SystemSO(t *testing.T) {
	store := newStubSealStore()
	srv := newSealStoreTestServer(t, authtest.SOIdentity(tenantid.System, "so-1"), store)

	rec := serveSealStore(t, srv, http.MethodPost, "/v1/seal-store/secrets/alpha", map[string]any{"secret": []byte("s3cret")})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("put status = %d body=%q, want 204", rec.Code, rec.Body.String())
	}

	rec = serveSealStore(t, srv, http.MethodGet, "/v1/seal-store/secrets/alpha", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d body=%q, want 200", rec.Code, rec.Body.String())
	}
	var got sealStoreSecretResponse
	decodeJSON(t, rec, &got)
	if got.Name != "alpha" || string(got.Secret) != "s3cret" {
		t.Fatalf("get = %+v, want alpha/s3cret", got)
	}

	rec = serveSealStore(t, srv, http.MethodGet, "/v1/seal-store/secrets", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%q, want 200", rec.Code, rec.Body.String())
	}
	var list sealStoreListResponse
	decodeJSON(t, rec, &list)
	if !slices.Equal(list.Names, []string{"alpha"}) {
		t.Fatalf("list = %v, want [alpha]", list.Names)
	}

	rec = serveSealStore(t, srv, http.MethodPost, "/v1/seal-store/secrets/alpha/reseal", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("reseal status = %d body=%q, want 204", rec.Code, rec.Body.String())
	}
	if !slices.Equal(store.resealed, []string{"alpha"}) {
		t.Fatalf("resealed = %v, want [alpha]", store.resealed)
	}

	rec = serveSealStore(t, srv, http.MethodGet, "/v1/seal-store/status", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status status = %d body=%q, want 200", rec.Code, rec.Body.String())
	}
	var st transport.SealStoreStatusResponse
	decodeJSON(t, rec, &st)
	if !st.Available || st.SealerID != "passphrase" || st.SecretCount != 1 || !slices.Equal(st.SecretNames, []string{"alpha"}) {
		t.Fatalf("status = %+v", st)
	}

	rec = serveSealStore(t, srv, http.MethodDelete, "/v1/seal-store/secrets/alpha", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d body=%q, want 204", rec.Code, rec.Body.String())
	}

	rec = serveSealStore(t, srv, http.MethodGet, "/v1/seal-store/secrets/alpha", nil)
	if rec.Code < 400 {
		t.Fatalf("get after delete status = %d, want an error", rec.Code)
	}

	want := []string{"put", "get", "list", "reseal", "status", "delete", "get"}
	if calls := store.callList(); !slices.Equal(calls, want) {
		t.Fatalf("store calls = %v, want %v", calls, want)
	}
}

// TestSealStoreRoutes_TenantAdminRefused checks a tenant Admin is refused
// with 403 and never reaches the store: seal:store_manage is node-scoped.
func TestSealStoreRoutes_TenantAdminRefused(t *testing.T) {
	store := newStubSealStore()
	srv := newSealStoreTestServer(t, authtest.AdminIdentity("acme", "acme-admin"), store)

	rec := serveSealStore(t, srv, http.MethodPost, "/v1/seal-store/secrets/alpha", map[string]any{"secret": []byte("x")})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%q, want 403", rec.Code, rec.Body.String())
	}
	if calls := store.callList(); len(calls) != 0 {
		t.Fatalf("a refused call reached the store: %v", calls)
	}
}
