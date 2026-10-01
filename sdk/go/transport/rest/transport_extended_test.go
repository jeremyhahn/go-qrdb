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
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// Test infrastructure: a generic httptest.Server-backed Transport that
// pattern-matches inbound requests.
// ===========================================================================

// route describes one inbound HTTP handler. The matcher is invoked in
// the order added.
type route struct {
	match func(r *http.Request) bool
	fn    http.HandlerFunc
}

// routerServer is a tiny pattern-matching HTTP test server that lets each
// test attach its own routes inline.
type routerServer struct {
	routes []route
}

func (rs *routerServer) handle(matcher func(*http.Request) bool, fn http.HandlerFunc) {
	rs.routes = append(rs.routes, route{match: matcher, fn: fn})
}

func (rs *routerServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for _, rt := range rs.routes {
		if rt.match(r) {
			rt.fn(w, r)
			return
		}
	}
	http.NotFound(w, r)
}

// newExtTransport returns a Transport wired to an httptest.Server backed
// by the given routerServer.
func newExtTransport(t *testing.T) (*Transport, *routerServer, *httptest.Server) {
	t.Helper()
	rs := &routerServer{}
	srv := httptest.NewServer(rs)
	t.Cleanup(srv.Close)
	return newWiredTransport(srv), rs, srv
}

// pathPrefix returns a matcher for a specific method+path-prefix combination.
func pathPrefix(method, prefix string) func(*http.Request) bool {
	return func(r *http.Request) bool {
		return r.Method == method && strings.HasPrefix(r.URL.Path, prefix)
	}
}

// exactPath returns a matcher for a specific method+path combination.
func exactPath(method, path string) func(*http.Request) bool {
	return func(r *http.Request) bool {
		return r.Method == method && r.URL.Path == path
	}
}

// writeJSON encodes v as JSON into w with a 200 status code.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ===========================================================================
// CredentialSubmit / Strategy / Delete  (rest/transport_credentials.go)
// ===========================================================================

func TestRESTCredentialSubmit_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	var captured transport.CredentialSubmitRequest
	rs.handle(exactPath(http.MethodPost, "/v1/credentials/submit"), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.WriteHeader(http.StatusNoContent)
	})

	if err := rt.CredentialSubmit(context.Background(), &transport.CredentialSubmitRequest{
		Name:  "db",
		Value: []byte("secret"),
	}); err != nil {
		t.Fatalf("CredentialSubmit: %v", err)
	}
	if captured.Name != "db" || string(captured.Value) != "secret" {
		t.Errorf("captured = %+v", captured)
	}
}

func TestRESTCredentialSubmit_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/credentials/submit"), func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	err := rt.CredentialSubmit(context.Background(), &transport.CredentialSubmitRequest{Name: "x"})
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T", err)
	}
}

func TestRESTCredentialSubmit_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	err := rt.CredentialSubmit(context.Background(), &transport.CredentialSubmitRequest{})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTCredentialStrategy_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/v1/credentials/strategy"), func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, &transport.CredentialStrategyResponse{Strategy: "vault"})
	})

	resp, err := rt.CredentialStrategy(context.Background())
	if err != nil {
		t.Fatalf("CredentialStrategy: %v", err)
	}
	if resp.Strategy != "vault" {
		t.Errorf("Strategy = %q", resp.Strategy)
	}
}

func TestRESTCredentialStrategy_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/v1/credentials/strategy"), func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusServiceUnavailable)
	})
	_, err := rt.CredentialStrategy(context.Background())
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T", err)
	}
}

func TestRESTCredentialStrategy_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.CredentialStrategy(context.Background())
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTCredentialStrategy_BadJSON(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/v1/credentials/strategy"), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{{not valid"))
	})
	_, err := rt.CredentialStrategy(context.Background())
	if err == nil {
		t.Fatal("expected JSON decode error")
	}
}

func TestRESTCredentialDelete_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	var capturedPath string
	rs.handle(pathPrefix(http.MethodDelete, "/v1/credentials/"), func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	if err := rt.CredentialDelete(context.Background(), "expired-token"); err != nil {
		t.Fatalf("CredentialDelete: %v", err)
	}
	if capturedPath != "/v1/credentials/expired-token" {
		t.Errorf("path = %q", capturedPath)
	}
}

func TestRESTCredentialDelete_OK_Status(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodDelete, "/v1/credentials/"), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if err := rt.CredentialDelete(context.Background(), "x"); err != nil {
		t.Fatalf("CredentialDelete: %v", err)
	}
}

func TestRESTCredentialDelete_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodDelete, "/v1/credentials/"), func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fail", http.StatusForbidden)
	})
	err := rt.CredentialDelete(context.Background(), "x")
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T", err)
	}
}

func TestRESTCredentialDelete_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	err := rt.CredentialDelete(context.Background(), "x")
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

// ===========================================================================
// Scan / Exists / Capabilities  (rest/transport.go)
// ===========================================================================

