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

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/keywrap"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// dekStores resolves one in-memory store per tenant.
type dekStores map[string]*kvstore.MemoryKVStore

func (d dekStores) Resolve(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	s, ok := d[tenantID]
	if !ok {
		s = kvstore.NewMemoryKVStore()
		d[tenantID] = s
	}
	return s, nil
}

// newDEKServer is a REST server over the production keyring service.
// fakeDEKKeyWrapProvider is a same-shaped keywrap.Provider test double --
// it "wraps" by copying bytes verbatim, the identical shape
// pkg/keywrap's own fakeHardwareProvider test double uses.
type fakeDEKKeyWrapProvider struct{}

func (fakeDEKKeyWrapProvider) ProviderID() string { return keywrap.SoftwareProviderID }

func (fakeDEKKeyWrapProvider) Wrap(_ context.Context, _ string, dek []byte) ([]byte, error) {
	out := make([]byte, len(dek))
	copy(out, dek)
	return out, nil
}

func (fakeDEKKeyWrapProvider) Unwrap(_ context.Context, _ string, wrapped []byte) ([]byte, error) {
	out := make([]byte, len(wrapped))
	copy(out, wrapped)
	return out, nil
}

// testDEKKeyWrapRegistry returns a Registry whose "software" entry is
// fakeDEKKeyWrapProvider, so a test gets a fully working DEK keyring with
// no filesystem key directory.
func testDEKKeyWrapRegistry() keywrap.Registry {
	r := keywrap.NewRegistry()
	_ = r.Register(keywrap.SoftwareProviderID, func() (keywrap.Provider, error) {
		return fakeDEKKeyWrapProvider{}, nil
	})
	return r
}

func newDEKServer(t *testing.T) *Server {
	t.Helper()
	keyring, err := dek.NewKeyring(dekStores{}, dek.WithKeyWrapRegistry(testDEKKeyWrapRegistry()))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := service.NewDEKService(keyring)
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0", WithDEKService(svc))
}

// dekDo serves one request as tenant acme through the router.
func dekDo(t *testing.T, s *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req = req.WithContext(tenant.WithTenantID(req.Context(), "acme"))
	rec := httptest.NewRecorder()
	s.dispatch(rec, req)
	return rec
}

func dekDecode(t *testing.T, rec *httptest.ResponseRecorder, out any) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.NewDecoder(rec.Body).Decode(out); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func TestHandleDEK_LifecycleThroughTheRouter(t *testing.T) {
	s := newDEKServer(t)
	aad := []byte("acme/db/password/v1")

	var enc transport.TenantDEKEncryptResponse
	dekDecode(t, dekDo(t, s, http.MethodPost, "/v1/dek/encrypt", transport.TenantDEKEncryptRequest{Plaintext: []byte("s3cret"), AAD: aad}), &enc)
	if enc.Version != 1 {
		t.Fatalf("encrypt version = %d", enc.Version)
	}
	var st transport.TenantDEKStatusResponse
	dekDecode(t, dekDo(t, s, http.MethodPost, "/v1/dek/rotate", transport.TenantDEKRequest{}), &st)
	if st.CurrentVersion != 2 || st.TenantID != "acme" {
		t.Fatalf("rotate = %+v", st)
	}
	var rw transport.TenantDEKRewrapResponse
	dekDecode(t, dekDo(t, s, http.MethodPost, "/v1/dek/rewrap", transport.TenantDEKRewrapRequest{Ciphertext: enc.Ciphertext, AAD: aad}), &rw)
	if rw.FromVersion != 1 || rw.ToVersion != 2 {
		t.Fatalf("rewrap = %+v", rw)
	}
	dekDecode(t, dekDo(t, s, http.MethodPost, "/v1/dek/retire", transport.TenantDEKRetireRequest{Version: 1}), &st)
	if st.Versions[0].RetiredAt == nil {
		t.Fatalf("retire = %+v", st)
	}
	var dec transport.TenantDEKDecryptResponse
	dekDecode(t, dekDo(t, s, http.MethodPost, "/v1/dek/decrypt", transport.TenantDEKDecryptRequest{Ciphertext: rw.Ciphertext, AAD: aad}), &dec)
	if !bytes.Equal(dec.Plaintext, []byte("s3cret")) {
		t.Fatalf("decrypt = %+v", dec)
	}
	dekDecode(t, dekDo(t, s, http.MethodGet, "/v1/dek/status?tenant_id=acme", nil), &st)
	if st.CurrentVersion != 2 || len(st.Versions) != 2 {
		t.Fatalf("status = %+v", st)
	}
}

// TestHandleDEK_RefusalsCarryTheirStatus pins the HTTP rendering of the
// shared classification.
func TestHandleDEK_RefusalsCarryTheirStatus(t *testing.T) {
	s := newDEKServer(t)
	var enc transport.TenantDEKEncryptResponse
	dekDecode(t, dekDo(t, s, http.MethodPost, "/v1/dek/encrypt", transport.TenantDEKEncryptRequest{Plaintext: []byte("x"), AAD: []byte("a")}), &enc)

	cases := []struct {
		name, method, path string
		body               any
		want               int
	}{
		{"cross tenant", http.MethodGet, "/v1/dek/status?tenant_id=globex", nil, http.StatusForbidden},
		{"aad mismatch", http.MethodPost, "/v1/dek/decrypt", transport.TenantDEKDecryptRequest{Ciphertext: enc.Ciphertext, AAD: []byte("b")}, http.StatusBadRequest},
		{"retire current", http.MethodPost, "/v1/dek/retire", transport.TenantDEKRetireRequest{Version: 1}, http.StatusConflict},
		{"unknown version", http.MethodPost, "/v1/dek/retire", transport.TenantDEKRetireRequest{Version: 9}, http.StatusNotFound},
		{"malformed", http.MethodPost, "/v1/dek/rewrap", transport.TenantDEKRewrapRequest{Ciphertext: []byte("junk")}, http.StatusBadRequest},
	}
	for _, c := range cases {
		if rec := dekDo(t, s, c.method, c.path, c.body); rec.Code != c.want {
			t.Errorf("%s: status %d (%s), want %d", c.name, rec.Code, rec.Body.String(), c.want)
		}
	}
}

func TestHandleDEK_BadBodiesAre400(t *testing.T) {
	s := newDEKServer(t)
	for _, path := range []string{"/v1/dek/rotate", "/v1/dek/retire", "/v1/dek/encrypt", "/v1/dek/decrypt", "/v1/dek/rewrap"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{not json"))
		rec := httptest.NewRecorder()
		s.dispatch(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", path, rec.Code)
		}
	}
}

// TestHandleDEK_RoutesAbsentWithoutTheService: a server built without the
// keyring does not serve the routes at all.
func TestHandleDEK_RoutesAbsentWithoutTheService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodGet, "/v1/dek/status", nil)
	rec := httptest.NewRecorder()
	s.dispatch(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("status %d, want the route to be absent", rec.Code)
	}
}

func TestWriteDEKErrorFallsBackForForeignErrors(t *testing.T) {
	rec := httptest.NewRecorder()
	writeDEKError(rec, &dek.StoreError{TenantID: "acme", Op: "read", Err: context.DeadlineExceeded})
	if rec.Code < 500 {
		t.Fatalf("status %d, want a server error for an unclassified failure", rec.Code)
	}
}