func TestRESTScan_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/kv/scan"), func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, &transport.KVScanResponse{Pairs: map[string][]byte{
			"p/a": []byte("v1"),
			"p/b": []byte("v2"),
		}})
	})
	out, err := rt.Scan(context.Background(), "p/")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("len = %d", len(out))
	}
}

func TestRESTScan_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/kv/scan"), func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fail", http.StatusInternalServerError)
	})
	_, err := rt.Scan(context.Background(), "p")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTScan_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.Scan(context.Background(), "p")
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTExists_True(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/kv/"), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("v"))
	})
	ok, err := rt.Exists(context.Background(), "k")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !ok {
		t.Error("expected true")
	}
}

func TestRESTExists_False(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/kv/"), func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	ok, err := rt.Exists(context.Background(), "k")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if ok {
		t.Error("expected false")
	}
}

func TestRESTExists_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.Exists(context.Background(), "k")
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTCapabilities_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/capabilities"), func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, &transport.CapabilitiesResponse{
			KV:    true,
			MVCC:  true,
			Index: true,
		})
	})
	caps, err := rt.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if !caps.KV || !caps.MVCC {
		t.Errorf("caps = %+v", caps)
	}
}

func TestRESTCapabilities_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/capabilities"), func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fail", http.StatusInternalServerError)
	})
	_, err := rt.Capabilities(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTCapabilities_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.Capabilities(context.Background())
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

// ===========================================================================
// DEK transport tests
// ===========================================================================

func TestRESTDEK_EncryptPostsBase64Body(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/dek/encrypt"), func(w http.ResponseWriter, r *http.Request) {
		var req transport.TenantDEKEncryptRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		if string(req.Plaintext) != "secret" || string(req.AAD) != "ctx" {
			t.Errorf("request = %+v", req)
		}
		writeJSON(w, &transport.TenantDEKEncryptResponse{Ciphertext: []byte("QDEK..."), Version: 2})
	})
	resp, err := rt.TenantDEKEncrypt(context.Background(), &transport.TenantDEKEncryptRequest{Plaintext: []byte("secret"), AAD: []byte("ctx")})
	if err != nil || resp.Version != 2 || string(resp.Ciphertext) != "QDEK..." {
		t.Fatalf("TenantDEKEncrypt = %+v, %v", resp, err)
	}
}

func TestRESTDEK_StatusCarriesTheTenantQuery(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/v1/dek/status"), func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("tenant_id"); got != "acme" {
			t.Errorf("tenant_id = %q", got)
		}
		writeJSON(w, &transport.TenantDEKStatusResponse{TenantID: "acme", CurrentVersion: 3, Versions: []transport.TenantDEKVersion{{Version: 3}}})
	})
	st, err := rt.TenantDEKStatus(context.Background(), &transport.TenantDEKRequest{TenantID: "acme"})
	if err != nil || st.CurrentVersion != 3 || len(st.Versions) != 1 {
		t.Fatalf("TenantDEKStatus = %+v, %v", st, err)
	}
}

// TestRESTDEK_EveryVerbHitsItsRoute drives the remaining verbs against their
// routes and checks the decoded bodies.
func TestRESTDEK_EveryVerbHitsItsRoute(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	status := &transport.TenantDEKStatusResponse{TenantID: "acme", CurrentVersion: 2}
	rs.handle(exactPath(http.MethodPost, "/v1/dek/rotate"), func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, status) })
	rs.handle(exactPath(http.MethodPost, "/v1/dek/retire"), func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, status) })
	rs.handle(exactPath(http.MethodPost, "/v1/dek/decrypt"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.TenantDEKDecryptResponse{Plaintext: []byte("p"), Version: 1})
	})
	rs.handle(exactPath(http.MethodPost, "/v1/dek/rewrap"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.TenantDEKRewrapResponse{Ciphertext: []byte("c"), FromVersion: 1, ToVersion: 2})
	})
	ctx := context.Background()
	if st, err := rt.TenantDEKRotate(ctx, &transport.TenantDEKRequest{}); err != nil || st.CurrentVersion != 2 {
		t.Errorf("rotate = %+v, %v", st, err)
	}
	if st, err := rt.TenantDEKRetire(ctx, &transport.TenantDEKRetireRequest{Version: 1}); err != nil || st.CurrentVersion != 2 {
		t.Errorf("retire = %+v, %v", st, err)
	}
	if d, err := rt.TenantDEKDecrypt(ctx, &transport.TenantDEKDecryptRequest{Ciphertext: []byte("c")}); err != nil || string(d.Plaintext) != "p" {
		t.Errorf("decrypt = %+v, %v", d, err)
	}
	if r, err := rt.TenantDEKRewrap(ctx, &transport.TenantDEKRewrapRequest{Ciphertext: []byte("c")}); err != nil || r.ToVersion != 2 {
		t.Errorf("rewrap = %+v, %v", r, err)
	}
}

func TestRESTDEK_ServerRefusalIsReturned(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/dek/retire"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		writeJSON(w, map[string]string{"error": "dek: cannot retire the current key version"})
	})
	if _, err := rt.TenantDEKRetire(context.Background(), &transport.TenantDEKRetireRequest{Version: 2}); err == nil {
		t.Fatal("a 409 must surface as an error")
	}
}

func TestRESTDEK_NilRequestAndClosed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	ctx := context.Background()
	var nre *transport.NilRequestError
	if _, err := rt.TenantDEKEncrypt(ctx, nil); !errors.As(err, &nre) {
		t.Errorf("nil encrypt: %v", err)
	}
	if _, err := rt.TenantDEKStatus(ctx, nil); !errors.As(err, &nre) {
		t.Errorf("nil status: %v", err)
	}
	_ = rt.Close()
	var ce *transport.ClosedError
	if _, err := rt.TenantDEKStatus(ctx, &transport.TenantDEKRequest{}); !errors.As(err, &ce) {
		t.Errorf("status after close: %v", err)
	}
	if _, err := rt.TenantDEKRewrap(ctx, &transport.TenantDEKRewrapRequest{}); !errors.As(err, &ce) {
		t.Errorf("rewrap after close: %v", err)
	}
}

// ===========================================================================
// Index transport tests (rest/transport_index.go)
// ===========================================================================

func TestRESTIndex_Create_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	var captured indexCreateWireRequest
	rs.handle(exactPath(http.MethodPost, "/index/create"), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		writeJSON(w, &transport.OKResponse{Status: "ok"})
	})
	err := rt.IndexCreate(context.Background(), "idx", "email", transport.IndexConfig{Unique: true})
	if err != nil {
		t.Fatalf("IndexCreate: %v", err)
	}
	if captured.Name != "idx" || captured.FieldName != "email" || !captured.Unique {
		t.Errorf("captured = %+v", captured)
	}
}

func TestRESTIndex_Create_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	err := rt.IndexCreate(context.Background(), "i", "f", transport.IndexConfig{})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTIndex_Drop_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	var captured indexDropWireRequest
	rs.handle(exactPath(http.MethodPost, "/index/drop"), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		writeJSON(w, &transport.OKResponse{Status: "ok"})
	})
	if err := rt.IndexDrop(context.Background(), "idx"); err != nil {
		t.Fatalf("IndexDrop: %v", err)
	}
	if captured.Name != "idx" {
		t.Errorf("Name = %q", captured.Name)
	}
}

func TestRESTIndex_Drop_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	err := rt.IndexDrop(context.Background(), "i")
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTIndex_Query_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/index/query"), func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, indexQueryWireResponse{Keys: []string{"k1", "k2"}})
	})
	resp, err := rt.IndexQuery(context.Background(), "idx", "alice")
	if err != nil {
		t.Fatalf("IndexQuery: %v", err)
	}
	if len(resp.Keys) != 2 {
		t.Errorf("Keys = %+v", resp.Keys)
	}
}

func TestRESTIndex_Query_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/index/query"), func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fail", http.StatusInternalServerError)
	})
	_, err := rt.IndexQuery(context.Background(), "idx", "v")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTIndex_Query_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.IndexQuery(context.Background(), "i", "v")
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTIndex_Scan_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/index/scan"), func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, indexScanWireResponse{
			Entries: map[string][]string{
				"a": {"k1"},
				"b": {"k2", "k3"},
			},
		})
	})
	resp, err := rt.IndexScan(context.Background(), "idx", spIdx("a"), spIdx("z"))
	if err != nil {
		t.Fatalf("IndexScan: %v", err)
	}
	if len(resp.Entries) != 2 {
		t.Errorf("Entries len = %d", len(resp.Entries))
	}
	// Verify sorted.
	if resp.Entries[0].Value != "a" {
		t.Errorf("first entry = %+v", resp.Entries[0])
	}
}

func TestRESTIndex_Scan_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/index/scan"), func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fail", http.StatusInternalServerError)
	})
	_, err := rt.IndexScan(context.Background(), "i", spIdx("a"), spIdx("z"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTIndex_Scan_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.IndexScan(context.Background(), "i", spIdx("a"), spIdx("z"))
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTIndex_List_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/index/list"), func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, indexListWireResponse{Names: []string{"i1", "i2"}})
	})
	resp, err := rt.IndexList(context.Background())
	if err != nil {
		t.Fatalf("IndexList: %v", err)
	}
	if len(resp.Names) != 2 {
		t.Errorf("len = %d", len(resp.Names))
	}
}

func TestRESTIndex_List_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/index/list"), func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fail", http.StatusInternalServerError)
	})
	_, err := rt.IndexList(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTIndex_List_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.IndexList(context.Background())
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func spIdx(s string) *string { return &s }
